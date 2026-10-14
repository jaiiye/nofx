package market

import (
	"math"
	"testing"
)

func book(bids, asks [][2]float64) *OrderBookDepth {
	b := &OrderBookDepth{Symbol: "BTCUSDT"}
	for _, l := range bids {
		b.Bids = append(b.Bids, OrderBookLevel{Price: l[0], Size: l[1]})
	}
	for _, l := range asks {
		b.Asks = append(b.Asks, OrderBookLevel{Price: l[0], Size: l[1]})
	}
	return b
}

func TestAnalyzeMicrostructureBalanced(t *testing.T) {
	b := book(
		[][2]float64{{99, 10}, {98, 10}, {97, 10}},
		[][2]float64{{101, 10}, {102, 10}, {103, 10}},
	)
	m := AnalyzeMicrostructure(b, nil)
	if m == nil {
		t.Fatal("expected analysis")
	}
	if math.Abs(m.SpreadPct-2.0) > 1e-9 || math.Abs(m.SpreadBps-200) > 1e-9 {
		t.Fatalf("spread = %.4f%% / %.1f bps, want 2%% / 200 bps", m.SpreadPct, m.SpreadBps)
	}
	// Notionals: bids 99+98+97 = 294 × 10, asks 101+102+103 = 306 × 10 —
	// nearly symmetric (imbalance -0.02), inside the BALANCED band.
	if math.Abs(m.Imbalance+0.02) > 1e-9 || m.ImbalanceSide != "BALANCED" {
		t.Fatalf("imbalance = %v (%s), want -0.02 BALANCED", m.Imbalance, m.ImbalanceSide)
	}
	if m.BidDepthUSD != 2940 || m.AskDepthUSD != 3060 {
		t.Fatalf("depth = bid %.0f ask %.0f", m.BidDepthUSD, m.AskDepthUSD)
	}
	// Largest support level below mid is 99 (size 10); largest resistance is 101.
	if len(m.SupportLevels) != 3 || m.SupportLevels[0] != 99 {
		t.Fatalf("support = %v", m.SupportLevels)
	}
	if len(m.ResistanceLevels) != 3 || m.ResistanceLevels[0] != 101 {
		t.Fatalf("resistance = %v", m.ResistanceLevels)
	}
	// 200 bps spread + thin depth => heavily penalized score.
	if m.LiquidityScore > 30 {
		t.Fatalf("liquidity score = %d, want heavily penalized", m.LiquidityScore)
	}
}

func TestAnalyzeMicrostructureImbalance(t *testing.T) {
	b := book(
		[][2]float64{{100, 50}, {99, 50}},
		[][2]float64{{101, 1}, {102, 1}},
	)
	m := AnalyzeMicrostructure(b, nil)
	if m.ImbalanceSide != "BUY" {
		t.Fatalf("imbalance side = %s, want BUY", m.ImbalanceSide)
	}
	if m.Imbalance <= 0.15 {
		t.Fatalf("imbalance = %.3f, want strongly positive", m.Imbalance)
	}
}

func TestAnalyzeMicrostructureVWAP(t *testing.T) {
	// 3 bars with typical price 100/102/104 and volumes 1/1/2:
	// vwap = (100*1 + 102*1 + 104*2)/4 = 102.5
	klines := []KlineBar{
		{High: 100.5, Low: 99.5, Close: 100, Volume: 1},
		{High: 102.5, Low: 101.5, Close: 102, Volume: 1},
		{High: 104.5, Low: 103.5, Close: 104, Volume: 2},
	}
	b := book(
		[][2]float64{{101.9, 1}},
		[][2]float64{{102.1, 1}},
	)
	m := AnalyzeMicrostructure(b, klines)
	if m == nil {
		t.Fatal("expected analysis")
	}
	if math.Abs(m.VWAP-102.5) > 1e-9 {
		t.Fatalf("vwap = %.4f, want 102.5", m.VWAP)
	}
	if math.Abs(m.VWAPDeviationPct-(-0.48780487804878090)) > 1e-6 {
		t.Fatalf("vwap deviation = %.6f", m.VWAPDeviationPct)
	}
}

func TestAnalyzeMicrostructureNilAndDegenerate(t *testing.T) {
	if m := AnalyzeMicrostructure(nil, nil); m != nil {
		t.Fatal("nil book must yield nil analysis")
	}
	if m := AnalyzeMicrostructure(&OrderBookDepth{Symbol: "X"}, nil); m != nil {
		t.Fatal("one-sided book must yield nil analysis")
	}
	// Zero mid (zero prices) must not divide by zero.
	bad := book(
		[][2]float64{{0, 1}},
		[][2]float64{{0, 1}},
	)
	if m := AnalyzeMicrostructure(bad, nil); m != nil {
		t.Fatal("zero-mid book must yield nil analysis")
	}
}

func TestLiquidityScoreTightDeepBook(t *testing.T) {
	// Tight spread, deep book, mild tilt: near-perfect score.
	levels := make([][2]float64, 10)
	for i := range levels {
		levels[i] = [2]float64{100 - float64(i)*0.01, 100}
	}
	askLevels := make([][2]float64, 10)
	for i := range askLevels {
		askLevels[i] = [2]float64{100.01 + float64(i)*0.01, 100}
	}
	m := AnalyzeMicrostructure(book(levels, askLevels), nil)
	if m.LiquidityScore < 80 {
		t.Fatalf("liquidity score = %d, want >= 80 for a tight deep book", m.LiquidityScore)
	}
	if m.SpreadBps > 1.1 {
		t.Fatalf("spread = %.2f bps, want ~1", m.SpreadBps)
	}
}
