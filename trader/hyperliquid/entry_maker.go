package hyperliquid

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/sonirico/go-hyperliquid"

	"nofx/logger"
	"nofx/trader/connector"
)

// ErrMakerEntryUnfilled is returned when the passive entry did not fill within
// its wait. It is not a failure of the trade: the caller falls back to the
// aggressive order it would have sent anyway.
var ErrMakerEntryUnfilled = errors.New("maker entry did not fill before the timeout")

const (
	// makerPollInterval is how often the resting order's status is queried.
	makerPollInterval = 1500 * time.Millisecond
	// makerFillTolerance treats a fill this close to the request as complete
	// (venue size rounding leaves fractions of a unit).
	makerFillTolerance = 0.999
	// makerQueryGrace tolerates a transient status-query failure while the order
	// is still resting, instead of abandoning it.
	makerQueryGrace = 3
)

// makerEntryAction is the pure decision taken on each poll of a resting order.
type makerEntryAction int

const (
	makerEntryWait makerEntryAction = iota
	makerEntryFilled
	makerEntryAcceptPartial
	makerEntryFallback
)

// makerEntryDecision decides what to do with a partly filled resting order.
//
//   - filled to (near) the request → done, whatever the deadline says
//   - still waiting inside the deadline → keep polling
//   - deadline reached with a partial fill → keep what filled and cancel the rest.
//     Topping up with an aggressive order would add taker cost and a second chance
//     of rejection, and an undersized position is the safe direction: the risk
//     gate approved a maximum, not a requirement.
//   - deadline reached with nothing filled → fall back to the market path
func makerEntryDecision(filled, requested float64, timedOut bool) makerEntryAction {
	if requested <= 0 {
		return makerEntryFallback
	}
	if filled >= requested*makerFillTolerance {
		return makerEntryFilled
	}
	if !timedOut {
		return makerEntryWait
	}
	if filled > 0 {
		return makerEntryAcceptPartial
	}
	return makerEntryFallback
}

// passiveEntryPrice picks a price that cannot cross the book, so the post-only
// (ALO) order is not rejected: a buy joins the best bid, a sell joins the best
// ask. ok is false when that side of the book is empty.
func passiveEntryPrice(bids, asks [][]float64, isLong bool) (float64, bool) {
	levels := asks
	if isLong {
		levels = bids
	}
	for _, level := range levels {
		if len(level) == 0 {
			continue
		}
		if level[0] > 0 {
			return level[0], true
		}
	}
	return 0, false
}

// aloOrderRequest builds the post-only entry order. ReduceOnly is false because
// this opens (or extends) a position — the opposite of a protective exit, which
// this feature deliberately never routes through a resting order.
func aloOrderRequest(coin string, isBuy bool, size, price float64, cloid string) hyperliquid.CreateOrderRequest {
	order := hyperliquid.CreateOrderRequest{
		Coin:  coin,
		IsBuy: isBuy,
		Size:  size,
		Price: price,
		OrderType: hyperliquid.OrderType{
			Limit: &hyperliquid.LimitOrderType{
				Tif: hyperliquid.TifAlo,
			},
		},
		ReduceOnly: false,
	}
	if cloid != "" {
		order.ClientOrderID = &cloid
	}
	return order
}

// newMakerCloid returns a client order id (0x + 32 hex chars), which lets the
// entry be tracked and cancelled by id rather than by symbol-wide sweeps.
func newMakerCloid() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return "0x" + hex.EncodeToString(b[:])
}

