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
)

// GetAccountBalance gets the typed account state.
func (t *HyperliquidTrader) GetAccountBalance() (types.AccountBalance, error) {
	logger.Infof("🔄 Calling Hyperliquid API to get account balance...")

	// Step 1: Query Spot account balance
	spotState, err := t.exchange.Info().SpotUserState(t.ctx, t.walletAddr)
	spotOK := err == nil
	var spotUSDCBalance float64 = 0.0
	if err != nil {
		logger.Infof("⚠️ Failed to query Spot balance (may have no spot assets): %v", err)
	} else if spotState != nil && len(spotState.Balances) > 0 {
		for _, balance := range spotState.Balances {
			if balance.Coin == "USDC" {
				spotUSDCBalance, _ = strconv.ParseFloat(balance.Total, 64)
				logger.Infof("✓ Found Spot balance: %.2f USDC", spotUSDCBalance)
				break
			}
		}
	}

	// Step 2: Query Perpetuals contract account status
	accountState, err := t.exchange.Info().UserState(t.ctx, t.walletAddr)
	if err != nil {
		logger.Infof("❌ Hyperliquid Perpetuals API call failed: %v", err)
		return types.AccountBalance{}, fmt.Errorf("failed to get account information: %w", err)
	}

	// Step 3: Dynamically select correct summary based on margin mode (CrossMarginSummary or MarginSummary)
	var accountValue, totalMarginUsed float64
	var summaryType string
	var summary interface{}

	var parseErr error
	if t.isCrossMargin {
		// Cross margin mode: use CrossMarginSummary
		if accountValue, parseErr = types.ParseFloatField("accountValue", accountState.CrossMarginSummary.AccountValue); parseErr != nil {
			return types.AccountBalance{}, parseErr
		}
		if totalMarginUsed, parseErr = types.ParseFloatField("totalMarginUsed", accountState.CrossMarginSummary.TotalMarginUsed); parseErr != nil {
			return types.AccountBalance{}, parseErr
		}
		summaryType = "CrossMarginSummary (cross margin)"
		summary = accountState.CrossMarginSummary
	} else {
		// Isolated margin mode: use MarginSummary
		if accountValue, parseErr = types.ParseFloatField("accountValue", accountState.MarginSummary.AccountValue); parseErr != nil {
			return types.AccountBalance{}, parseErr
		}
		if totalMarginUsed, parseErr = types.ParseFloatField("totalMarginUsed", accountState.MarginSummary.TotalMarginUsed); parseErr != nil {
			return types.AccountBalance{}, parseErr
		}
		summaryType = "MarginSummary (isolated margin)"
		summary = accountState.MarginSummary
	}

	// Debug: Print complete summary structure returned by API
	summaryJSON, _ := json.MarshalIndent(summary, "  ", "  ")
	logger.Infof("🔍 [DEBUG] Hyperliquid API %s complete data:", summaryType)
	logger.Infof("%s", string(summaryJSON))

	// Critical fix: Accumulate actual unrealized PnL from all positions
	totalUnrealizedPnl := 0.0
	for _, assetPos := range accountState.AssetPositions {
		unrealizedPnl, _ := strconv.ParseFloat(assetPos.Position.UnrealizedPnl, 64)
		totalUnrealizedPnl += unrealizedPnl
	}

	// Correctly understand Hyperliquid fields:
	// AccountValue = Total account equity (includes idle funds + position value + unrealized PnL)
	// TotalMarginUsed = Margin used by positions (included in AccountValue, for display only)
	//
	// The wallet (equity minus unrealized) is derived inside assembleBalance, so
	// the two figures cannot drift apart.

	// Step 4: Query xyz dex balance (stock perps, forex, commodities)
	var xyzBal *xyzDexBalance
	xyzBal, err = t.getXYZDexBalance()
	xyzOK := err == nil
	if err != nil {
		// xyz dex query failed - log warning but don't fail the entire balance query
		logger.Infof("⚠️ Failed to query xyz dex balance: %v", err)
		xyzBal = &xyzDexBalance{}
	}
	// Always log xyz dex state for debugging
	logger.Infof("🔍 xyz dex state: accountValue=%.4f, withdrawable=%.4f, unrealizedPnl=%.4f, positions=%d",
		xyzBal.AccountValue, xyzBal.Withdrawable, xyzBal.UnrealizedPnL, len(xyzBal.Positions))
	for _, pos := range xyzBal.Positions {
		entryPx := "nil"
		if pos.Position.EntryPx != nil {
			entryPx = *pos.Position.EntryPx
		}
		logger.Infof("   └─ %s: size=%s, entryPx=%s, posValue=%s, pnl=%s",
			pos.Position.Coin, pos.Position.Szi, entryPx, pos.Position.PositionValue, pos.Position.UnrealizedPnl)
	}

	// Step 5: assemble the typed account balance (equity == wallet + unrealized).
	balance := assembleBalance(perpSnapshot{
		accountValue:  accountValue,
		marginUsed:    totalMarginUsed,
		unrealizedPnL: totalUnrealizedPnl,
		withdrawable:  accountState.Withdrawable,
	}, spotUSDCBalance, *xyzBal, t.isUnifiedAccount, spotOK && xyzOK)

	printAccountSummary(accountValue, totalMarginUsed, totalUnrealizedPnl, spotUSDCBalance, *xyzBal, balance)

	return balance, nil
}

