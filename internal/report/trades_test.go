package report

import (
	"math"
	"testing"
	"time"
)

func trade(symbol, side string, hold time.Duration, gross, fees, funding float64, ai bool) Trade {
	const entryMs = int64(1_700_000_000_000)
	return Trade{
		Symbol: symbol, Side: side,
		EntryMs: entryMs, ExitMs: entryMs + hold.Milliseconds(),
		Qty: 10, EntryPx: 100,
		GrossPnL: gross, Fees: fees, Funding: funding, AIInitiated: ai,
	}
}

func TestBuildSplitsBucketsAndCloseKinds(t *testing.T) {
	trades := []Trade{
		trade("A", "LONG", 10*time.Minute, 10, 2, 0, true),   // net +8, <30m, AI
		trade("B", "LONG", 5*time.Hour, -5, 1, 0, false),     // net -6, 2-12h, protective
		trade("C", "LONG", 4*24*time.Hour, 20, 3, 0.5, true), // net +16.5, >3d, AI
		trade("D", "LONG", 0, 0, 0, 0, false),                // net 0, unknown hold
	}

	rep := Build(trades)

	if rep.Overall.Trades != 4 {
		t.Fatalf("trades = %d, want 4", rep.Overall.Trades)
	}
	if !almost(rep.Overall.Gross, 25) || !almost(rep.Overall.Fees, 6) || !almost(rep.Overall.Funding, 0.5) {
		t.Fatalf("gross/fees/funding = %v/%v/%v, want 25/6/0.5", rep.Overall.Gross, rep.Overall.Fees, rep.Overall.Funding)
	}
	if !almost(rep.Overall.Net, 18.5) {
		t.Fatalf("net = %v, want 18.5 (gross - fees - funding)", rep.Overall.Net)
	}
	// Two of four trades end net-positive (the zero-hold one is not a win).
	if !almost(rep.Overall.WinRate, 50) {
		t.Fatalf("win rate = %v, want 50", rep.Overall.WinRate)
	}
	if !almost(rep.Overall.GrossPF, 6.0) {
		t.Fatalf("gross PF = %v, want 6.0 (30 gains / 5 losses)", rep.Overall.GrossPF)
	}
	if !almost(rep.Overall.NetPF, 24.5/6) {
		t.Fatalf("net PF = %v, want %.3f", rep.Overall.NetPF, 24.5/6)
	}
	// Fees / total notional (4 x 1000) = 0.15%.
	if !almost(rep.Overall.FeesPctOfNotional, 0.15) {
		t.Fatalf("fees %% of notional = %v, want 0.15", rep.Overall.FeesPctOfNotional)
	}
	if !almost(rep.Overall.AIShare, 50) {
		t.Fatalf("AI share = %v, want 50", rep.Overall.AIShare)
	}

	// Buckets in band order, with the unknown hold separated instead of being
	// silently counted as a sub-30-minute round trip.
	wantLabels := []string{"<30m", "2-12h", ">3d", "n/a"}
	if len(rep.Buckets) != len(wantLabels) {
		t.Fatalf("buckets = %+v, want labels %v", rep.Buckets, wantLabels)
	}
	for i, want := range wantLabels {
		if rep.Buckets[i].Label != want {
			t.Errorf("bucket %d = %q, want %q", i, rep.Buckets[i].Label, want)
		}
	}

	if rep.AI.Trades != 2 || !almost(rep.AI.Net, 24.5) {
		t.Fatalf("AI closes = %d trades / net %v, want 2 / 24.5", rep.AI.Trades, rep.AI.Net)
	}
	if rep.Protective.Trades != 2 || !almost(rep.Protective.Net, -6) {
		t.Fatalf("protective closes = %d trades / net %v, want 2 / -6", rep.Protective.Trades, rep.Protective.Net)
	}
	// No losing AI trade: profit factor is infinite rather than a huge number.
	if !math.IsInf(rep.AI.NetPF, 1) {
		t.Fatalf("AI net PF = %v, want +Inf", rep.AI.NetPF)
	}
}