// MakerEntry opens a position as a post-only order on the passive side of the
// book, paying the maker fee, and reports ErrMakerEntryUnfilled when it does not
// fill in time so the caller can submit the aggressive order instead.
//
// It never places a stop/take-profit and never touches the close path: this is
// entry-side only, so a protective exit can still always fill immediately.
func (t *HyperliquidTrader) MakerEntry(symbol string, quantity float64, leverage int, isLong bool, timeout time.Duration) (*connector.OrderResult, error) {
	if quantity <= 0 {
		return nil, fmt.Errorf("maker entry: quantity must be positive")
	}
	if timeout <= 0 {
		timeout = 20 * time.Second
	}

	if err := t.CancelAllOrders(symbol); err != nil {
		logger.Infof("  ⚠ Failed to cancel old pending orders: %v", err)
	}

	coin := convertSymbolToHyperliquid(symbol)
	isXyz := strings.HasPrefix(coin, "xyz:")

	// Leverage first, exactly as the aggressive path does: the position must be
	// opened on the leverage the strategy (and the risk gate) assumed.
	if !isXyz {
		if err := t.SetLeverage(symbol, leverage); err != nil {
			return nil, err
		}
	} else if err := t.setXyzLeverage(coin, leverage, t.isCrossMargin); err != nil {
		logger.Infof("  ⚠️ xyz dex %s leverage not applied (%v) - proceeding on the dex's default leverage", coin, err)
	}

	bids, asks, err := t.GetOrderBook(symbol, 5)
	if err != nil {
		return nil, fmt.Errorf("maker entry: order book unavailable: %w", err)
	}
	price, ok := passiveEntryPrice(bids, asks, isLong)
	if !ok {
		return nil, fmt.Errorf("maker entry: no resting liquidity on the passive side")
	}
	price = t.roundPriceToSigfigs(price)

	size := quantity
	if !isXyz {
		size = t.roundToSzDecimals(coin, quantity)
	}
	if size <= 0 {
		return nil, fmt.Errorf("maker entry: quantity rounds to zero at the venue's precision")
	}

	cloid := newMakerCloid()
	status, err := t.placeMakerOrder(coin, isLong, size, price, cloid)
	if err != nil {
		// A rejected post-only order (it would have crossed, or HIP-3 does not
		// support ALO) is not a failed trade: fall back to the market path.
		return nil, fmt.Errorf("maker entry: post-only order rejected: %w", err)
	}
	if status.Filled != nil {
		// Filled on arrival: report it as a maker fill (ALO cannot have crossed).
		avg := parseVenueFloat(status.Filled.AvgPx, price)
		filled := parseVenueFloat(status.Filled.TotalSz, size)
		return &connector.OrderResult{
			OrderID:      fmt.Sprintf("%d", status.Filled.Oid),
			Symbol:       symbol,
			Status:       "FILLED",
			AvgFillPrice: avg,
			FilledQty:    filled,
			IsMaker:      true,
		}, nil
	}

	oid := int64(0)
	if status.Resting != nil {
		oid = status.Resting.Oid
	}
	logger.Infof("  🕰 Post-only entry resting: %s %s size=%.8f @ %.8f (waiting up to %s for the maker fill)",
		coin, sideLabel(isLong), size, price, timeout)

	filled, timedOut, err := t.waitForMakerFill(coin, oid, cloid, size, timeout)
	if err != nil {
		return nil, err
	}

	action := makerEntryDecision(filled, size, timedOut)
	if action == makerEntryFallback {
		// Nothing filled: withdraw and let the caller use the market path.
		if err := t.CancelAllOrders(symbol); err != nil {
			logger.Infof("  ⚠ Failed to cancel the resting post-only order: %v", err)
		}
		logger.Infof("  ↪️ Post-only entry did not fill in %s, falling back to the market order", timeout)
		return nil, ErrMakerEntryUnfilled
	}

	if action == makerEntryAcceptPartial {
		if err := t.CancelAllOrders(symbol); err != nil {
			logger.Infof("  ⚠ Failed to cancel the post-only remainder: %v", err)
		}
		logger.Infof("  ⚠️ Post-only entry partially filled (%.8f of %.8f) — keeping the filled part, remainder cancelled", filled, size)
	}

	avgPx, fee := t.makerFillDetails(oid, price, coin)
	logger.Infof("  ✓ Maker entry filled: %s %s size=%.8f @ %.8f (fee %.4f)", coin, sideLabel(isLong), filled, avgPx, fee)

	return &connector.OrderResult{
		OrderID:      fmt.Sprintf("%d", oid),
		Symbol:       symbol,
		Status:       "FILLED",
		AvgFillPrice: avgPx,
		FilledQty:    filled,
		Fee:          fee,
		IsMaker:      true,
	}, nil
}

// placeMakerOrder submits the post-only order, routing HIP-3 assets through the
// hand-signed xyz path.
func (t *HyperliquidTrader) placeMakerOrder(coin string, isLong bool, size, price float64, cloid string) (hyperliquid.OrderStatus, error) {
	if strings.HasPrefix(coin, "xyz:") {
		if err := t.placeXyzOrderWithTif(coin, isLong, size, price, false, hyperliquid.TifAlo, cloid); err != nil {
			return hyperliquid.OrderStatus{}, err
		}
		// The xyz path is hand-signed and does not return the venue's order
		// status; the caller polls by client order id instead.
		return hyperliquid.OrderStatus{}, nil
	}
	return t.placeOrderWithBuilderFee(aloOrderRequest(coin, isLong, size, price, cloid))
}

