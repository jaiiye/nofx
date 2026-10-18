package hyperliquid

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"nofx/logger"
	"nofx/trader/connector"
	"nofx/trader/types"
	"strconv"
	"strings"
	"time"

	"github.com/sonirico/go-hyperliquid"
)

// Aggressive limit prices simulate market orders: buy slightly above and sell
// slightly below the current price so IOC limit orders fill immediately while
// capping slippage at 1%.
const (
	aggressiveBuyPriceFactor  = 1.01
	aggressiveSellPriceFactor = 0.99
)

func (t *HyperliquidTrader) placeOrderWithBuilderFee(order hyperliquid.CreateOrderRequest) (hyperliquid.OrderStatus, error) {
	status, err := t.exchange.Order(t.ctx, order, builderInfo())
	if err != nil {
		return status, wrapBuilderFeeNotApproved(err)
	}
	return status, nil
}

// confirmedFillResult verifies the venue reported an actual IOC fill before the
// caller treats a position as opened/closed. Unfilled IOC orders used to be
// reported as FILLED with a placeholder order ID, so the system tracked a
// position that did not exist. Partial fills return the real filled size and
// average price; the venue cancels the remainder.
func confirmedFillResult(status *hyperliquid.OrderStatus, symbol string, requestedQty float64) (*connector.OrderResult, error) {
	if status.Error != nil {
		return nil, fmt.Errorf("order rejected: %s", *status.Error)
	}
	if status.Filled == nil {
		if status.Resting != nil {
			return nil, fmt.Errorf("IOC order did not fill (resting oid=%d, expired without matching)", status.Resting.Oid)
		}
		return nil, fmt.Errorf("IOC order did not fill (no fill in response)")
	}

	filledQty, err := strconv.ParseFloat(status.Filled.TotalSz, 64)
	if err != nil || filledQty <= 0 {
		return nil, fmt.Errorf("IOC order did not fill (filled size %q)", status.Filled.TotalSz)
	}
	avgPx, _ := strconv.ParseFloat(status.Filled.AvgPx, 64)

	if filledQty < requestedQty {
		logger.Infof("  ⚠ IOC partial fill for %s: requested %.8f, filled %.8f (remainder cancelled)", symbol, requestedQty, filledQty)
	}

	return &connector.OrderResult{
		OrderID:      strconv.FormatInt(int64(status.Filled.Oid), 10),
		Symbol:       symbol,
		Status:       "FILLED",
		AvgFillPrice: avgPx,
		FilledQty:    filledQty,
	}, nil
}

func isBuilderFeeNotApprovedError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "builder fee has not been approved")
}

func wrapBuilderFeeNotApproved(err error) error {
	if isBuilderFeeNotApprovedError(err) {
		return fmt.Errorf("Hyperliquid builder fee is not approved for NOFX; reconnect Hyperliquid wallet and complete trading authorization: %w", err)
	}
	return err
}

// OpenLong opens a long position (supports both crypto and xyz dex)
func (t *HyperliquidTrader) OpenLong(symbol string, quantity float64, leverage int) (*connector.OrderResult, error) {
	// First cancel all pending orders for this coin
	if err := t.CancelAllOrders(symbol); err != nil {
		logger.Infof("  ⚠ Failed to cancel old pending orders: %v", err)
	}

	// Hyperliquid symbol format
	coin := convertSymbolToHyperliquid(symbol)

	// Check if this is an xyz dex asset
	isXyz := strings.HasPrefix(coin, "xyz:")

	// Set leverage. xyz dex assets take the hand-signed HIP-3 updateLeverage
	// path; the SDK's name-based lookup predates it. HIP-3 leverage support is
	// not guaranteed, so a failure only logs and the order proceeds on the
	// dex's default leverage.
	if !isXyz {
		if err := t.SetLeverage(symbol, leverage); err != nil {
			return nil, err
		}
	} else if err := t.setXyzLeverage(coin, leverage, t.isCrossMargin); err != nil {
		logger.Infof("  ⚠️ xyz dex %s leverage not applied (%v) - proceeding on the dex's default leverage", coin, err)
	}

	// Get current price (for market order)
	price, err := t.GetMarketPrice(symbol)
	if err != nil {
		return nil, err
	}

	// Price needs to be processed to 5 significant figures
	aggressivePrice := t.roundPriceToSigfigs(price * aggressiveBuyPriceFactor)
	logger.Infof("  💰 Price precision handling: %.8f -> %.8f (5 significant figures)", price*aggressiveBuyPriceFactor, aggressivePrice)

	// Handle xyz dex assets differently
	var result *connector.OrderResult
	if isXyz {
		// xyz dex order
		if err := t.placeXyzOrder(coin, true, quantity, aggressivePrice, false); err != nil {
			return nil, fmt.Errorf("failed to open long position on xyz dex: %w", err)
		}
		result = &connector.OrderResult{Symbol: symbol, Status: "FILLED"}
	} else {
		// Standard crypto order
		roundedQuantity := t.roundToSzDecimals(coin, quantity)
		logger.Infof("  📏 Quantity precision handling: %.8f -> %.8f (szDecimals=%d)", quantity, roundedQuantity, t.getSzDecimals(coin))

		order := hyperliquid.CreateOrderRequest{
			Coin:  coin,
			IsBuy: true,
			Size:  roundedQuantity,
			Price: aggressivePrice,
			OrderType: hyperliquid.OrderType{
				Limit: &hyperliquid.LimitOrderType{
					Tif: hyperliquid.TifIoc,
				},
			},
			ReduceOnly: false,
		}

		status, err := t.placeOrderWithBuilderFee(order)
		if err != nil {
			return nil, fmt.Errorf("failed to open long position: %w", err)
		}
		if result, err = confirmedFillResult(&status, symbol, roundedQuantity); err != nil {
			return nil, fmt.Errorf("failed to open long position: %w", err)
		}
	}

	logger.Infof("✓ Long position opened successfully: %s quantity: %.4f", symbol, quantity)

	return result, nil
}

