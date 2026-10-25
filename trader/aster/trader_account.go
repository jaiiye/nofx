package aster

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"nofx/logger"
	"nofx/trader/types"
	"strconv"
	"time"
)

// GetAccountBalance gets the typed account state.
func (t *AsterTrader) GetAccountBalance() (types.AccountBalance, error) {
	params := make(map[string]interface{})
	body, err := t.request("GET", "/fapi/v3/balance", params)
	if err != nil {
		return types.AccountBalance{}, err
	}

	var balances []map[string]interface{}
	if err := json.Unmarshal(body, &balances); err != nil {
		return types.AccountBalance{}, err
	}

	// Find USDT balance
	availableBalance := 0.0
	crossUnPnl := 0.0
	crossWalletBalance := 0.0
	foundUSDT := false

	for _, bal := range balances {
		if asset, ok := bal["asset"].(string); ok && asset == "USDT" {
			foundUSDT = true

			// Parse Aster fields (reference: https://github.com/asterdex/api-docs)
			var parseErr error
			if avail, ok := bal["availableBalance"].(string); ok {
				if availableBalance, parseErr = types.ParseFloatField("availableBalance", avail); parseErr != nil {
					return types.AccountBalance{}, parseErr
				}
			}
			if unpnl, ok := bal["crossUnPnl"].(string); ok {
				if crossUnPnl, parseErr = types.ParseFloatField("crossUnPnl", unpnl); parseErr != nil {
					return types.AccountBalance{}, parseErr
				}
			}
			if cwb, ok := bal["crossWalletBalance"].(string); ok {
				if crossWalletBalance, parseErr = types.ParseFloatField("crossWalletBalance", cwb); parseErr != nil {
					return types.AccountBalance{}, parseErr
				}
			}
			break
		}
	}

	if !foundUSDT {
		logger.Infof("⚠️  USDT asset record not found!")
	}

	// Get positions to accumulate the real unrealized PnL. Aster's crossUnPnl
	// field is unreliable, so it is recomputed from the positions.
	positions, posErr := t.GetPositions()
	if posErr != nil {
		logger.Infof("⚠️  Failed to get position information: %v", posErr)
	}
	return assembleAsterBalance(availableBalance, crossWalletBalance, crossUnPnl, positions, posErr == nil), nil
}

// GetBalance renders the compatibility map. Prefer GetAccountBalance.
func (t *AsterTrader) GetBalance() (map[string]interface{}, error) {
	balance, err := t.GetAccountBalance()
	if err != nil {
		return nil, err
	}
	return balance.ToMap(), nil
}

// assembleAsterBalance maps the balance fields plus the open positions onto the
// canonical balance map.
//
// The previous implementation derived equity as availableBalance +
// Σ(notional/leverage), which silently drops the margin locked by resting
// orders and only approximates initial margin. crossWalletBalance is the venue's
// cash wallet and already includes order margin, so equity is that plus the
// position PnL.
func assembleAsterBalance(available, crossWallet, crossUnPnl float64, positions []map[string]interface{}, positionsOK bool) types.AccountBalance {
	if !positionsOK {
		// Degraded but identity-preserving: the venue's own PnL figure. Marked
		// incomplete so consumers can fail closed instead of sizing against it.
		return types.BalanceFromWallet("aster", crossWallet, crossUnPnl, available).Incomplete()
	}

	unrealized := 0.0
	incomplete := false
	for _, pos := range positions {
		quantity, okQty := pos["positionAmt"].(float64)
		unrealizedPnl, okPnL := pos["unRealizedProfit"].(float64)
		if !okQty || !okPnL {
			// A field rename must not panic the balance call.
			incomplete = true
			continue
		}
		if quantity == 0 {
			continue // ghost entry for an already-closed position
		}
		unrealized += unrealizedPnl
	}

	balance := types.BalanceFromWallet("aster", crossWallet, unrealized, available)
	if incomplete {
		balance = balance.Incomplete()
	}
	return balance
}

// GetMarketPrice Get market price
func (t *AsterTrader) GetMarketPrice(symbol string) (float64, error) {
	// Use ticker interface to get current price
	resp, err := t.client.Get(fmt.Sprintf("%s/fapi/v3/ticker/price?symbol=%s", t.baseURL, symbol))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, err
	}

	priceStr, ok := result["price"].(string)
	if !ok {
		return 0, errors.New("unable to get price")
	}

	return strconv.ParseFloat(priceStr, 64)
}

