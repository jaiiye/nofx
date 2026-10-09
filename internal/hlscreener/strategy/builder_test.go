package strategy

import (
	"strings"
	"testing"
	"time"

	"nofx/internal/hlscreener/store"
)

func TestMarketContext_ToJSON(t *testing.T) {
	mc := &MarketContext{
		GeneratedAt: 1700000000000,
		Universe: []CandidateSummary{
			{
				Coin:    "BTC",
				Score:   85.5,
				InPool:  true,
				Reasons: map[string]any{"liquidity": map[string]any{"pass": true}},
				CVD24h:  1_500_000,
			},
		},
	}
	b, err := mc.ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"BTC"`) {
		t.Error("json missing coin")
	}
	if !strings.Contains(s, `"score": 85.5`) {
		t.Error("json missing score")
	}
	// json may format large ints as 1.5e+06; check both forms
	if !strings.Contains(s, `"cvd_24h": 1500000`) && !strings.Contains(s, `"cvd_24h": 1.5e+06`) {
		t.Errorf("json missing cvd: %s", s)
	}
}

func TestMarketContext_AsPrompt(t *testing.T) {
	mc := &MarketContext{
		GeneratedAt: time.Unix(1700000000, 0).UnixMilli(),
		Universe: []CandidateSummary{
			{
				Coin:   "BTC",
				Score:  85.5,
				InPool: true,
				CVD24h: 1_500_000,
				NearestLiq: []LiqBinSummary{
					{Side: "above", DistancePct: 1.2, NotionalUSD: 12_000_000, BinStart: 61_000, BinEnd: 61_500},
					{Side: "below", DistancePct: 0.5, NotionalUSD: 8_000_000, BinStart: 59_500, BinEnd: 60_000},
				},
			},
			{
				Coin:   "ETH",
				Score:  42.0,
				InPool: false,
			},
		},
	}
	prompt := mc.AsPrompt()
	if !strings.Contains(prompt, "Market context generated") {
		t.Error("prompt missing header")
	}
	if !strings.Contains(prompt, "* BTC") {
		t.Error("prompt should mark in-pool coin with *")
	}
	if !strings.Contains(prompt, "  ETH") {
		t.Error("prompt should mark out-of-pool coin with space")
	}
	if !strings.Contains(prompt, "cvd24h=1500000") {
		t.Error("prompt missing cvd24h")
	}
	if !strings.Contains(prompt, "above  +1.20%") {
		t.Error("prompt missing above liq")
	}
	if !strings.Contains(prompt, "below  +0.50%") {
		t.Error("prompt missing below liq")
	}
}

// TestNearestBins exercises the sort + distance logic with a known
// input. Mark is the densest bin's center; top-1 above and top-1 below
// by notional should be returned (n=1).
func TestNearestBins(t *testing.T) {
	levels := []store.LiqLevel{
		// 100 ± 50 (5M) — below mark after weighting
		{BinStart: 75, BinEnd: 125, NotionalUSD: 5_000_000},
		// 130–180 (1M) — above mark
		{BinStart: 130, BinEnd: 180, NotionalUSD: 1_000_000},
		// 20–70 (3M) — below mark
		{BinStart: 20, BinEnd: 70, NotionalUSD: 3_000_000},
		// 200–250 (8M) — above mark, top by notional
		{BinStart: 200, BinEnd: 250, NotionalUSD: 8_000_000},
	}
	b := NewBuilder(nil)
	out := b.nearestBins(levels, 1)
	if len(out) != 2 {
		t.Fatalf("got %d bins, want 2 (1 above + 1 below)", len(out))
	}
	// Above: 200-250 (8M) is the biggest above
	var above *LiqBinSummary
	var below *LiqBinSummary
	for i := range out {
		switch out[i].Side {
		case "above":
			above = &out[i]
		case "below":
			below = &out[i]
		}
	}
	if above == nil {
		t.Fatal("no 'above' bin returned")
	}
	if above.BinStart != 200 || above.NotionalUSD != 8_000_000 {
		t.Errorf("above = %+v, want BinStart=200 NotionalUSD=8M", above)
	}
	if above.DistancePct < 30 {
		t.Errorf("above distPct = %v, expected > 30%% (mark is weighted centroid ~152)", above.DistancePct)
	}
	if below == nil {
		t.Fatal("no 'below' bin returned")
	}
	// Below: 75-125 (5M) is the biggest below
	if below.BinStart != 75 || below.NotionalUSD != 5_000_000 {
		t.Errorf("below = %+v, want BinStart=75 NotionalUSD=5M", below)
	}
}

func TestNearestBins_Empty(t *testing.T) {
	b := NewBuilder(nil)
	if got := b.nearestBins(nil, 3); got != nil {
		t.Errorf("nil input should return nil, got %v", got)
	}
	if got := b.nearestBins([]store.LiqLevel{}, 3); got != nil {
		t.Errorf("empty input should return nil, got %v", got)
	}
}