// OpenShort opens a short position (supports both crypto and xyz dex)
func (t *HyperliquidTrader) OpenShort(symbol string, quantity float64, leverage int) (*connector.OrderResult, error) {
	// First cancel all pending orders for this coin
	if err := t.CancelAllOrders(symbol); err != nil {
		logger.Infof("  ⚠ Failed to cancel old pending orders: %v", err)
	}

	// Hyperliquid symbol format
	coin := convertSymbolToHyperliquid(symbol)

	// Check if this is an xyz dex asset
	isXyz := strings.HasPrefix(coin, "xyz:")

	// Set leverage. xyz dex assets take the hand-signed HIP-3 updateLeverage
	// path; a failure only logs and the order proceeds on the dex default.
	if !isXyz {
		if err := t.SetLeverage(symbol, leverage); err != nil {
			return nil, err
		}
	} else if err := t.setXyzLeverage(coin, leverage, t.isCrossMargin); err != nil {
		logger.Infof("  ⚠️ xyz dex %s leverage not applied (%v) - proceeding on the dex's default leverage", coin, err)
	}

	// Get current price
	price, err := t.GetMarketPrice(symbol)
	if err != nil {
		return nil, err
	}

	// Price needs to be processed to 5 significant figures
	aggressivePrice := t.roundPriceToSigfigs(price * aggressiveSellPriceFactor)
	logger.Infof("  💰 Price precision handling: %.8f -> %.8f (5 significant figures)", price*aggressiveSellPriceFactor, aggressivePrice)

	// Handle xyz dex assets differently
	var result *connector.OrderResult
	if isXyz {
		// xyz dex order
		if err := t.placeXyzOrder(coin, false, quantity, aggressivePrice, false); err != nil {
			return nil, fmt.Errorf("failed to open short position on xyz dex: %w", err)
		}
		result = &connector.OrderResult{Symbol: symbol, Status: "FILLED"}
	} else {
		// Standard crypto order
		roundedQuantity := t.roundToSzDecimals(coin, quantity)
		logger.Infof("  📏 Quantity precision handling: %.8f -> %.8f (szDecimals=%d)", quantity, roundedQuantity, t.getSzDecimals(coin))

		order := hyperliquid.CreateOrderRequest{
			Coin:  coin,
			IsBuy: false,
			Size:  roundedQuantity,
			Price: aggressivePrice,
			OrderType: hyperliquid.OrderType{
				Limit: &hyperliquid.LimitOrderType{
					Tif: hyperliquid.TifIoc,
				},
			},
			ReduceOnly: false,
		}

		status, err := t.placeOrderWithBuilderFee(order)
		if err != nil {
			return nil, fmt.Errorf("failed to open short position: %w", err)
		}
		if result, err = confirmedFillResult(&status, symbol, roundedQuantity); err != nil {
			return nil, fmt.Errorf("failed to open short position: %w", err)
		}
	}

	logger.Infof("✓ Short position opened successfully: %s quantity: %.4f", symbol, quantity)

	return result, nil
}

// CloseLong closes a long position (supports both crypto and xyz dex)
func (t *HyperliquidTrader) CloseLong(symbol string, quantity float64) (*connector.OrderResult, error) {
	// Hyperliquid symbol format
	coin := convertSymbolToHyperliquid(symbol)
	isXyz := strings.HasPrefix(coin, "xyz:")

	// If quantity is 0, get current position quantity
	if quantity == 0 {
		positions, err := t.GetPositions()
		if err != nil {
			return nil, err
		}

		// For xyz dex, also check xyz: prefixed symbols
		searchSymbol := symbol
		if isXyz {
			searchSymbol = coin // Use xyz:SYMBOL format for comparison
		}

		for _, pos := range positions {
			posSymbol := pos["symbol"].(string)
			if (posSymbol == symbol || posSymbol == searchSymbol) && pos["side"] == "long" {
				quantity = pos["positionAmt"].(float64)
				break
			}
		}

		if quantity == 0 {
			return nil, fmt.Errorf("no long position found for %s", symbol)
		}
	}

	// Get current price
	price, err := t.GetMarketPrice(symbol)
	if err != nil {
		return nil, err
	}

	// Price needs to be processed to 5 significant figures
	aggressivePrice := t.roundPriceToSigfigs(price * aggressiveSellPriceFactor)
	logger.Infof("  💰 Price precision handling: %.8f -> %.8f (5 significant figures)", price*aggressiveSellPriceFactor, aggressivePrice)

	// Handle xyz dex assets differently
	var result *connector.OrderResult
	if isXyz {
		// xyz dex close order
		if err := t.placeXyzOrder(coin, false, quantity, aggressivePrice, true); err != nil {
			return nil, fmt.Errorf("failed to close long position on xyz dex: %w", err)
		}
		result = &connector.OrderResult{Symbol: symbol, Status: "FILLED"}
	} else {
		// Standard crypto close order
		roundedQuantity := t.roundToSzDecimals(coin, quantity)
		logger.Infof("  📏 Quantity precision handling: %.8f -> %.8f (szDecimals=%d)", quantity, roundedQuantity, t.getSzDecimals(coin))

		order := hyperliquid.CreateOrderRequest{
			Coin:  coin,
			IsBuy: false,
			Size:  roundedQuantity,
			Price: aggressivePrice,
			OrderType: hyperliquid.OrderType{
				Limit: &hyperliquid.LimitOrderType{
					Tif: hyperliquid.TifIoc,
				},
			},
			ReduceOnly: true,
		}

		status, err := t.placeOrderWithBuilderFee(order)
		if err != nil {
			return nil, fmt.Errorf("failed to close long position: %w", err)
		}
		if result, err = confirmedFillResult(&status, symbol, roundedQuantity); err != nil {
			return nil, fmt.Errorf("failed to close long position: %w", err)
		}
	}

	logger.Infof("✓ Long position closed successfully: %s quantity: %.4f", symbol, quantity)

	// Cancel all pending orders for this coin after closing position
	if err := t.CancelAllOrders(symbol); err != nil {
		logger.Infof("  ⚠ Failed to cancel pending orders: %v", err)
	}

	return result, nil
}