// waitForMakerFill polls the resting order until it fills, dies, or the timeout
// expires, returning the filled size and whether the wait timed out.
func (t *HyperliquidTrader) waitForMakerFill(coin string, oid int64, cloid string, requested float64, timeout time.Duration) (float64, bool, error) {
	deadline := time.Now().Add(timeout)
	filled := 0.0
	consecutiveErrors := 0

	for {
		remaining, orig, status, err := t.queryMakerOrder(oid, cloid)
		switch {
		case err != nil:
			// A transient query failure must not abandon a live resting order:
			// keep polling until the deadline, then report what we know.
			consecutiveErrors++
			if consecutiveErrors == 1 {
				logger.Infof("  ⚠ Could not query the resting post-only order (%v) — retrying", err)
			}
			if consecutiveErrors >= makerQueryGrace && time.Now().After(deadline) {
				return filled, true, nil
			}
		case orig > 0:
			consecutiveErrors = 0
			filled = math.Max(0, orig-remaining)
			if status == hyperliquid.OrderStatusValueFilled {
				filled = orig
			}
			if status == hyperliquid.OrderStatusValueCanceled || status == hyperliquid.OrderStatusValueRejected {
				return filled, false, nil
			}
		}

		if action := makerEntryDecision(filled, requested, time.Now().After(deadline)); action != makerEntryWait {
			return filled, action != makerEntryFilled, nil
		}

		if time.Now().After(deadline) {
			return filled, true, nil
		}
		time.Sleep(makerPollInterval)
	}
}

// queryMakerOrder returns the remaining size, original size and venue status of
// an order, preferring the client order id (which is unique to this entry and
// available even before the venue assigns an id).
func (t *HyperliquidTrader) queryMakerOrder(oid int64, cloid string) (remaining, orig float64, status hyperliquid.OrderStatusValue, err error) {
	var result *hyperliquid.OrderQueryResult
	if cloid != "" {
		result, err = t.exchange.Info().QueryOrderByCloid(t.ctx, t.walletAddr, cloid)
	} else if oid != 0 {
		result, err = t.exchange.Info().QueryOrderByOid(t.ctx, t.walletAddr, oid)
	} else {
		return 0, 0, "", fmt.Errorf("no order identifier to query")
	}
	if err != nil {
		return 0, 0, "", err
	}
	if result == nil {
		return 0, 0, "", fmt.Errorf("empty order status")
	}
	if result.Status != hyperliquid.OrderQueryStatusSuccess {
		return 0, 0, "", fmt.Errorf("order not found by the venue (status %s)", result.Status)
	}
	queried := result.Order.Order
	return parseVenueFloat(queried.Sz, 0), parseVenueFloat(queried.OrigSz, 0), result.Order.Status, nil
}

// makerFillDetails returns the average fill price and fee for the entry. Fills
// are matched by order id; when the venue has not reported them yet the limit
// price is used (a maker fill is at its limit or better), which keeps the
// recorded entry sane rather than zero.
func (t *HyperliquidTrader) makerFillDetails(oid int64, limitPrice float64, coin string) (avgPx, fee float64) {
	if oid != 0 {
		fills, err := t.exchange.Info().UserFills(t.ctx, hyperliquid.UserFillsParams{Address: t.walletAddr})
		if err != nil {
			logger.Infof("  ⚠ Could not read the entry fill (using the limit price): %v", err)
			return limitPrice, 0
		}
		totalSize, totalNotional, totalFee := 0.0, 0.0, 0.0
		for _, fill := range fills {
			if fill.Oid != oid || fill.Coin != coin {
				continue
			}
			size := parseVenueFloat(fill.Size, 0)
			price := parseVenueFloat(fill.Price, 0)
			if size <= 0 || price <= 0 {
				continue
			}
			totalSize += size
			totalNotional += size * price
			totalFee += math.Abs(parseVenueFloat(fill.Fee, 0))
			if fill.Crossed {
				logger.Infof("  ⚠️ Entry fill for oid %d was reported as a taker fill", oid)
			}
		}
		if totalSize > 0 {
			return totalNotional / totalSize, totalFee
		}
	}
	return limitPrice, 0
}

// parseVenueFloat parses a venue-formatted number, falling back to def.
func parseVenueFloat(raw string, def float64) float64 {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return def
	}
	value, err := strconv.ParseFloat(trimmed, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return def
	}
	return value
}

func sideLabel(isLong bool) string {
	if isLong {
		return "LONG"
	}
	return "SHORT"
}
