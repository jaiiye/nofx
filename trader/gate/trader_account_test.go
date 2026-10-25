package gate

import "testing"

// Gate's `total` already includes the unrealised PnL. Reporting it as the cash
// wallet made the loop's wallet+unrealized fallback double count, while the
// risk gate read it as equity — two different account values from one call.
func TestBalanceFromAccountFields(t *testing.T) {
	got := balanceFromAccountFields(1100, 900, 100)

	if got.Equity != 1100 {
		t.Fatalf("equity = %v, want 1100", got.Equity)
	}
	if got.Wallet != 1000 {
		t.Fatalf("wallet = %v, want 1000", got.Wallet)
	}
	if got.UnrealizedPnL != 100 {
		t.Fatalf("unrealized = %v, want 100", got.UnrealizedPnL)
	}
	if got.AvailableMargin != 900 {
		t.Fatalf("available = %v, want 900", got.AvailableMargin)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}
