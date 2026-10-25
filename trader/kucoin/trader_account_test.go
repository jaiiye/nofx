package kucoin

import "testing"

func TestParseAccountBalanceMapsFields(t *testing.T) {
	raw := []byte(`{"accountEquity":1100,"unrealisedPNL":100,"marginBalance":1000,"availableBalance":900,"currency":"USDT"}`)

	got, err := parseAccountBalance(raw)
	if err != nil {
		t.Fatalf("parseAccountBalance: %v", err)
	}
	if got.Equity != 1100 {
		t.Fatalf("equity = %v, want 1100", got.Equity)
	}
	if got.Wallet != 1000 {
		t.Fatalf("wallet = %v, want 1000", got.Wallet)
	}
	if got.UnrealizedPnL != 100 {
		t.Fatalf("unrealized = %v, want 100", got.UnrealizedPnL)
	}
	if got.ToMap()["total_equity"].(float64) != 1100 {
		t.Fatalf("total_equity alias = %v, want 1100", got.ToMap()["total_equity"])
	}
}

// An empty payload used to be returned as a successful all-zero balance, which
// zeroes equity, PnL% and available margin at once.
func TestParseAccountBalanceRejectsEmptyPayload(t *testing.T) {
	for _, payload := range []string{"", "null", "{}", "[]"} {
		if _, err := parseAccountBalance([]byte(payload)); err == nil {
			t.Fatalf("expected an error for payload %q", payload)
		}
	}
}