// GetBalance renders the compatibility map. Prefer GetAccountBalance.
func (t *HyperliquidTrader) GetBalance() (map[string]interface{}, error) {
	balance, err := t.GetAccountBalance()
	if err != nil {
		return nil, err
	}
	return balance.ToMap(), nil
}

// perpSnapshot is the slice of the main perp clearinghouse state that the
// balance assembly needs.
type perpSnapshot struct {
	accountValue  float64 // margin summary account value (wallet + unrealized)
	marginUsed    float64
	unrealizedPnL float64
	withdrawable  string // raw venue field; empty when absent
}

// assembleBalance turns the raw venue state into the canonical balance map.
//
// The invariant every consumer relies on is
//
//	equity == wallet + unrealized
//
// so the two figures are derived from the same inputs instead of being added
// independently. It is what makes reconcilePnLBaseline's cash-flow detection
// meaningful: wallet must move only on realized PnL, fees, funding and
// transfers.
//
// Unified vs Standard matters because the xyz (HIP-3) dex does not own a
// separate USDC pool in every mode:
//   - Unified (the venue default): one USDC balance backs perp, spot and XYZ
//     alike. The XYZ clearinghouse state still reports an accountValue, but it
//     is that position's MARGIN overlaid on the shared balance — adding it
//     would count the margin twice, which is exactly how a 26.76 USDT margin
//     used to surface as a phantom deposit (and a 15% phantom drawdown).
//   - Standard/Manual: perp, spot and each DEX keep separate balances that the
//     user must fund explicitly, so xyz equity has to be added.
func assembleBalance(perp perpSnapshot, spotUSDC float64, xyz xyzDexBalance, unified bool, complete bool) types.AccountBalance {
	unrealized := perp.unrealizedPnL + xyz.UnrealizedPnL

	// Available margin: the venue's Withdrawable field is authoritative when
	// present (PR #443); otherwise fall back to equity minus used margin.
	available := 0.0
	if perp.withdrawable != "" {
		if withdrawable, err := strconv.ParseFloat(perp.withdrawable, 64); err == nil && withdrawable > 0 {
			available = withdrawable
		}
	} else {
		available = perp.accountValue - perp.marginUsed
		if available < 0 {
			available = 0
		}
	}
	// In unified mode the spot USDC is usable collateral too, so the margin
	// available to a new order is the sum of both pools.
	if unified && spotUSDC > 0 {
		available += spotUSDC
	}

	var balance types.AccountBalance
	if unified {
		balance = types.BalanceFromEquity("hyperliquid", perp.accountValue+spotUSDC, unrealized, available)
	} else {
		wallet := (perp.accountValue - perp.unrealizedPnL) + spotUSDC + (xyz.AccountValue - xyz.UnrealizedPnL)
		balance = types.BalanceFromWallet("hyperliquid", wallet, unrealized, available)
	}

	balance.MarginUsed = perp.marginUsed
	balance.XyzSeparate = !unified
	balance.XyzEquity = xyz.AccountValue
	balance.XyzAvailable = xyz.Withdrawable
	if !complete {
		balance = balance.Incomplete()
	}
	if err := balance.Validate(); err != nil {
		logger.Warnf("⚠️ %v", err)
	}

	return balance
}