func TestProfitFactorEdges(t *testing.T) {
	if pf := profitFactor([]float64{-1, -2}); pf != 0 {
		t.Fatalf("all-loss PF = %v, want 0", pf)
	}
	if pf := profitFactor(nil); pf != 0 {
		t.Fatalf("empty PF = %v, want 0", pf)
	}
	if pf := profitFactor([]float64{2, 4}); !math.IsInf(pf, 1) {
		t.Fatalf("all-win PF = %v, want +Inf", pf)
	}
	if pf := profitFactor([]float64{6, -3}); !almost(pf, 2) {
		t.Fatalf("mixed PF = %v, want 2", pf)
	}
}

func TestEffectiveFeePerLeg(t *testing.T) {
	trades := []Trade{
		trade("A", "LONG", time.Hour, 0, 2, 0, false), // notional 1000 → 0.1% per leg
		trade("B", "LONG", time.Hour, 0, 8, 0, false),
	}
	// 10 USD of fees over 2 trades × 2 legs × 1000 notional = 0.25%.
	if got := EffectiveFeePerLeg(trades); !almost(got, 0.0025) {
		t.Fatalf("fee per leg = %v, want 0.0025", got)
	}
	if got := EffectiveFeePerLeg(nil); got != 0 {
		t.Fatalf("no trades → %v, want 0", got)
	}
}

func TestRandomBaselineMechanics(t *testing.T) {
	// A flat path: random entries earn nothing, so any positive trade return
	// must land at the top of the distribution.
	flat := Series{BarMs: 3_600_000, Closes: make([]float64, 200)}
	for i := range flat.Closes {
		flat.Closes[i] = 100
	}
	trades := []Trade{
		trade("SYM", "LONG", time.Hour, 10, 0, 0, false),
		trade("SYM", "LONG", time.Hour, 10, 0, 0, false),
		trade("MISSING", "LONG", time.Hour, 10, 0, 0, false), // no price path
	}

	base := RandomBaseline(trades, map[string]Series{"SYM": flat}, 50, 7)
	if base.Total != 3 || base.Covered != 2 {
		t.Fatalf("coverage = %d/%d, want 2/3", base.Covered, base.Total)
	}
	if !almost(base.StrategyNetPct, 1.0) {
		t.Fatalf("strategy net/trade = %v%%, want 1.0%%", base.StrategyNetPct)
	}
	if !almost(base.RandomMedian, 0) || !almost(base.RandomP25, 0) || !almost(base.RandomP75, 0) {
		t.Fatalf("random quantiles = %v/%v/%v, want 0", base.RandomP25, base.RandomMedian, base.RandomP75)
	}
	if !almost(base.Percentile, 100) {
		t.Fatalf("percentile = %v, want 100 (every random entry earns 0)", base.Percentile)
	}

	// A losing strategy on the same path lands at the bottom.
	losers := []Trade{trade("SYM", "LONG", time.Hour, -10, 0, 0, false)}
	base = RandomBaseline(losers, map[string]Series{"SYM": flat}, 50, 7)
	if !almost(base.Percentile, 0) {
		t.Fatalf("percentile for a losing strategy = %v, want 0", base.Percentile)
	}

	// No usable path at all: nothing is claimed.
	base = RandomBaseline(trades, nil, 20, 7)
	if base.Covered != 0 || base.StrategyNetPct != 0 || base.Percentile != 0 {
		t.Fatalf("baseline without paths = %+v, want an empty comparison", base)
	}
}

func TestRandomBaselineChargesBothLegs(t *testing.T) {
	flat := Series{BarMs: 3_600_000, Closes: make([]float64, 100)}
	for i := range flat.Closes {
		flat.Closes[i] = 100
	}
	// 2 USD of fees on 1000 notional → 0.1% per leg; a flat path minus two legs
	// gives −0.2% for every random entry.
	trades := []Trade{trade("SYM", "LONG", time.Hour, 0, 2, 0, false)}
	base := RandomBaseline(trades, map[string]Series{"SYM": flat}, 10, 3)
	if !almost(base.FeePerLeg, 0.001) {
		t.Fatalf("fee per leg = %v, want 0.001", base.FeePerLeg)
	}
	if !almost(base.RandomMedian, -0.2) {
		t.Fatalf("random median = %v%%, want -0.2%% (flat path, two fee legs)", base.RandomMedian)
	}
}
