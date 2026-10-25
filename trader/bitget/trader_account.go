package bitget

import (
	"encoding/json"
	"fmt"
	"nofx/logger"
	"nofx/trader/types"
	"strconv"
	"strings"
	"time"
)

// GetAccountBalance gets the typed account state.
func (t *BitgetTrader) GetAccountBalance() (types.AccountBalance, error) {
	// Check cache
	t.balanceCacheMutex.RLock()
	if t.cachedBalance != nil && time.Since(t.balanceCacheTime) < t.cacheDuration {
		cached := t.cachedBalance
		t.balanceCacheMutex.RUnlock()
		return types.FromMap(cached), nil
	}
	t.balanceCacheMutex.RUnlock()

	params := map[string]interface{}{
		"productType": "USDT-FUTURES",
	}

	data, err := t.doRequest("GET", bitgetAccountPath, params)
	if err != nil {
		return types.AccountBalance{}, fmt.Errorf("failed to get account balance: %w", err)
	}

	var accounts []bitgetAccount

	if err := json.Unmarshal(data, &accounts); err != nil {
		return types.AccountBalance{}, fmt.Errorf("failed to parse balance data: %w, raw: %s", err, string(data))
	}

	balance, err := balanceFromAccounts(accounts)
	if err != nil {
		return types.AccountBalance{}, err
	}

	// Update cache
	t.balanceCacheMutex.Lock()
	t.cachedBalance = balance.ToMap()
	t.balanceCacheTime = time.Now()
	t.balanceCacheMutex.Unlock()

	return balance, nil
}

// GetBalance renders the compatibility map. Prefer GetAccountBalance.
func (t *BitgetTrader) GetBalance() (map[string]interface{}, error) {
	balance, err := t.GetAccountBalance()
	if err != nil {
		return nil, err
	}
	return balance.ToMap(), nil
}

// bitgetAccount is one entry of /api/v2/mix/account/accounts.
type bitgetAccount struct {
	MarginCoin    string `json:"marginCoin"`
	Available     string `json:"available"`     // Available balance
	AccountEquity string `json:"accountEquity"` // Total equity
	UsdtEquity    string `json:"usdtEquity"`    // USDT equity
	UnrealizedPL  string `json:"unrealizedPL"`  // Unrealized P&L
}

// balanceFromAccounts maps the venue's account list onto the canonical balance
// map. Extracted so the "no USDT account" path is testable without HTTP.
func balanceFromAccounts(accounts []bitgetAccount) (types.AccountBalance, error) {
	var totalEquity, availableBalance, unrealizedPnL float64
	foundUSDT := false
	for _, acc := range accounts {
		if acc.MarginCoin != "USDT" {
			continue
		}
		foundUSDT = true
		var err error
		if totalEquity, err = types.ParseFloatField("accountEquity", acc.AccountEquity); err != nil {
			return types.AccountBalance{}, err
		}
		if availableBalance, err = types.ParseFloatField("available", acc.Available); err != nil {
			return types.AccountBalance{}, err
		}
		if unrealizedPnL, err = types.ParseFloatField("unrealizedPL", acc.UnrealizedPL); err != nil {
			return types.AccountBalance{}, err
		}
		logger.Infof("✓ [Bitget] Balance: equity=%.2f, available=%.2f", totalEquity, availableBalance)
		break
	}
	if !foundUSDT {
		// Returning an all-zero map here used to zero out equity, PnL% and
		// available margin in one go, which the UI and the risk gate read as
		// "the account has no money" instead of "the call failed".
		return types.AccountBalance{}, fmt.Errorf("bitget: USDT margin account not found (productType=USDT-FUTURES, accounts=%d)", len(accounts))
	}

	return types.BalanceFromEquity("bitget", totalEquity, unrealizedPnL, availableBalance), nil
}

// SetMarginMode sets margin mode
func (t *BitgetTrader) SetMarginMode(symbol string, isCrossMargin bool) error {
	symbol = t.convertSymbol(symbol)

	marginMode := "isolated"
	if isCrossMargin {
		marginMode = "crossed"
	}

	body := map[string]interface{}{
		"symbol":      symbol,
		"productType": "USDT-FUTURES",
		"marginCoin":  "USDT",
		"marginMode":  marginMode,
	}

	_, err := t.doRequest("POST", bitgetMarginModePath, body)
	if err != nil {
		if strings.Contains(err.Error(), "same") || strings.Contains(err.Error(), "already") {
			return nil
		}
		if strings.Contains(err.Error(), "position") {
			logger.Infof("  ⚠️ %s has positions, cannot change margin mode", symbol)
			return nil
		}
		return err
	}

	logger.Infof("  ✓ %s margin mode set to %s", symbol, marginMode)
	return nil
}

// SetLeverage sets leverage
func (t *BitgetTrader) SetLeverage(symbol string, leverage int) error {
	symbol = t.convertSymbol(symbol)

	body := map[string]interface{}{
		"symbol":      symbol,
		"productType": "USDT-FUTURES",
		"marginCoin":  "USDT",
		"leverage":    fmt.Sprintf("%d", leverage),
	}

	_, err := t.doRequest("POST", bitgetLeveragePath, body)
	if err != nil {
		if strings.Contains(err.Error(), "same") {
			return nil
		}
		logger.Infof("  ⚠️ Failed to set %s leverage: %v", symbol, err)
		return err
	}

	logger.Infof("  ✓ %s leverage set to %dx", symbol, leverage)
	return nil
}

// GetMarketPrice gets market price
func (t *BitgetTrader) GetMarketPrice(symbol string) (float64, error) {
	symbol = t.convertSymbol(symbol)

	params := map[string]interface{}{
		"symbol":      symbol,
		"productType": "USDT-FUTURES",
	}

	data, err := t.doRequest("GET", bitgetTickerPath, params)
	if err != nil {
		return 0, fmt.Errorf("failed to get price: %w", err)
	}

	var tickers []struct {
		LastPr string `json:"lastPr"`
	}

	if err := json.Unmarshal(data, &tickers); err != nil {
		return 0, err
	}

	if len(tickers) == 0 {
		return 0, fmt.Errorf("no price data received")
	}

	price, err := strconv.ParseFloat(tickers[0].LastPr, 64)
	if err != nil {
		return 0, err
	}

	return price, nil
}

// GetOrderBook gets the order book for a symbol
// Implements GridTrader interface
func (t *BitgetTrader) GetOrderBook(symbol string, depth int) (bids, asks [][]float64, err error) {
	symbol = t.convertSymbol(symbol)
	path := fmt.Sprintf("/api/v2/mix/market/depth?symbol=%s&productType=USDT-FUTURES&limit=%d", symbol, depth)

	data, err := t.doRequest("GET", path, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get order book: %w", err)
	}

	var result struct {
		Bids [][]string `json:"bids"`
		Asks [][]string `json:"asks"`
	}

	if err := json.Unmarshal(data, &result); err != nil {
		return nil, nil, fmt.Errorf("failed to parse order book: %w", err)
	}

	// Parse bids
	for _, b := range result.Bids {
		if len(b) >= 2 {
			price, _ := strconv.ParseFloat(b[0], 64)
			qty, _ := strconv.ParseFloat(b[1], 64)
			bids = append(bids, []float64{price, qty})
		}
	}

	// Parse asks
	for _, a := range result.Asks {
		if len(a) >= 2 {
			price, _ := strconv.ParseFloat(a[0], 64)
			qty, _ := strconv.ParseFloat(a[1], 64)
			asks = append(asks, []float64{price, qty})
		}
	}

	return bids, asks, nil
}
