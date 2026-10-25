package indodax

import (
	"encoding/json"
	"fmt"
	"net/url"
	"nofx/logger"
	"nofx/trader/types"
	"strconv"
	"strings"
	"time"
)

// GetAccountBalance gets the typed account state from Indodax.
func (t *IndodaxTrader) GetAccountBalance() (types.AccountBalance, error) {
	// Check cache
	t.cacheMutex.RLock()
	if t.cachedBalance != nil && time.Since(t.balanceCacheTime) < t.cacheDuration {
		cached := t.cachedBalance
		t.cacheMutex.RUnlock()
		return types.FromMap(cached), nil
	}
	t.cacheMutex.RUnlock()

	params := url.Values{}
	params.Set("method", "getInfo")

	data, err := t.doPrivateRequest(params)
	if err != nil {
		return types.AccountBalance{}, fmt.Errorf("failed to get account info: %w", err)
	}

	var result struct {
		ServerTime  int64                  `json:"server_time"`
		Balance     map[string]interface{} `json:"balance"`
		BalanceHold map[string]interface{} `json:"balance_hold"`
		UserID      string                 `json:"user_id"`
		Name        string                 `json:"name"`
		Email       string                 `json:"email"`
	}

	if err := json.Unmarshal(data, &result); err != nil {
		return types.AccountBalance{}, fmt.Errorf("failed to parse balance: %w", err)
	}

	// Calculate total balance in IDR
	idrBalance := parseFloat(result.Balance["idr"])
	idrHold := parseFloat(result.BalanceHold["idr"])

	// Crypto holdings are part of the account's value. Leaving them out made
	// every spot buy look like an equal-sized loss (IDR down, equity unchanged)
	// even though GetPositions reports the holding at market.
	cryptoValue := 0.0
	if positions, posErr := t.GetPositions(); posErr != nil {
		logger.Infof("[Indodax] Unable to value crypto holdings: %v", posErr)
	} else {
		for _, p := range positions {
			if v, ok := p["notionalValue"].(float64); ok {
				cryptoValue += v
			}
		}
	}

	balance := assembleIndodaxBalance(idrBalance, idrHold, cryptoValue)

	// The compatibility map also carries the per-currency holdings: the
	// close-all path sizes a sell from balance_<coin>, and that is not part of
	// the account identity so the typed struct does not model it.
	cached := balance.ToMap()
	for currency, amount := range result.Balance {
		if currency != "idr" {
			cached["balance_"+currency] = parseFloat(amount)
		}
	}
	for currency, amount := range result.BalanceHold {
		if currency != "idr" {
			cached["hold_"+currency] = parseFloat(amount)
		}
	}

	// Update cache
	t.cacheMutex.Lock()
	t.cachedBalance = cached
	t.balanceCacheTime = time.Now()
	t.cacheMutex.Unlock()

	return balance, nil
}

// GetBalance renders the compatibility map. Prefer GetAccountBalance.
func (t *IndodaxTrader) GetBalance() (map[string]interface{}, error) {
	t.cacheMutex.RLock()
	cached := t.cachedBalance
	fresh := cached != nil && time.Since(t.balanceCacheTime) < t.cacheDuration
	t.cacheMutex.RUnlock()
	if fresh {
		return cached, nil
	}

	balance, err := t.GetAccountBalance()
	if err != nil {
		return nil, err
	}
	m := balance.ToMap()
	t.cacheMutex.RLock()
	if cached := t.cachedBalance; cached != nil {
		for k, v := range cached {
			if strings.HasPrefix(k, "balance_") || strings.HasPrefix(k, "hold_") {
				m[k] = v
			}
		}
	}
	t.cacheMutex.RUnlock()
	return m, nil
}

// assembleIndodaxBalance builds the canonical balance map from the IDR cash and
// the mark-to-market value of the crypto holdings.
//
// Spot has no entry-price tracking, so the non-cash side is reported as
// unrealized. That keeps equity == wallet + unrealized and makes the equity
// snapshot's wallet column stay "cash" (it used to be the whole equity, so the
// cash-flow baseline could not tell a buy from a withdrawal).
func assembleIndodaxBalance(idrBalance, idrHold, cryptoValue float64) types.AccountBalance {
	totalIDR := idrBalance + idrHold
	return types.BalanceFromWallet("indodax", totalIDR, cryptoValue, idrBalance)
}

