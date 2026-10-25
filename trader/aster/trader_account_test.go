package aster

import "testing"

func TestAssembleAsterBalanceUsesVenueWallet(t *testing.T) {
	positions := []map[string]interface{}{
		{"positionAmt": 0.5, "unRealizedProfit": 7.0},
		{"positionAmt": -0.2, "unRealizedProfit": 3.0},
		{"positionAmt": 0.0, "unRealizedProfit": 999.0}, // ghost entry, must be ignored
	}

	got := assembleAsterBalance(120, 500, 0, positions, true)

	if got.Wallet != 500 {
		t.Fatalf("wallet = %v, want 500", got.Wallet)
	}
	if got.Equity != 510 {
		t.Fatalf("equity = %v, want 510", got.Equity)
	}
	if got.UnrealizedPnL != 10 {
		t.Fatalf("unrealized = %v, want 10", got.UnrealizedPnL)
	}
	if !got.Complete {
		t.Fatal("complete positions must not be flagged incomplete")
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// Equity must never be derived from the available balance, which excludes the
// margin locked by resting orders.
func TestAssembleAsterBalanceEquityIndependentOfAvailable(t *testing.T) {
	positions := []map[string]interface{}{{"positionAmt": 1.0, "unRealizedProfit": 0.0}}

	got := assembleAsterBalance(10, 500, 0, positions, true)
	if got.Equity != 500 {
		t.Fatalf("equity = %v, want 500 (wallet), not available+margin", got.Equity)
	}
}

func TestAssembleAsterBalanceDegradesOnPositionError(t *testing.T) {
	got := assembleAsterBalance(120, 500, 8, nil, false)

	if got.Complete {
		t.Fatal("expected an incomplete balance when positions could not be read")
	}
	if got.Equity != 508 {
		t.Fatalf("equity = %v, want 508", got.Equity)
	}
	if got.ToMap()["balance_incomplete"] != true {
		t.Fatal("incomplete flag missing from the compatibility map")
	}
}

func TestAssembleAsterBalanceToleratesMissingFields(t *testing.T) {
	positions := []map[string]interface{}{
		{"positionAmt": 1.0, "unRealizedProfit": 5.0},
		{"positionAmt": 1.0}, // field rename: must not panic, must flag
	}

	got := assembleAsterBalance(120, 500, 0, positions, true)
	if got.Complete {
		t.Fatal("expected an incomplete balance for a position missing unRealizedProfit")
	}
	if got.UnrealizedPnL != 5 {
		t.Fatalf("unrealized = %v, want 5", got.UnrealizedPnL)
	}
}
