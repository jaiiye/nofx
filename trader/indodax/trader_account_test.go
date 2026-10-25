package indodax

import "testing"

// Crypto holdings are part of the account value. Ignoring them made every spot
// buy look like an equal-sized loss: IDR cash fell but equity did not move.
func TestAssembleIndodaxBalanceIncludesCryptoValue(t *testing.T) {
	got := assembleIndodaxBalance(1_000_000, 0, 2_500_000)

	if got.Equity != 3_500_000 {
		t.Fatalf("equity = %v, want 3500000", got.Equity)
	}
	if got.Wallet != 1_000_000 {
		t.Fatalf("wallet = %v, want 1000000 (IDR cash only)", got.Wallet)
	}
	if got.UnrealizedPnL != 2_500_000 {
		t.Fatalf("unrealized = %v, want 2500000", got.UnrealizedPnL)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestAssembleIndodaxBalanceCashOnly(t *testing.T) {
	got := assembleIndodaxBalance(750_000, 250_000, 0)

	if got.Equity != 1_000_000 {
		t.Fatalf("equity = %v, want 1000000", got.Equity)
	}
	if got.AvailableMargin != 750_000 {
		t.Fatalf("available = %v, want 750000", got.AvailableMargin)
	}
}
