package api

import "testing"

func TestEquityPointPnL(t *testing.T) {
	tests := []struct {
		name        string
		totalEquity float64
		baseline    float64
		wantPnL     float64
		wantPct     float64
	}{
		{name: "profit", totalEquity: 161, baseline: 75, wantPnL: 86, wantPct: 114.66666666666667},
		{name: "loss", totalEquity: 66.69, baseline: 75, wantPnL: -8.31, wantPct: -11.08},
		{name: "flat", totalEquity: 100, baseline: 100, wantPnL: 0, wantPct: 0},
		{name: "missing baseline", totalEquity: 100, baseline: 0, wantPnL: 0, wantPct: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pnl, pct := equityPointPnL(tt.totalEquity, tt.baseline)
			if diff := pnl - tt.wantPnL; diff > 1e-9 || diff < -1e-9 {
				t.Fatalf("pnl = %v, want %v", pnl, tt.wantPnL)
			}
			if diff := pct - tt.wantPct; diff > 1e-9 || diff < -1e-9 {
				t.Fatalf("pct = %v, want %v", pct, tt.wantPct)
			}
		})
	}
}

// A deposit must not read as profit: folding it into the baseline keeps the PnL
// amount at the trading result instead of the wallet jump. The percentage is
// diluted by the larger capital base, which is intended — the amount is what
// has to stay cash-flow neutral.
func TestEquityPointPnLCashFlowNeutral(t *testing.T) {
	const deposit = 86.0
	beforePnL, _ := equityPointPnL(66.69, 75.0)
	afterPnL, _ := equityPointPnL(66.69+deposit, 75.0+deposit)

	if diff := afterPnL - beforePnL; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("cash flow changed the PnL amount: before=%v after=%v", beforePnL, afterPnL)
	}
	if afterPnL >= 0 {
		t.Fatalf("expected the trading loss to survive the deposit, got %v", afterPnL)
	}

	// The bug this guards against: an unadjusted baseline reports the deposit
	// itself as profit (the live account showed +102.94% after an $86 deposit).
	unadjustedPnL, unadjustedPct := equityPointPnL(66.69+deposit, 75.0)
	if unadjustedPnL <= 0 {
		t.Fatalf("expected the unadjusted baseline to show the deposit as profit, got %v", unadjustedPnL)
	}
	if unadjustedPct < 100 {
		t.Fatalf("expected the unadjusted distortion to exceed 100%%, got %v%%", unadjustedPct)
	}
}

func TestResolveSnapshotBaseline(t *testing.T) {
	if got := resolveSnapshotBaseline(120, 75); got != 120 {
		t.Fatalf("stored baseline should win, got %v", got)
	}
	if got := resolveSnapshotBaseline(0, 75); got != 75 {
		t.Fatalf("missing baseline should fall back, got %v", got)
	}
}