// GetPositions returns currently held crypto balances as "positions"
// Since Indodax is spot-only, each non-zero crypto balance is treated as a position
func (t *IndodaxTrader) GetPositions() ([]map[string]interface{}, error) {
	// Check cache
	t.cacheMutex.RLock()
	if t.cachedPositions != nil && time.Since(t.positionCacheTime) < t.cacheDuration {
		cached := t.cachedPositions
		t.cacheMutex.RUnlock()
		return cached, nil
	}
	t.cacheMutex.RUnlock()

	params := url.Values{}
	params.Set("method", "getInfo")

	data, err := t.doPrivateRequest(params)
	if err != nil {
		return nil, fmt.Errorf("failed to get positions: %w", err)
	}

	var result struct {
		Balance     map[string]interface{} `json:"balance"`
		BalanceHold map[string]interface{} `json:"balance_hold"`
	}

	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to parse positions: %w", err)
	}

	var positions []map[string]interface{}

	for currency, amountRaw := range result.Balance {
		if currency == "idr" {
			continue
		}

		amount := parseFloat(amountRaw)
		holdAmount := parseFloat(result.BalanceHold[currency])
		totalAmount := amount + holdAmount

		if totalAmount <= 0 {
			continue
		}

		// Get market price for this coin
		markPrice, _ := t.GetMarketPrice(strings.ToUpper(currency) + "IDR")

		// Calculate position value in IDR
		notionalValue := totalAmount * markPrice

		position := map[string]interface{}{
			"symbol":           strings.ToUpper(currency) + "IDR",
			"side":             "LONG",
			"positionAmt":      totalAmount,
			"entryPrice":       markPrice, // Spot doesn't track entry price
			"markPrice":        markPrice,
			"unRealizedProfit": 0.0, // Spot doesn't track unrealized PnL
			"leverage":         1.0,
			"mgnMode":          "spot",
			"notionalValue":    notionalValue,
			"currency":         currency,
			"available":        amount,
			"hold":             holdAmount,
		}

		positions = append(positions, position)
	}

	// Update cache
	t.cacheMutex.Lock()
	t.cachedPositions = positions
	t.positionCacheTime = time.Now()
	t.cacheMutex.Unlock()

	return positions, nil
}

// GetClosedPnL gets closed position PnL records (trade history)
func (t *IndodaxTrader) GetClosedPnL(startTime time.Time, limit int) ([]types.ClosedPnLRecord, error) {
	// Indodax trade history is limited to 7 days range
	params := url.Values{}
	params.Set("method", "tradeHistory")
	params.Set("pair", "btc_idr") // Default pair; Indodax requires a pair
	if limit > 0 {
		params.Set("count", strconv.Itoa(limit))
	}
	if !startTime.IsZero() {
		params.Set("since", strconv.FormatInt(startTime.Unix(), 10))
	}

	data, err := t.doPrivateRequest(params)
	if err != nil {
		return nil, fmt.Errorf("failed to get trade history: %w", err)
	}

	var result struct {
		Trades []struct {
			TradeID       string `json:"trade_id"`
			OrderID       string `json:"order_id"`
			Type          string `json:"type"`
			Price         string `json:"price"`
			Fee           string `json:"fee"`
			TradeTime     string `json:"trade_time"`
			ClientOrderID string `json:"client_order_id"`
		} `json:"trades"`
	}

	if err := json.Unmarshal(data, &result); err != nil {
		// Trade history might return empty, that's fine
		logger.Infof("[Indodax] Trade history parse note: %v", err)
		return nil, nil
	}

	var records []types.ClosedPnLRecord
	for _, trade := range result.Trades {
		price, _ := strconv.ParseFloat(trade.Price, 64)
		fee, _ := strconv.ParseFloat(trade.Fee, 64)
		tradeTime, _ := strconv.ParseInt(trade.TradeTime, 10, 64)

		side := "long"
		if trade.Type == "sell" {
			side = "long" // Selling from a spot position is closing long
		}

		records = append(records, types.ClosedPnLRecord{
			Symbol:    "BTCIDR",
			Side:      side,
			ExitPrice: price,
			Fee:       fee,
			ExitTime:  time.Unix(tradeTime, 0),
			OrderID:   trade.OrderID,
			CloseType: "manual",
		})
	}

	return records, nil
}