// CloseShort closes a short position (supports both crypto and xyz dex)
func (t *HyperliquidTrader) CloseShort(symbol string, quantity float64) (*connector.OrderResult, error) {
	// Hyperliquid symbol format
	coin := convertSymbolToHyperliquid(symbol)
	isXyz := strings.HasPrefix(coin, "xyz:")

	// If quantity is 0, get current position quantity
	if quantity == 0 {
		positions, err := t.GetPositions()
		if err != nil {
			return nil, err
		}

		// For xyz dex, also check xyz: prefixed symbols
		searchSymbol := symbol
		if isXyz {
			searchSymbol = coin
		}

		for _, pos := range positions {
			posSymbol := pos["symbol"].(string)
			if (posSymbol == symbol || posSymbol == searchSymbol) && pos["side"] == "short" {
				quantity = pos["positionAmt"].(float64)
				break
			}
		}

		if quantity == 0 {
			return nil, fmt.Errorf("no short position found for %s", symbol)
		}
	}

	// Get current price
	price, err := t.GetMarketPrice(symbol)
	if err != nil {
		return nil, err
	}

	// Price needs to be processed to 5 significant figures
	aggressivePrice := t.roundPriceToSigfigs(price * aggressiveBuyPriceFactor)
	logger.Infof("  💰 Price precision handling: %.8f -> %.8f (5 significant figures)", price*aggressiveBuyPriceFactor, aggressivePrice)

	// Handle xyz dex assets differently
	var result *connector.OrderResult
	if isXyz {
		// xyz dex close order
		if err := t.placeXyzOrder(coin, true, quantity, aggressivePrice, true); err != nil {
			return nil, fmt.Errorf("failed to close short position on xyz dex: %w", err)
		}
		result = &connector.OrderResult{Symbol: symbol, Status: "FILLED"}
	} else {
		// Standard crypto close order
		roundedQuantity := t.roundToSzDecimals(coin, quantity)
		logger.Infof("  📏 Quantity precision handling: %.8f -> %.8f (szDecimals=%d)", quantity, roundedQuantity, t.getSzDecimals(coin))

		order := hyperliquid.CreateOrderRequest{
			Coin:  coin,
			IsBuy: true,
			Size:  roundedQuantity,
			Price: aggressivePrice,
			OrderType: hyperliquid.OrderType{
				Limit: &hyperliquid.LimitOrderType{
					Tif: hyperliquid.TifIoc,
				},
			},
			ReduceOnly: true,
		}

		status, err := t.placeOrderWithBuilderFee(order)
		if err != nil {
			return nil, fmt.Errorf("failed to close short position: %w", err)
		}
		if result, err = confirmedFillResult(&status, symbol, roundedQuantity); err != nil {
			return nil, fmt.Errorf("failed to close short position: %w", err)
		}
	}

	logger.Infof("✓ Short position closed successfully: %s quantity: %.4f", symbol, quantity)

	// Cancel all pending orders for this coin after closing position
	if err := t.CancelAllOrders(symbol); err != nil {
		logger.Infof("  ⚠ Failed to cancel pending orders: %v", err)
	}

	return result, nil
}

// CancelStopLossOrders only cancels stop loss orders (Hyperliquid cannot distinguish stop loss and take profit, cancel all)
func (t *HyperliquidTrader) CancelStopLossOrders(symbol string) error {
	// Hyperliquid SDK's OpenOrder structure does not expose trigger field
	// Cannot distinguish stop loss and take profit orders, so cancel all pending orders for this coin
	logger.Infof("  ⚠️ Hyperliquid cannot distinguish stop loss/take profit orders, will cancel all pending orders")
	return t.CancelStopOrders(symbol)
}

// CancelTakeProfitOrders only cancels take profit orders (Hyperliquid cannot distinguish stop loss and take profit, cancel all)
func (t *HyperliquidTrader) CancelTakeProfitOrders(symbol string) error {
	// Hyperliquid SDK's OpenOrder structure does not expose trigger field
	// Cannot distinguish stop loss and take profit orders, so cancel all pending orders for this coin
	logger.Infof("  ⚠️ Hyperliquid cannot distinguish stop loss/take profit orders, will cancel all pending orders")
	return t.CancelStopOrders(symbol)
}

