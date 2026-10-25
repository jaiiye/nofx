package kucoin

import (
	"encoding/json"
	"fmt"
	"nofx/logger"
	"nofx/trader/types"
	"strings"
	"time"
)

// GetAccountBalance gets the typed account state.
func (t *KuCoinTrader) GetAccountBalance() (types.AccountBalance, error) {
	// Check cache
	t.balanceCacheMutex.RLock()
	if t.cachedBalance != nil && time.Since(t.balanceCacheTime) < t.cacheDuration {
		cached := t.cachedBalance
		t.balanceCacheMutex.RUnlock()
		return types.FromMap(cached), nil
	}
	t.balanceCacheMutex.RUnlock()

	data, err := t.doRequest("GET", kucoinAccountPath+"?currency=USDT", nil)
	if err != nil {
		return types.AccountBalance{}, fmt.Errorf("failed to get account balance: %w", err)
	}

	balance, err := parseAccountBalance(data)
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
func (t *KuCoinTrader) GetBalance() (map[string]interface{}, error) {
	balance, err := t.GetAccountBalance()
	if err != nil {
		return nil, err
	}
	return balance.ToMap(), nil
}

// parseAccountBalance maps the raw futures account payload onto the canonical
// balance map. Extracted so the empty-payload guard is testable without HTTP.
func parseAccountBalance(data []byte) (types.AccountBalance, error) {
	// An empty payload (or a null/[] response) unmarshals into a zero-valued
	// struct and used to be returned as a successful all-zero balance, which
	// zeroes equity, PnL% and available margin at once. Distinguish "we could
	// not read it" from a genuine zero balance, which comes back as a full
	// object.
	if trimmed := strings.TrimSpace(string(data)); trimmed == "" || trimmed == "null" || trimmed == "{}" || trimmed == "[]" {
		return types.AccountBalance{}, fmt.Errorf("kucoin: empty balance payload")
	}

	var account struct {
		AccountEquity    float64 `json:"accountEquity"`
		UnrealisedPNL    float64 `json:"unrealisedPNL"`
		MarginBalance    float64 `json:"marginBalance"`
		PositionMargin   float64 `json:"positionMargin"`
		OrderMargin      float64 `json:"orderMargin"`
		FrozenFunds      float64 `json:"frozenFunds"`
		AvailableBalance float64 `json:"availableBalance"`
		Currency         string  `json:"currency"`
	}

	if err := json.Unmarshal(data, &account); err != nil {
		return types.AccountBalance{}, fmt.Errorf("failed to parse balance data: %w", err)
	}

	logger.Infof("✓ KuCoin balance: Total equity=%.2f, Available=%.2f, Unrealized PnL=%.2f",
		account.AccountEquity, account.AvailableBalance, account.UnrealisedPNL)

	return types.BalanceFromEquity("kucoin", account.AccountEquity, account.UnrealisedPNL, account.AvailableBalance), nil
}
