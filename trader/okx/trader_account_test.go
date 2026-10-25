package okx

import (
	"math"
	"testing"
)

// OKX reports totalEq = Σ eq where eq = cashBal + upl. The USDT unrealized PnL
// is therefore already inside totalEq; the trading loop's fallback
// (wallet + unrealized) used to add it a second time and inflate the equity.
func TestParseAccountBalanceDoesNotDoubleCountUnrealized(t *testing.T) {
	raw := []byte(`[{
		"totalEq": "1100",
		"adjEq": "1090",
		"details": [
			{"ccy": "USDT", "eq": "1100", "cashBal": "1000", "availBal": "900", "upl": "100"},
			{"ccy": "BTC",  "eq": "0",    "cashBal": "0",    "availBal": "0",   "upl": "0"}
		]
	}]`)

	got, err := parseAccountBalance(raw)
	if err != nil {
		t.Fatalf("parseAccountBalance: %v", err)
	}

	assertField(t, "equity", got.Equity, 1100)
	assertField(t, "wallet", got.Wallet, 1000)
	assertField(t, "unrealized", got.UnrealizedPnL, 100)
	assertField(t, "available", got.AvailableMargin, 900)

	if err := got.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// Unrealized PnL is summed across all currencies, not just USDT: a BTC leg's
// floating PnL is part of the account equity even though it cannot size a
// USDT-margined order.
func TestParseAccountBalanceSumsAllCurrencyPnL(t *testing.T) {
	raw := []byte(`[{
		"totalEq": "210",
		"details": [
			{"ccy": "USDT", "eq": "150", "cashBal": "140", "availBal": "120", "upl": "10"},
			{"ccy": "BTC",  "eq": "60",  "cashBal": "0",   "availBal": "0",   "upl": "60"}
		]
	}]`)

	got, err := parseAccountBalance(raw)
	if err != nil {
		t.Fatalf("parseAccountBalance: %v", err)
	}
	assertField(t, "unrealized", got.UnrealizedPnL, 70)
	assertField(t, "wallet", got.Wallet, 140) // 210 - 70
	assertField(t, "available", got.AvailableMargin, 120)
}

func TestParseAccountBalanceRejectsMissingUSDT(t *testing.T) {
	raw := []byte(`[{"totalEq": "60", "details": [{"ccy": "BTC", "eq": "60", "cashBal": "0", "availBal": "0", "upl": "60"}]}]`)

	if _, err := parseAccountBalance(raw); err == nil {
		t.Fatal("expected an error for a balance payload without a USDT leg, got nil")
	}
}

func TestParseAccountBalanceRejectsEmptyPayload(t *testing.T) {
	if _, err := parseAccountBalance([]byte(`[]`)); err == nil {
		t.Fatal("expected an error for an empty balance payload, got nil")
	}
}

func assertField(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("%s = %.6f, want %.6f", name, got, want)
	}
}