// CancelAllOrders cancels all pending orders for this coin
func (t *HyperliquidTrader) CancelAllOrders(symbol string) error {
	coin := convertSymbolToHyperliquid(symbol)

	// Check if this is an xyz dex asset
	isXyz := strings.HasPrefix(coin, "xyz:")

	if isXyz {
		// xyz dex orders - use direct API call
		return t.cancelXyzOrders(coin)
	}

	// Standard crypto orders
	openOrders, err := t.exchange.Info().OpenOrders(t.ctx, t.walletAddr)
	if err != nil {
		return fmt.Errorf("failed to get pending orders: %w", err)
	}

	// Cancel all pending orders for this coin
	for _, order := range openOrders {
		if order.Coin == coin {
			_, err := t.exchange.Cancel(t.ctx, coin, order.Oid)
			if err != nil {
				logger.Infof("  ⚠ Failed to cancel order (oid=%d): %v", order.Oid, err)
			}
		}
	}

	logger.Infof("  ✓ Cancelled all pending orders for %s", symbol)
	return nil
}

// CancelStopOrders cancels take profit/stop loss orders for this coin (used to adjust TP/SL positions)
func (t *HyperliquidTrader) CancelStopOrders(symbol string) error {
	coin := convertSymbolToHyperliquid(symbol)

	// Check if this is an xyz dex asset
	isXyz := strings.HasPrefix(coin, "xyz:")

	if isXyz {
		// xyz dex orders - use direct API call
		return t.cancelXyzOrders(coin)
	}

	// Get all pending orders for standard crypto
	openOrders, err := t.exchange.Info().OpenOrders(t.ctx, t.walletAddr)
	if err != nil {
		return fmt.Errorf("failed to get pending orders: %w", err)
	}

	// Note: Hyperliquid SDK's OpenOrder structure does not expose trigger field
	// Therefore temporarily cancel all pending orders for this coin (including TP/SL orders)
	// This is safe because all old orders should be cleaned up before setting new TP/SL
	canceledCount := 0
	for _, order := range openOrders {
		if order.Coin == coin {
			_, err := t.exchange.Cancel(t.ctx, coin, order.Oid)
			if err != nil {
				logger.Infof("  ⚠ Failed to cancel order (oid=%d): %v", order.Oid, err)
				continue
			}
			canceledCount++
		}
	}

	if canceledCount == 0 {
		logger.Infof("  ℹ No pending orders to cancel for %s", symbol)
	} else {
		logger.Infof("  ✓ Cancelled %d pending orders for %s (including TP/SL orders)", canceledCount, symbol)
	}

	return nil
}

// cancelXyzOrders cancels all pending orders for xyz dex assets (stocks, forex, commodities)
func (t *HyperliquidTrader) cancelXyzOrders(coin string) error {
	// Query xyz dex open orders
	reqBody := map[string]interface{}{
		"type": "openOrders",
		"user": t.walletAddr,
		"dex":  "xyz",
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	apiURL := "https://api.hyperliquid.xyz/info"

	req, err := http.NewRequestWithContext(t.ctx, "POST", apiURL, bytes.NewBuffer(jsonBody))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("xyz dex openOrders API error (status %d): %s", resp.StatusCode, string(body))
	}

	// Parse open orders
	var openOrders []struct {
		Coin string `json:"coin"`
		Oid  int64  `json:"oid"`
	}
	if err := json.Unmarshal(body, &openOrders); err != nil {
		return fmt.Errorf("failed to parse open orders: %w", err)
	}

	// Filter orders for this coin and cancel them
	canceledCount := 0
	for _, order := range openOrders {
		if order.Coin == coin {
			if err := t.cancelXyzOrder(order.Oid); err != nil {
				logger.Infof("  ⚠ Failed to cancel xyz dex order (oid=%d): %v", order.Oid, err)
				continue
			}
			canceledCount++
		}
	}

	if canceledCount == 0 {
		logger.Infof("  ℹ No pending xyz dex orders to cancel for %s", coin)
	} else {
		logger.Infof("  ✓ Cancelled %d xyz dex orders for %s", canceledCount, coin)
	}

	return nil
}

