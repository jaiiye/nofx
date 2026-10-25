package bitget

import "testing"

func TestBalanceFromAccountsMapsUSDT(t *testing.T) {
	got, err := balanceFromAccounts([]bitgetAccount{
		{MarginCoin: "BTC", AccountEquity: "1", Available: "1", UnrealizedPL: "0"},
		{MarginCoin: "USDT", AccountEquity: "1100", Available: "900", UnrealizedPL: "100"},
	})
	if err != nil {
		t.Fatalf("balanceFromAccounts: %v", err)
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
	if err := got.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	// Both key styles are still emitted for the legacy map consumers.
	if got.ToMap()["total_equity"].(float64) != 1100 {
		t.Fatalf("total_equity alias = %v, want 1100", got.ToMap()["total_equity"])
	}
}

// An empty account list used to produce a successful all-zero balance map,
// which reads as "no money" rather than "the call returned nothing".
func TestBalanceFromAccountsRejectsMissingUSDT(t *testing.T) {
	if _, err := balanceFromAccounts(nil); err == nil {
		t.Fatal("expected an error for an empty account list")
	}
	if _, err := balanceFromAccounts([]bitgetAccount{{MarginCoin: "BTC", AccountEquity: "1"}}); err == nil {
		t.Fatal("expected an error when no USDT account is present")
	}
}