// printAccountSummary renders the account breakdown the operator reads in the
// logs. Kept out of GetAccountBalance so the assembly stays unit-testable.
func printAccountSummary(accountValue, totalMarginUsed, totalUnrealizedPnl, spotUSDC float64, xyz xyzDexBalance, result types.AccountBalance) {
	logger.Infof("✓ Hyperliquid complete account:")
	logger.Infof("  • Spot balance: %.2f USDC", spotUSDC)
	logger.Infof("  • Perpetuals equity: %.2f USDC (wallet %.2f + unrealized %.2f)",
		accountValue,
		accountValue-totalUnrealizedPnl,
		totalUnrealizedPnl)
	logger.Infof("  • Perpetuals available balance: %.2f USDC", result.AvailableMargin)
	logger.Infof("  • Margin used: %.2f USDC", totalMarginUsed)
	logger.Infof("  • xyz dex equity: %.2f USDC (wallet %.2f + unrealized %.2f), available %.2f, separate pool: %v",
		xyz.AccountValue,
		xyz.AccountValue-xyz.UnrealizedPnL,
		xyz.UnrealizedPnL,
		xyz.Withdrawable,
		result.XyzSeparate)
	logger.Infof("  • Total assets (Perp+Spot+xyz): %.2f USDC", result.Wallet)
	logger.Infof("  ⭐ Total: %.2f USDC | Perp avail: %.2f | Spot: %.2f | xyz: %.2f (avail %.2f)",
		result.Wallet, result.AvailableMargin, spotUSDC, xyz.AccountValue, xyz.Withdrawable)
}

// xyzDexState represents the clearinghouse state for xyz dex
type xyzDexState struct {
	MarginSummary      *xyzMarginSummary  `json:"marginSummary,omitempty"`
	CrossMarginSummary *xyzMarginSummary  `json:"crossMarginSummary,omitempty"`
	Withdrawable       string             `json:"withdrawable,omitempty"`
	AssetPositions     []xyzAssetPosition `json:"assetPositions,omitempty"`
}

// xyzDexBalance is the parsed margin state of the xyz (HIP-3) perp dex.
//
// AccountValue means different things per account abstraction mode: in
// Standard/Manual mode it is a genuinely separate USDC pool that the user had
// to fund, while in the default Unified mode it is just the margin locked by
// the xyz position on top of the shared spot balance. assembleBalance only
// counts it as equity in the former case.
type xyzDexBalance struct {
	AccountValue  float64 // equity, including unrealized PnL
	Withdrawable  float64 // free USDC available to open new xyz orders
	UnrealizedPnL float64
	Positions     []xyzAssetPosition
}

type xyzMarginSummary struct {
	AccountValue    string `json:"accountValue"`
	TotalMarginUsed string `json:"totalMarginUsed"`
}

type xyzAssetPosition struct {
	Position struct {
		Coin          string  `json:"coin"`
		Szi           string  `json:"szi"`
		EntryPx       *string `json:"entryPx"`
		PositionValue string  `json:"positionValue"`
		UnrealizedPnl string  `json:"unrealizedPnl"`
		LiquidationPx *string `json:"liquidationPx"`
		Leverage      struct {
			Type  string `json:"type"`
			Value int    `json:"value"`
		} `json:"leverage"`
	} `json:"position"`
}