// cancelXyzOrder cancels a single xyz dex order by oid
func (t *HyperliquidTrader) cancelXyzOrder(oid int64) error {
	// Get asset index for this order (we need it for cancel action)
	// For cancel, we construct a cancel action with the oid

	action := map[string]interface{}{
		"type": "cancel",
		"cancels": []map[string]interface{}{
			{
				"a": oid, // asset index not needed for cancel by oid in xyz dex
				"o": oid,
			},
		},
	}

	// Sign the action
	nonce := time.Now().UnixMilli()
	isMainnet := !t.isTestnet
	vaultAddress := ""

	sig, err := hyperliquid.SignL1Action(t.privateKey, action, vaultAddress, nonce, nil, isMainnet)
	if err != nil {
		return fmt.Errorf("failed to sign cancel action: %w", err)
	}

	payload := map[string]any{
		"action":    action,
		"nonce":     nonce,
		"signature": sig,
	}

	apiURL := hyperliquid.MainnetAPIURL
	if t.isTestnet {
		apiURL = hyperliquid.TestnetAPIURL
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(t.ctx, http.MethodPost, apiURL+"/exchange", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	// Check response
	var result struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if result.Status != "ok" {
		return fmt.Errorf("cancel failed: %s", string(body))
	}

	return nil
}

// floatToWireStr converts a float to wire format string (8 decimal places, trimmed zeros)
// This matches the SDK's floatToWire function
func floatToWireStr(x float64) string {
	// Format to 8 decimal places
	result := fmt.Sprintf("%.8f", x)
	// Remove trailing zeros
	result = strings.TrimRight(result, "0")
	// Remove trailing decimal point if no decimals left
	result = strings.TrimRight(result, ".")
	return result
}

// placeXyzOrder places an order on the xyz dex (stocks, forex, commodities)
// Note: xyz dex orders use builder-deployed perpetuals and require different handling
// xyz dex asset indices start from 10000 (10000 + meta_index)
// This implementation bypasses the SDK's NameToAsset lookup and directly constructs the order
// placeXyzOrder submits an aggressive IOC order on the HIP-3 dex.
func (t *HyperliquidTrader) placeXyzOrder(coin string, isBuy bool, size float64, price float64, reduceOnly bool) error {
	return t.placeXyzOrderWithTif(coin, isBuy, size, price, reduceOnly, hyperliquid.TifIoc, "")
}

// placeXyzOrderWithTif is placeXyzOrder with an explicit time-in-force and
// client order id, so the same hand-signed path can also rest a post-only (ALO)
// entry. HIP-3 support for ALO is not documented; a rejection is surfaced to the
// caller, which falls back to the aggressive order.
func (t *HyperliquidTrader) placeXyzOrderWithTif(coin string, isBuy bool, size float64, price float64, reduceOnly bool, tif hyperliquid.Tif, cloid string) error {
	// Fetch xyz meta if not cached
	t.xyzMetaMutex.RLock()
	hasMeta := t.xyzMeta != nil
	t.xyzMetaMutex.RUnlock()

	if !hasMeta {
		if err := t.fetchXyzMeta(); err != nil {
			return fmt.Errorf("failed to fetch xyz meta: %w", err)
		}
	}

	// Get asset index from xyz meta (returns 0-based index)
	assetIndex, err := t.xyzAssetIndexFor(coin)
	if err != nil {
		return err
	}

	// Pre-flight (opens only): the xyz dex (HIP-3) settles against its own
	// USDC pool, so a funded main perp account can still be rejected here.
	// Catch the empty-pool case with an actionable message instead of sending
	// a doomed order — and never block a reduce/close, which frees margin.
	if !reduceOnly {
		if xyzBal, balErr := t.getXYZDexBalance(); balErr == nil && xyzBal.Withdrawable <= 0 {
			return fmt.Errorf("xyz dex has no free margin (available %.2f USDC, equity %.2f USDC): the xyz perp dex keeps its own USDC pool, separate from the main perp account — transfer USDC into the xyz dex before opening %s",
				xyzBal.Withdrawable, xyzBal.AccountValue, coin)
		}
	}

	// Round size to correct precision
	szDecimals := t.getXyzSzDecimals(coin)
	multiplier := 1.0
	for i := 0; i < szDecimals; i++ {
		multiplier *= 10.0
	}
	roundedSize := float64(int(size*multiplier+0.5)) / multiplier

	// Round price to 5 significant figures
	roundedPrice := t.roundPriceToSigfigs(price)

	logger.Infof("📝 Placing xyz dex order (direct): %s %s size=%.4f price=%.4f assetIndex=%d reduceOnly=%v",
		map[bool]string{true: "BUY", false: "SELL"}[isBuy],
		coin, roundedSize, roundedPrice, assetIndex, reduceOnly)

	// Construct OrderWire directly with correct asset index (bypassing SDK's NameToAsset)
	orderWire := hyperliquid.OrderWire{
		Asset:      assetIndex,
		IsBuy:      isBuy,
		LimitPx:    floatToWireStr(roundedPrice),
		Size:       floatToWireStr(roundedSize),
		ReduceOnly: reduceOnly,
		OrderType: hyperliquid.OrderWireType{
			Limit: &hyperliquid.OrderWireTypeLimit{
				Tif: tif,
			},
		},
	}
	if cloid != "" {
		orderWire.Cloid = &cloid
	}

	// Create OrderAction with NOFX builder fee. Trader startup requires a persisted
	// builder approval flag before this live path can run.
	action := hyperliquid.OrderAction{
		Type:     "order",
		Orders:   []hyperliquid.OrderWire{orderWire},
		Grouping: "na",
		Builder:  builderInfo(),
	}

	body, err := t.postXyzExchangeAction(action)
	if err != nil {
		return wrapXyzInsufficientMargin(err, coin)
	}

	// Parse response
	var result struct {
		Status   string `json:"status"`
		Response struct {
			Type string `json:"type"`
			Data struct {
				Statuses []struct {
					Resting *struct {
						Oid int64 `json:"oid"`
					} `json:"resting,omitempty"`
					Filled *struct {
						TotalSz string `json:"totalSz"`
						AvgPx   string `json:"avgPx"`
						Oid     int    `json:"oid"`
					} `json:"filled,omitempty"`
					Error *string `json:"error,omitempty"`
				} `json:"statuses"`
			} `json:"data"`
		} `json:"response"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		// Try to parse as error response
		logger.Infof("⚠️  Failed to parse response as success, raw body: %s", string(body))
		return wrapXyzInsufficientMargin(fmt.Errorf("xyz dex order failed, body=%s", string(body)), coin)
	}

	// Check for errors in response
	if result.Status != "ok" {
		return wrapXyzInsufficientMargin(fmt.Errorf("xyz dex order failed: status=%s, body=%s", result.Status, string(body)), coin)
	}

	// Check order statuses. IOC orders must report a fill — a resting or
	// empty response means nothing traded, and reporting success would leave
	// the system tracking a position that does not exist.
	if len(result.Response.Data.Statuses) == 0 {
		return fmt.Errorf("xyz dex order returned no order status (body=%s)", string(body))
	}
	status := result.Response.Data.Statuses[0]
	if status.Error != nil {
		return wrapXyzInsufficientMargin(
			wrapBuilderFeeNotApproved(fmt.Errorf("xyz dex order error (coin=%s, assetIndex=%d, size=%.4f, price=%.4f): %s", coin, assetIndex, roundedSize, roundedPrice, *status.Error)),
			coin)
	}
	if status.Filled != nil {
		logger.Infof("✅ xyz dex order filled: totalSz=%s avgPx=%s oid=%d",
			status.Filled.TotalSz, status.Filled.AvgPx, status.Filled.Oid)
	} else {
		return fmt.Errorf("xyz dex IOC order did not fill for %s (no fill in response)", coin)
	}

	logger.Infof("✅ xyz dex order placed successfully: %s (response: %s)", coin, string(body))
	return nil
}

// xyzAssetIndexFor resolves the HIP-3 encoded asset index for an xyz dex
// coin, fetching the dex meta on first use.
//
// Formula: 100000 + perp_dex_index * 10000 + index_in_meta. The xyz dex is at
// perp_dex_index = 1 (verified from the perpDexs API), so xyz assets live at
// 110000 + metaIndex (e.g. SILVER -> 110026).
func (t *HyperliquidTrader) xyzAssetIndexFor(coin string) (int, error) {
	t.xyzMetaMutex.RLock()
	hasMeta := t.xyzMeta != nil
	t.xyzMetaMutex.RUnlock()

	if !hasMeta {
		if err := t.fetchXyzMeta(); err != nil {
			return 0, fmt.Errorf("failed to fetch xyz meta: %w", err)
		}
	}

	metaIndex := t.getXyzAssetIndex(coin)
	if metaIndex < 0 {
		return 0, fmt.Errorf("xyz asset %s not found in meta", coin)
	}
	const xyzPerpDexIndex = 1
	return 100000 + xyzPerpDexIndex*10000 + metaIndex, nil
}

// postXyzExchangeAction signs an action with the L1 signer and POSTs it to the
// venue's /exchange endpoint, returning the raw response body. It is the
// shared transport for the hand-rolled xyz dex actions (orders, leverage),
// which bypass the SDK because its name->asset lookup predates HIP-3.
func (t *HyperliquidTrader) postXyzExchangeAction(action any) ([]byte, error) {
	nonce := time.Now().UnixMilli()
	isMainnet := !t.isTestnet
	vaultAddress := "" // No vault for personal account

	sig, err := hyperliquid.SignL1Action(t.privateKey, action, vaultAddress, nonce, nil, isMainnet)
	if err != nil {
		return nil, fmt.Errorf("failed to sign action: %w", err)
	}

	jsonData, err := json.Marshal(map[string]any{
		"action":    action,
		"nonce":     nonce,
		"signature": sig,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	apiURL := hyperliquid.MainnetAPIURL
	if t.isTestnet {
		apiURL = hyperliquid.TestnetAPIURL
	}

	req, err := http.NewRequestWithContext(t.ctx, http.MethodPost, apiURL+"/exchange", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("exchange endpoint returned HTTP %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}

// wrapXyzInsufficientMargin turns the venue's raw "insufficient margin"
// rejection into guidance: on the xyz dex that message almost always means the
// dex's own USDC pool needs a deposit, not that the account is empty.
func wrapXyzInsufficientMargin(err error, coin string) error {
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "insufficient margin") {
		return fmt.Errorf("%w — the xyz dex (HIP-3) keeps its own USDC margin pool, separate from the main perp account; transfer USDC into the xyz dex or reduce the order size for %s", err, coin)
	}
	return err
}

// setXyzLeverage attempts to set leverage for an xyz dex asset via a
// hand-signed updateLeverage action carrying the HIP-3 encoded asset index
// (the SDK's name-based lookup predates HIP-3).
//
// HIP-3 leverage updates are not documented as supported for every dex, so
// callers treat a failure as non-fatal: the order proceeds on the dex's
// default leverage. The log line tells us whether the venue accepts it.
func (t *HyperliquidTrader) setXyzLeverage(coin string, leverage int, isCross bool) error {
	assetIndex, err := t.xyzAssetIndexFor(coin)
	if err != nil {
		return err
	}
	action := hyperliquid.UpdateLeverageAction{
		Type:     "updateLeverage",
		Asset:    assetIndex,
		IsCross:  isCross,
		Leverage: leverage,
	}
	if _, err := t.postXyzExchangeAction(action); err != nil {
		return fmt.Errorf("xyz dex updateLeverage failed: %w", err)
	}
	logger.Infof("  ✓ xyz dex %s leverage set to %dx (assetIndex=%d)", coin, leverage, assetIndex)
	return nil
}

// placeXyzTriggerOrder places a trigger order (stop loss / take profit) on the xyz dex
// tpsl: "sl" for stop loss, "tp" for take profit
func (t *HyperliquidTrader) placeXyzTriggerOrder(coin string, isBuy bool, size float64, triggerPrice float64, tpsl string) error {
	// Fetch xyz meta if not cached
	t.xyzMetaMutex.RLock()
	hasMeta := t.xyzMeta != nil
	t.xyzMetaMutex.RUnlock()

	if !hasMeta {
		if err := t.fetchXyzMeta(); err != nil {
			return fmt.Errorf("failed to fetch xyz meta: %w", err)
		}
	}

	// Get asset index from xyz meta (returns 0-based index)
	metaIndex := t.getXyzAssetIndex(coin)
	if metaIndex < 0 {
		return fmt.Errorf("xyz asset %s not found in meta", coin)
	}

	// HIP-3 perp dex asset index formula: 100000 + perp_dex_index * 10000 + index_in_meta
	// xyz dex is at perp_dex_index = 1
	const xyzPerpDexIndex = 1
	assetIndex := 100000 + xyzPerpDexIndex*10000 + metaIndex

	// Round size to correct precision
	szDecimals := t.getXyzSzDecimals(coin)
	multiplier := 1.0
	for i := 0; i < szDecimals; i++ {
		multiplier *= 10.0
	}
	roundedSize := float64(int(size*multiplier+0.5)) / multiplier

	// Round price to 5 significant figures
	roundedPrice := t.roundPriceToSigfigs(triggerPrice)

	logger.Infof("📝 Placing xyz dex %s order: %s %s size=%.4f triggerPrice=%.4f assetIndex=%d",
		tpsl,
		map[bool]string{true: "BUY", false: "SELL"}[isBuy],
		coin, roundedSize, roundedPrice, assetIndex)

	// Construct OrderWire with trigger type for stop loss / take profit
	orderWire := hyperliquid.OrderWire{
		Asset:      assetIndex,
		IsBuy:      isBuy,
		LimitPx:    floatToWireStr(roundedPrice),
		Size:       floatToWireStr(roundedSize),
		ReduceOnly: true, // TP/SL orders are always reduce-only
		OrderType: hyperliquid.OrderWireType{
			Trigger: &hyperliquid.OrderWireTypeTrigger{
				TriggerPx: floatToWireStr(roundedPrice),
				IsMarket:  true,
				Tpsl:      hyperliquid.Tpsl(tpsl), // "sl" or "tp" - convert string to Tpsl type
			},
		},
	}

	// Create OrderAction with NOFX builder fee. Trader startup requires a persisted
	// builder approval flag before this live path can run.
	action := hyperliquid.OrderAction{
		Type:     "order",
		Orders:   []hyperliquid.OrderWire{orderWire},
		Grouping: "na",
		Builder:  builderInfo(),
	}

	// Sign the action
	nonce := time.Now().UnixMilli()
	isMainnet := !t.isTestnet
	vaultAddress := ""

	sig, err := hyperliquid.SignL1Action(t.privateKey, action, vaultAddress, nonce, nil, isMainnet)
	if err != nil {
		return fmt.Errorf("failed to sign xyz dex trigger order: %w", err)
	}

	// Construct payload for /exchange endpoint
	payload := map[string]any{
		"action":    action,
		"nonce":     nonce,
		"signature": sig,
	}

	// Determine API URL
	apiURL := hyperliquid.MainnetAPIURL
	if t.isTestnet {
		apiURL = hyperliquid.TestnetAPIURL
	}

	// POST to /exchange
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	logger.Infof("📤 Sending xyz dex %s order to %s/exchange", tpsl, apiURL)

	req, err := http.NewRequestWithContext(t.ctx, http.MethodPost, apiURL+"/exchange", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	// Parse response
	var result struct {
		Status   string `json:"status"`
		Response struct {
			Type string `json:"type"`
			Data struct {
				Statuses []struct {
					Resting *struct {
						Oid int64 `json:"oid"`
					} `json:"resting,omitempty"`
					Error *string `json:"error,omitempty"`
				} `json:"statuses"`
			} `json:"data"`
		} `json:"response"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		logger.Infof("⚠️  Failed to parse response, raw body: %s", string(body))
		return fmt.Errorf("xyz dex %s order failed, status=%d, body=%s", tpsl, resp.StatusCode, string(body))
	}

	// Check for errors in response
	if result.Status != "ok" {
		return fmt.Errorf("xyz dex %s order failed: status=%s, body=%s", tpsl, result.Status, string(body))
	}

	// Check order statuses
	if len(result.Response.Data.Statuses) > 0 {
		status := result.Response.Data.Statuses[0]
		if status.Error != nil {
			return wrapBuilderFeeNotApproved(fmt.Errorf("xyz dex %s order error: %s", tpsl, *status.Error))
		}
		if status.Resting != nil {
			logger.Infof("✅ xyz dex %s order placed: oid=%d", tpsl, status.Resting.Oid)
		}
	}

	logger.Infof("✅ xyz dex %s order placed successfully: %s", tpsl, coin)
	return nil
}

// SetStopLoss sets stop loss order
func (t *HyperliquidTrader) SetStopLoss(symbol string, positionSide string, quantity, stopPrice float64) error {
	coin := convertSymbolToHyperliquid(symbol)

	isBuy := positionSide == "SHORT" // Short position stop loss = buy, long position stop loss = sell

	// Price needs to be processed to 5 significant figures
	roundedStopPrice := t.roundPriceToSigfigs(stopPrice)

	// Check if this is an xyz dex asset (stocks, forex, commodities)
	isXyz := strings.HasPrefix(coin, "xyz:")

	if isXyz {
		// xyz dex stop loss order - use direct API call similar to placeXyzOrder
		if err := t.placeXyzTriggerOrder(coin, isBuy, quantity, roundedStopPrice, "sl"); err != nil {
			return fmt.Errorf("failed to set xyz dex stop loss: %w", err)
		}
	} else {
		// Standard crypto stop loss order
		// Round quantity according to coin precision requirements
		roundedQuantity := t.roundToSzDecimals(coin, quantity)

		// Create stop loss order (Trigger Order)
		order := hyperliquid.CreateOrderRequest{
			Coin:  coin,
			IsBuy: isBuy,
			Size:  roundedQuantity,  // Use rounded quantity
			Price: roundedStopPrice, // Use processed price
			OrderType: hyperliquid.OrderType{
				Trigger: &hyperliquid.TriggerOrderType{
					TriggerPx: roundedStopPrice,
					IsMarket:  true,
					Tpsl:      "sl", // stop loss
				},
			},
			ReduceOnly: true,
		}

		_, err := t.placeOrderWithBuilderFee(order)
		if err != nil {
			return fmt.Errorf("failed to set stop loss: %w", err)
		}
	}

	logger.Infof("  Stop loss price set: %.4f", roundedStopPrice)
	return nil
}

// SetTakeProfit sets take profit order
func (t *HyperliquidTrader) SetTakeProfit(symbol string, positionSide string, quantity, takeProfitPrice float64) error {
	coin := convertSymbolToHyperliquid(symbol)

	isBuy := positionSide == "SHORT" // Short position take profit = buy, long position take profit = sell

	// Price needs to be processed to 5 significant figures
	roundedTakeProfitPrice := t.roundPriceToSigfigs(takeProfitPrice)

	// Check if this is an xyz dex asset (stocks, forex, commodities)
	isXyz := strings.HasPrefix(coin, "xyz:")

	if isXyz {
		// xyz dex take profit order - use direct API call similar to placeXyzOrder
		if err := t.placeXyzTriggerOrder(coin, isBuy, quantity, roundedTakeProfitPrice, "tp"); err != nil {
			return fmt.Errorf("failed to set xyz dex take profit: %w", err)
		}
	} else {
		// Standard crypto take profit order
		// Round quantity according to coin precision requirements
		roundedQuantity := t.roundToSzDecimals(coin, quantity)

		// Create take profit order (Trigger Order)
		order := hyperliquid.CreateOrderRequest{
			Coin:  coin,
			IsBuy: isBuy,
			Size:  roundedQuantity,        // Use rounded quantity
			Price: roundedTakeProfitPrice, // Use processed price
			OrderType: hyperliquid.OrderType{
				Trigger: &hyperliquid.TriggerOrderType{
					TriggerPx: roundedTakeProfitPrice,
					IsMarket:  true,
					Tpsl:      "tp", // take profit
				},
			},
			ReduceOnly: true,
		}

		_, err := t.placeOrderWithBuilderFee(order)
		if err != nil {
			return fmt.Errorf("failed to set take profit: %w", err)
		}
	}

	logger.Infof("  Take profit price set: %.4f", roundedTakeProfitPrice)
	return nil
}

// PlaceLimitOrder places a limit order for grid trading
// Implements GridTrader interface
func (t *HyperliquidTrader) PlaceLimitOrder(req *types.LimitOrderRequest) (*types.LimitOrderResult, error) {
	coin := convertSymbolToHyperliquid(req.Symbol)

	// Set leverage if specified and not xyz dex
	isXyz := strings.HasPrefix(coin, "xyz:")
	if req.Leverage > 0 && !isXyz {
		if err := t.SetLeverage(req.Symbol, req.Leverage); err != nil {
			logger.Warnf("[Hyperliquid] Failed to set leverage: %v", err)
		}
	}

	// Round quantity to allowed decimals
	roundedQuantity := t.roundToSzDecimals(coin, req.Quantity)

	// Round price to 5 significant figures
	roundedPrice := t.roundPriceToSigfigs(req.Price)

	// Determine if buy or sell
	isBuy := req.Side == "BUY"

	logger.Infof("[Hyperliquid] PlaceLimitOrder: %s %s @ %.4f, qty=%.4f", coin, req.Side, roundedPrice, roundedQuantity)

	order := hyperliquid.CreateOrderRequest{
		Coin:  coin,
		IsBuy: isBuy,
		Size:  roundedQuantity,
		Price: roundedPrice,
		OrderType: hyperliquid.OrderType{
			Limit: &hyperliquid.LimitOrderType{
				Tif: hyperliquid.TifGtc, // Good Till Cancel for grid orders
			},
		},
		ReduceOnly: req.ReduceOnly,
	}

	status, err := t.placeOrderWithBuilderFee(order)
	if err != nil {
		return nil, fmt.Errorf("failed to place limit order: %w", err)
	}

	// Prefer the venue-assigned oid from the response; fall back to a
	// timestamp placeholder only when the response carries no id (grid
	// trading tracks orders by price level as well).
	orderID := fmt.Sprintf("%d", time.Now().UnixNano())
	if status.Resting != nil {
		orderID = strconv.FormatInt(status.Resting.Oid, 10)
	} else if status.Filled != nil {
		orderID = strconv.FormatInt(int64(status.Filled.Oid), 10)
	}

	logger.Infof("✓ [Hyperliquid] Limit order placed: %s %s @ %.4f",
		coin, req.Side, roundedPrice)

	return &types.LimitOrderResult{
		OrderID:      orderID,
		ClientID:     req.ClientID,
		Symbol:       req.Symbol,
		Side:         req.Side,
		PositionSide: req.PositionSide,
		Price:        roundedPrice,
		Quantity:     roundedQuantity,
		Status:       "NEW",
	}, nil
}

// CancelOrder cancels a specific order by ID
// Implements GridTrader interface
func (t *HyperliquidTrader) CancelOrder(symbol, orderID string) error {
	coin := convertSymbolToHyperliquid(symbol)

	// Parse order ID
	oid, err := strconv.ParseInt(orderID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid order ID: %w", err)
	}

	_, err = t.exchange.Cancel(t.ctx, coin, oid)
	if err != nil {
		return fmt.Errorf("failed to cancel order: %w", err)
	}

	logger.Infof("✓ [Hyperliquid] Order cancelled: %s %s", symbol, orderID)
	return nil
}
