package api

import (
	"math"
	"testing"

	"nofx/store"
	"nofx/trader"
)

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

	baselines := deriveSnapshotBaselines(points, steps)
	if math.Abs(baselines[0]-75.386714) > 1e-9 {
		t.Fatalf("pre-deposit baseline = %.6f, want the created capital 75.386714", baselines[0])
	}
	// Equal to the baseline the live reconciler re-derives from the same numbers.
	if math.Abs(baselines[1]-161.356138) > 1e-6 {
		t.Fatalf("post-deposit baseline = %.6f, want 161.356138", baselines[1])
	}

	// The last curve point now reports the trading result, not the deposit.
	if pnl, _ := trader.PnLAgainstBaseline(152.993038, baselines[1]); math.Abs(pnl-netRealized) > 1e-6 {
		t.Fatalf("pnl = %.4f, want the realized result %.4f", pnl, netRealized)
	}
	// ...where the created capital reported the deposit itself as profit.
	if _, pct := trader.PnLAgainstBaseline(152.993038, 75.386714); pct < 100 {
		t.Fatalf("expected the unrepaired baseline to overstate the return, got %.2f%%", pct)
	}
}

func TestDeriveSnapshotBaselinesPrefersTheStoredBaseline(t *testing.T) {
	steps := []store.ClosedPnLPt{{ExitTimeMs: 2_000, Net: 50}}
	points := []snapshotBaselineInput{
		{TimeMs: 3_000, Wallet: 10, Stored: 120}, // a stored baseline always wins
		{TimeMs: 3_000, Wallet: 10},              // 10 - 50 is not a capital base
	}

	baselines := deriveSnapshotBaselines(points, steps)
	if baselines[0] != 120 {
		t.Fatalf("stored baseline = %v, want 120", baselines[0])
	}
	if baselines[1] != 0 {
		t.Fatalf("unusable derivation = %v, want 0 (no capital base)", baselines[1])
	}
	if pnl, pct := trader.PnLAgainstBaseline(10, baselines[1]); pnl != 0 || pct != 0 {
		t.Fatalf("no capital base must report no PnL, got %v / %v%%", pnl, pct)
	}
}
