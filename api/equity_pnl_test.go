package api

import (
	"math"
	"testing"

	"nofx/store"
)

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

// Snapshots written before the per-snapshot baseline existed have to be
// reconstructed point by point: one baseline applied to the whole curve reports
// the deposit as profit on everything after it and as a loss on everything
// before it.
func TestDeriveSnapshotBaselinesReconstructsDeposit(t *testing.T) {
	const (
		preDeposit  = int64(1_791_600_000_000) // 2026-10-11, capital as created
		postDeposit = int64(1_793_000_000_000) // 2026-10-14, well after the deposit
	)
	const netRealized = -8.3631 // realized -4.2869 and 4.0762 of fees

	steps := []store.ClosedPnLPt{
		{ExitTimeMs: preDeposit + 1_000, Net: -0.11},
		{ExitTimeMs: postDeposit - 1_000, Net: netRealized + 0.11},
	}
	points := []snapshotBaselineInput{
		{TimeMs: preDeposit, Wallet: 75.386714},
		{TimeMs: postDeposit, Wallet: 152.993038},
	}

	baselines := deriveSnapshotBaselines(points, steps, 75.386714)
	if math.Abs(baselines[0]-75.386714) > 1e-9 {
		t.Fatalf("pre-deposit baseline = %.6f, want the created capital 75.386714", baselines[0])
	}
	// Equal to the baseline the live reconciler re-derives from the same numbers.
	if math.Abs(baselines[1]-161.356138) > 1e-6 {
		t.Fatalf("post-deposit baseline = %.6f, want 161.356138", baselines[1])
	}

	// The last curve point now reports the trading result, not the deposit.
	if pnl, _ := equityPointPnL(152.993038, baselines[1]); math.Abs(pnl-netRealized) > 1e-6 {
		t.Fatalf("pnl = %.4f, want the realized result %.4f", pnl, netRealized)
	}
	// ...where the configured baseline reported the deposit itself as profit.
	if _, pct := equityPointPnL(152.993038, 75.386714); pct < 100 {
		t.Fatalf("expected the unrepaired baseline to overstate the return, got %.2f%%", pct)
	}
}

func TestDeriveSnapshotBaselinesPrefersStoredAndFallsBack(t *testing.T) {
	steps := []store.ClosedPnLPt{{ExitTimeMs: 2_000, Net: 50}}
	points := []snapshotBaselineInput{
		{TimeMs: 1_000, Wallet: 100, Stored: 120}, // a stored baseline always wins
		{TimeMs: 3_000, Wallet: 10, Stored: 0},    // 10 - 50 < 0: not a usable baseline
	}

	baselines := deriveSnapshotBaselines(points, steps, 42)
	if baselines[0] != 120 {
		t.Fatalf("stored baseline = %v, want 120", baselines[0])
	}
	if baselines[1] != 42 {
		t.Fatalf("unusable derivation = %v, want the fallback 42", baselines[1])
	}
}