// GetClosedPnL gets recent closing trades from Aster
// Note: Aster does NOT have a position history API, only trade history.
// This returns individual closing trades for real-time position closure detection.
func (t *AsterTrader) GetClosedPnL(startTime time.Time, limit int) ([]types.ClosedPnLRecord, error) {
	trades, err := t.GetTrades(startTime, limit)
	if err != nil {
		return nil, err
	}

	// Filter only closing trades (realizedPnl != 0)
	var records []types.ClosedPnLRecord
	for _, trade := range trades {
		if trade.RealizedPnL == 0 {
			continue
		}

		// Determine side from PositionSide or trade direction
		side := "long"
		if trade.PositionSide == "SHORT" || trade.PositionSide == "short" {
			side = "short"
		} else if trade.PositionSide == "BOTH" || trade.PositionSide == "" {
			if trade.Side == "SELL" || trade.Side == "Sell" {
				side = "long"
			} else {
				side = "short"
			}
		}

		// Calculate entry price from PnL
		var entryPrice float64
		if trade.Quantity > 0 {
			if side == "long" {
				entryPrice = trade.Price - trade.RealizedPnL/trade.Quantity
			} else {
				entryPrice = trade.Price + trade.RealizedPnL/trade.Quantity
			}
		}

		records = append(records, types.ClosedPnLRecord{
			Symbol:      trade.Symbol,
			Side:        side,
			EntryPrice:  entryPrice,
			ExitPrice:   trade.Price,
			Quantity:    trade.Quantity,
			RealizedPnL: trade.RealizedPnL,
			Fee:         trade.Fee,
			ExitTime:    trade.Time,
			EntryTime:   trade.Time,
			OrderID:     trade.TradeID,
			ExchangeID:  trade.TradeID,
			CloseType:   "unknown",
		})
	}

	return records, nil
}

// AsterTradeRecord represents a trade from Aster API
type AsterTradeRecord struct {
	ID           int64  `json:"id"`
	Symbol       string `json:"symbol"`
	OrderID      int64  `json:"orderId"`
	Side         string `json:"side"`         // BUY or SELL
	PositionSide string `json:"positionSide"` // LONG or SHORT
	Price        string `json:"price"`
	Qty          string `json:"qty"`
	RealizedPnl  string `json:"realizedPnl"`
	Commission   string `json:"commission"`
	Time         int64  `json:"time"`
	Buyer        bool   `json:"buyer"`
	Maker        bool   `json:"maker"`
}

// GetTrades retrieves trade history from Aster
func (t *AsterTrader) GetTrades(startTime time.Time, limit int) ([]types.TradeRecord, error) {
	if limit <= 0 {
		limit = 500
	}

	// Build request params
	params := map[string]interface{}{
		"startTime": startTime.UnixMilli(),
		"limit":     limit,
	}

	// Use existing request method with signing
	body, err := t.request("GET", "/fapi/v3/userTrades", params)
	if err != nil {
		logger.Infof("⚠️  Aster userTrades API error: %v", err)
		return []types.TradeRecord{}, nil
	}

	var asterTrades []AsterTradeRecord
	if err := json.Unmarshal(body, &asterTrades); err != nil {
		logger.Infof("⚠️  Failed to parse Aster trades response: %v", err)
		return []types.TradeRecord{}, nil
	}

	// Convert to unified TradeRecord format
	var result []types.TradeRecord
	for _, at := range asterTrades {
		price, _ := strconv.ParseFloat(at.Price, 64)
		qty, _ := strconv.ParseFloat(at.Qty, 64)
		fee, _ := strconv.ParseFloat(at.Commission, 64)
		pnl, _ := strconv.ParseFloat(at.RealizedPnl, 64)

		trade := types.TradeRecord{
			TradeID:      strconv.FormatInt(at.ID, 10),
			Symbol:       at.Symbol,
			Side:         at.Side,
			PositionSide: at.PositionSide,
			Price:        price,
			Quantity:     qty,
			RealizedPnL:  pnl,
			Fee:          fee,
			Time:         time.UnixMilli(at.Time).UTC(),
		}
		result = append(result, trade)
	}

	return result, nil
}

// GetOrderBook gets the order book for a symbol
func (t *AsterTrader) GetOrderBook(symbol string, depth int) (bids, asks [][]float64, err error) {
	if depth <= 0 {
		depth = 20
	}

	// Aster uses public endpoint (no signature required)
	resp, err := t.client.Get(fmt.Sprintf("%s/fapi/v3/depth?symbol=%s&limit=%d", t.baseURL, symbol, depth))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch order book: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Bids [][]string `json:"bids"` // [[price, qty], ...]
		Asks [][]string `json:"asks"` // [[price, qty], ...]
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, nil, fmt.Errorf("failed to parse order book: %w", err)
	}

	// Convert string arrays to float64 arrays
	bids = make([][]float64, len(result.Bids))
	for i, bid := range result.Bids {
		if len(bid) >= 2 {
			price, _ := strconv.ParseFloat(bid[0], 64)
			qty, _ := strconv.ParseFloat(bid[1], 64)
			bids[i] = []float64{price, qty}
		}
	}

	asks = make([][]float64, len(result.Asks))
	for i, ask := range result.Asks {
		if len(ask) >= 2 {
			price, _ := strconv.ParseFloat(ask[0], 64)
			qty, _ := strconv.ParseFloat(ask[1], 64)
			asks[i] = []float64{price, qty}
		}
	}

	return bids, asks, nil
}