// getXYZDexBalance queries the xyz dex balance (stock perps, forex, commodities)
func (t *HyperliquidTrader) getXYZDexBalance() (*xyzDexBalance, error) {
	// Build request for xyz dex clearinghouse state
	reqBody := map[string]interface{}{
		"type": "clearinghouseState",
		"user": t.walletAddr,
		"dex":  "xyz",
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Determine API URL
	apiURL := "https://api.hyperliquid.xyz/info"
	// Note: xyz dex may not be available on testnet

	req, err := http.NewRequestWithContext(t.ctx, "POST", apiURL, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("xyz dex API error (status %d): %s", resp.StatusCode, string(body))
	}

	var state xyzDexState
	if err := json.Unmarshal(body, &state); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	bal := &xyzDexBalance{Positions: state.AssetPositions}

	// Parse account value - xyz dex uses MarginSummary for isolated margin mode
	// CrossMarginSummary may exist but with 0 values, so check MarginSummary first
	if state.MarginSummary != nil && state.MarginSummary.AccountValue != "" {
		av, _ := strconv.ParseFloat(state.MarginSummary.AccountValue, 64)
		if av > 0 {
			bal.AccountValue = av
		}
	}
	// Fallback to CrossMarginSummary if MarginSummary is 0
	if bal.AccountValue == 0 && state.CrossMarginSummary != nil && state.CrossMarginSummary.AccountValue != "" {
		bal.AccountValue, _ = strconv.ParseFloat(state.CrossMarginSummary.AccountValue, 64)
	}

	// Free margin: the venue's own withdrawable figure, falling back to
	// equity minus used margin when the field is absent.
	if state.Withdrawable != "" {
		bal.Withdrawable, _ = strconv.ParseFloat(state.Withdrawable, 64)
	} else if state.MarginSummary != nil && state.MarginSummary.TotalMarginUsed != "" {
		if used, usedErr := strconv.ParseFloat(state.MarginSummary.TotalMarginUsed, 64); usedErr == nil {
			bal.Withdrawable = bal.AccountValue - used
			if bal.Withdrawable < 0 {
				bal.Withdrawable = 0
			}
		}
	}

	// Calculate total unrealized PnL from positions
	for _, pos := range state.AssetPositions {
		pnl, _ := strconv.ParseFloat(pos.Position.UnrealizedPnl, 64)
		bal.UnrealizedPnL += pnl
	}

	return bal, nil
}

// GetMarketPrice gets market price (supports both crypto and xyz dex assets)
func (t *HyperliquidTrader) GetMarketPrice(symbol string) (float64, error) {
	coin := convertSymbolToHyperliquid(symbol)

	// Check if this is an xyz dex asset
	if strings.HasPrefix(coin, "xyz:") {
		return t.getXyzMarketPrice(coin)
	}

	// Get all market prices for crypto
	allMids, err := t.exchange.Info().AllMids(t.ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get price: %w", err)
	}

	// Find price for corresponding coin (allMids is map[string]string)
	if priceStr, ok := allMids[coin]; ok {
		priceFloat, err := strconv.ParseFloat(priceStr, 64)
		if err == nil {
			return priceFloat, nil
		}
		return 0, fmt.Errorf("price format error: %v", err)
	}

	return 0, fmt.Errorf("price not found for %s", symbol)
}

// getXyzMarketPrice gets market price for xyz dex assets
func (t *HyperliquidTrader) getXyzMarketPrice(coin string) (float64, error) {
	// Build request for xyz dex allMids
	reqBody := map[string]string{
		"type": "allMids",
		"dex":  "xyz",
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal request: %w", err)
	}

	apiURL := "https://api.hyperliquid.xyz/info"

	req, err := http.NewRequestWithContext(t.ctx, "POST", apiURL, bytes.NewBuffer(jsonBody))
	if err != nil {
		return 0, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("xyz dex allMids API error (status %d): %s", resp.StatusCode, string(body))
	}

	var mids map[string]string
	if err := json.Unmarshal(body, &mids); err != nil {
		return 0, fmt.Errorf("failed to parse response: %w", err)
	}

	// The API returns keys with xyz: prefix, so ensure the coin has it
	lookupKey := coin
	if !strings.HasPrefix(lookupKey, "xyz:") {
		lookupKey = "xyz:" + lookupKey
	}

	if priceStr, ok := mids[lookupKey]; ok {
		priceFloat, err := strconv.ParseFloat(priceStr, 64)
		if err == nil {
			return priceFloat, nil
		}
		return 0, fmt.Errorf("price format error: %v", err)
	}

	return 0, fmt.Errorf("xyz dex price not found for %s (lookup key: %s)", coin, lookupKey)
}

// GetOrderStatus gets order status
// Hyperliquid uses IOC orders, usually filled or cancelled immediately
// For completed orders, need to query historical records
func (t *HyperliquidTrader) GetOrderStatus(symbol string, orderID string) (*connector.OrderResult, error) {
	// Hyperliquid's IOC orders are completed almost immediately
	// If order was placed through this system, returned status will be FILLED
	// Try to query open orders to determine if still pending
	coin := convertSymbolToHyperliquid(symbol)

	// First check if in open orders
	openOrders, err := t.exchange.Info().OpenOrders(t.ctx, t.walletAddr)
	if err != nil {
		// Never report an unknown state as FILLED — callers record fills
		// based on this value. Surface the error instead.
		return nil, fmt.Errorf("failed to query open orders: %w", err)
	}

	// Check if order is in open orders list
	for _, order := range openOrders {
		if order.Coin == coin && fmt.Sprintf("%d", order.Oid) == orderID {
			// Order is still pending
			return &connector.OrderResult{
				OrderID: orderID,
				Status:  "NEW",
			}, nil
		}
	}

	// Order not in open list, meaning completed or cancelled
	// Hyperliquid IOC orders not in open list are usually filled
	return &connector.OrderResult{
		OrderID: orderID,
		Status:  "FILLED",
	}, nil
}

// GetClosedPnL gets recent closing trades from Hyperliquid
// Note: Hyperliquid does NOT have a position history API, only fill history.
// This returns individual closing trades for real-time position closure detection.
func (t *HyperliquidTrader) GetClosedPnL(startTime time.Time, limit int) ([]types.ClosedPnLRecord, error) {
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

		// Determine side (Hyperliquid uses one-way mode)
		side := "long"
		if trade.Side == "SELL" || trade.Side == "Sell" {
			side = "long" // Selling closes long
		} else {
			side = "short" // Buying closes short
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

// GetTrades retrieves trade history from Hyperliquid
func (t *HyperliquidTrader) GetTrades(startTime time.Time, limit int) ([]types.TradeRecord, error) {
	// Use UserFillsByTime API
	startTimeMs := startTime.UnixMilli()
	fills, err := t.exchange.Info().UserFillsByTime(t.ctx, t.walletAddr, startTimeMs, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get user fills: %w", err)
	}

	var trades []types.TradeRecord
	for _, fill := range fills {
		price, _ := strconv.ParseFloat(fill.Price, 64)
		qty, _ := strconv.ParseFloat(fill.Size, 64)
		fee, _ := strconv.ParseFloat(fill.Fee, 64)
		pnl, _ := strconv.ParseFloat(fill.ClosedPnl, 64)

		// Determine side: "B" = Buy, "S" = Sell (or "A" = Ask, "B" = Bid)
		var side string
		if fill.Side == "B" || fill.Side == "Buy" || fill.Side == "bid" {
			side = "BUY"
		} else {
			side = "SELL"
		}

		// Parse Dir field to get order action
		// Hyperliquid Dir values: "Open Long", "Open Short", "Close Long", "Close Short"
		var orderAction string
		switch strings.ToLower(fill.Dir) {
		case "open long":
			orderAction = "open_long"
		case "open short":
			orderAction = "open_short"
		case "close long":
			orderAction = "close_long"
		case "close short":
			orderAction = "close_short"
		default:
			// Fallback: use RealizedPnL if Dir is missing/unknown
			if pnl != 0 {
				if side == "BUY" {
					orderAction = "close_short"
				} else {
					orderAction = "close_long"
				}
			} else {
				if side == "BUY" {
					orderAction = "open_long"
				} else {
					orderAction = "open_short"
				}
			}
		}

		// Hyperliquid uses one-way mode, so PositionSide is "BOTH"
		trade := types.TradeRecord{
			TradeID:      strconv.FormatInt(fill.Tid, 10),
			Symbol:       fill.Coin,
			Side:         side,
			PositionSide: "BOTH", // Hyperliquid doesn't have hedge mode
			OrderAction:  orderAction,
			Price:        price,
			Quantity:     qty,
			RealizedPnL:  pnl,
			Fee:          fee,
			Time:         time.UnixMilli(fill.Time).UTC(),
		}
		trades = append(trades, trade)
	}

	return trades, nil
}

// GetOpenOrders gets all open/pending orders for a symbol
func (t *HyperliquidTrader) GetOpenOrders(symbol string) ([]types.OpenOrder, error) {
	openOrders, err := t.exchange.Info().OpenOrders(t.ctx, t.walletAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to get open orders: %w", err)
	}

	var result []types.OpenOrder
	for _, order := range openOrders {
		if order.Coin != symbol {
			continue
		}

		side := "BUY"
		if order.Side == "A" {
			side = "SELL"
		}

		result = append(result, types.OpenOrder{
			OrderID:      fmt.Sprintf("%d", order.Oid),
			Symbol:       order.Coin,
			Side:         side,
			PositionSide: "",
			Type:         "LIMIT",
			Price:        order.LimitPx,
			StopPrice:    0,
			Quantity:     order.Size,
			Status:       "NEW",
		})
	}

	return result, nil
}

// GetOrderBook gets the order book for a symbol
// Implements GridTrader interface
func (t *HyperliquidTrader) GetOrderBook(symbol string, depth int) (bids, asks [][]float64, err error) {
	coin := convertSymbolToHyperliquid(symbol)

	l2Book, err := t.exchange.Info().L2Snapshot(t.ctx, coin)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get order book: %w", err)
	}

	if l2Book == nil || len(l2Book.Levels) < 2 {
		return nil, nil, fmt.Errorf("invalid order book data")
	}

	// Parse bids (first level array)
	for i, level := range l2Book.Levels[0] {
		if i >= depth {
			break
		}
		bids = append(bids, []float64{level.Px, level.Sz})
	}

	// Parse asks (second level array)
	for i, level := range l2Book.Levels[1] {
		if i >= depth {
			break
		}
		asks = append(asks, []float64{level.Px, level.Sz})
	}

	return bids, asks, nil
}
