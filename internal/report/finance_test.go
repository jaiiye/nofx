package report

import (
	"math"
	"testing"
	"time"

	"nofx/store"
)

func almost(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestDailyEquityResamplesToLastPerDay(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	snaps := []*store.EquitySnapshot{
		{Timestamp: base.Add(1 * time.Hour), TotalEquity: 1000},
		{Timestamp: base.Add(20 * time.Hour), TotalEquity: 1010}, // last of day 1
		{Timestamp: base.Add(25 * time.Hour), TotalEquity: 1020}, // day 2
	}
	got := DailyEquity(snaps)
	want := []float64{1010, 1020}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestReturnsAndRiskRatios(t *testing.T) {
	equities := []float64{100, 110, 99, 108.9}
	returns := DailyReturns(equities)
	if len(returns) != 3 {
		t.Fatalf("returns len = %d, want 3", len(returns))
	}
	if !almost(returns[0], 0.1) {
		t.Fatalf("returns[0] = %v, want 0.1", returns[0])
	}

	// Sharpe/Sortino of a constant series are 0 (no dispersion).
	if s := Sharpe([]float64{0.01, 0.01}, 365); s != 0 {
		t.Fatalf("Sharpe constant = %v, want 0", s)
	}
	if s := Sortino(returns, 365); s == 0 {
		t.Fatalf("Sortino should be non-zero with downside present")
	}
}

func TestMaxDrawdownPct(t *testing.T) {
	// Peak 120 -> trough 60 is a 50% drawdown.
	if got := MaxDrawdownPct([]float64{100, 120, 60, 90}); !almost(got, 50) {
		t.Fatalf("MaxDrawdownPct = %v, want 50", got)
	}
	if got := MaxDrawdownPct(nil); got != 0 {
		t.Fatalf("MaxDrawdownPct(nil) = %v, want 0", got)
	}
}

func TestAnnualizedReturnPct(t *testing.T) {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	// Doubling over exactly one year -> ~100% CAGR.
	snaps := []*store.EquitySnapshot{
		{Timestamp: start, TotalEquity: 1000},
		{Timestamp: start.AddDate(1, 0, 0), TotalEquity: 2000},
	}
	got := AnnualizedReturnPct(snaps)
	if math.Abs(got-100) > 1 {
		t.Fatalf("AnnualizedReturnPct = %v, want ~100", got)
	}
}
