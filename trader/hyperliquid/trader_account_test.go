package hyperliquid

import (
	"math"
	"testing"
)

// liveXYZFixture reproduces the account state captured from a running unified
// account at 2026-10-23 17:30 UTC, while a 0.778 xyz:CL long was open at 3x:
//
//	SpotUserState.total   = 146.934675  (unchanged by the position; already
//	                                     carries the xyz unrealized PnL)
//	xyz accountValue      =  26.766400  (the position's MARGIN, not a pool)
//	xyz unrealizedPnl     =   0.093360
//
// Before the fix this reported equity 173.70 — a phantom +26.76 that the
// cash-flow detector booked as an external deposit and the drawdown breaker
// later read as a 15.4% peak.
func liveXYZFixture() (perpSnapshot, float64, xyzDexBalance) {
	return perpSnapshot{
			accountValue:  0,
			marginUsed:    0,
			unrealizedPnL: 0,
			withdrawable:  "0",
		},
		146.934675,
		xyzDexBalance{AccountValue: 26.7664, UnrealizedPnL: 0.09336, Withdrawable: 0}
}

func TestAssembleBalanceUnifiedDoesNotDoubleCountXYZMargin(t *testing.T) {
	perp, spot, xyz := liveXYZFixture()

	got := assembleBalance(perp, spot, xyz, true, true)

	if math.Abs(got.Equity-146.934675) > 1e-6 {
		t.Fatalf("equity = %.6f, want 146.934675", got.Equity)
	}
	if math.Abs(got.UnrealizedPnL-0.09336) > 1e-6 {
		t.Fatalf("unrealized = %.6f, want 0.09336", got.UnrealizedPnL)
	}
	if math.Abs(got.Wallet-146.841315) > 1e-6 {
		t.Fatalf("wallet = %.6f, want 146.841315", got.Wallet)
	}
	// Unified collateral: the spot balance is usable margin even though the
	// perp sub-account's withdrawable is 0.
	if math.Abs(got.AvailableMargin-146.934675) > 1e-6 {
		t.Fatalf("available = %.6f, want 146.934675", got.AvailableMargin)
	}
	if got.XyzSeparate {
		t.Fatal("unified mode must not mark the xyz dex as a separate pool")
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestAssembleBalanceStandardAddsXYZPool(t *testing.T) {
	perp, spot, xyz := liveXYZFixture()

	got := assembleBalance(perp, spot, xyz, false, true)

	if math.Abs(got.Equity-173.701075) > 1e-6 {
		t.Fatalf("equity = %.6f, want 173.701075", got.Equity)
	}
	if math.Abs(got.Wallet-173.607715) > 1e-6 {
		t.Fatalf("wallet = %.6f, want 173.607715", got.Wallet)
	}
	if !got.XyzSeparate {
		t.Fatal("standard mode must mark the xyz dex as a separate pool")
	}
	if math.Abs(got.XyzEquity-26.7664) > 1e-6 {
		t.Fatalf("xyz equity = %.6f, want 26.7664", got.XyzEquity)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestAssembleBalanceKeepsIdentity(t *testing.T) {
	cases := []struct {
		name    string
		perp    perpSnapshot
		spot    float64
		xyz     xyzDexBalance
		unified bool
	}{
		{"unified live", mustFixture(), 146.934675, xyzDexBalance{AccountValue: 26.7664, UnrealizedPnL: 0.09336}, true},
		{"standard live", perpSnapshot{accountValue: 0}, 146.934675, xyzDexBalance{AccountValue: 26.7664, UnrealizedPnL: 0.09336}, false},
		{"perp open, unified", perpSnapshot{accountValue: 500, marginUsed: 100, unrealizedPnL: -12.5}, 50, xyzDexBalance{}, true},
		{"perp open, standard", perpSnapshot{accountValue: 500, marginUsed: 100, unrealizedPnL: -12.5}, 50, xyzDexBalance{AccountValue: 20, UnrealizedPnL: 2}, false},
		{"flat", perpSnapshot{}, 0, xyzDexBalance{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := assembleBalance(tc.perp, tc.spot, tc.xyz, tc.unified, true)
			if diff := math.Abs(got.Equity - (got.Wallet + got.UnrealizedPnL)); diff > 0.01 {
				t.Fatalf("identity broken: equity=%.6f wallet=%.6f unrealized=%.6f (diff %.6f)",
					got.Equity, got.Wallet, got.UnrealizedPnL, diff)
			}
		})
	}
}

func TestAssembleBalanceAvailableMargin(t *testing.T) {
	t.Run("venue withdrawable wins", func(t *testing.T) {
		got := assembleBalance(perpSnapshot{accountValue: 100, marginUsed: 40, withdrawable: "55.5"}, 0, xyzDexBalance{}, false, true)
		if math.Abs(got.AvailableMargin-55.5) > 1e-6 {
			t.Fatalf("available = %v, want 55.5", got.AvailableMargin)
		}
	})
	t.Run("fallback to equity minus margin", func(t *testing.T) {
		got := assembleBalance(perpSnapshot{accountValue: 100, marginUsed: 40}, 0, xyzDexBalance{}, false, true)
		if math.Abs(got.AvailableMargin-60) > 1e-6 {
			t.Fatalf("available = %v, want 60", got.AvailableMargin)
		}
	})
	t.Run("negative fallback clamps to zero", func(t *testing.T) {
		got := assembleBalance(perpSnapshot{accountValue: 10, marginUsed: 40}, 0, xyzDexBalance{}, false, true)
		if got.AvailableMargin != 0 {
			t.Fatalf("available = %v, want 0", got.AvailableMargin)
		}
	})
}

func TestAssembleBalanceMarksIncomplete(t *testing.T) {
	got := assembleBalance(perpSnapshot{}, 0, xyzDexBalance{}, true, false)
	if got.Complete {
		t.Fatal("expected an incomplete balance when a sub-query failed")
	}
	if got.ToMap()["balance_incomplete"] != true {
		t.Fatal("incomplete flag missing from the compatibility map")
	}
}

func mustFixture() perpSnapshot {
	perp, _, _ := liveXYZFixture()
	return perp
}
