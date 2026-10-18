package trader

import (
	"math"
	"strings"
	"testing"

	"nofx/kernel"
	"nofx/market"
	"nofx/store"
)

func TestPearsonCorrelation(t *testing.T) {
	a := []float64{1, 2, 3, 4, 5}
	if rho := pearsonCorrelation(a, a); math.Abs(rho-1) > 1e-9 {
		t.Fatalf("identical series: rho = %v, want 1", rho)
	}

	neg := []float64{-1, -2, -3, -4, -5}
	if rho := pearsonCorrelation(a, neg); math.Abs(rho+1) > 1e-9 {
		t.Fatalf("inverted series: rho = %v, want -1", rho)
	}

	// A flat series has no variance: report 0 instead of NaN.
	flat := []float64{3, 3, 3, 3, 3}
	if rho := pearsonCorrelation(a, flat); rho != 0 {
		t.Fatalf("flat series: rho = %v, want 0", rho)
	}
	if rho := pearsonCorrelation(a, []float64{1, 2}); rho != 0 {
		t.Fatalf("length mismatch: rho = %v, want 0", rho)
	}
	if rho := pearsonCorrelation(nil, nil); rho != 0 {
		t.Fatalf("empty series: rho = %v, want 0", rho)
	}
}

func TestCorrelationOfClosesAlignsTheTail(t *testing.T) {
	// Two paths of different lengths but the same recent shape: the alignment
	// must use the common tail, not the heads.
	short := make([]float64, 40)
	long := make([]float64, 80)
	for i := range short {
		short[i] = 100 + math.Sin(float64(i)*0.4)*5
	}
	for i := range long {
		if i < len(long)-len(short) {
			long[i] = 200 // old, unrelated history
			continue
		}
		long[i] = 100 + math.Sin(float64(i-len(long)+len(short))*0.4)*5
	}

	rho, ok := correlationOfCloses(short, long)
	if !ok {
		t.Fatal("expected a usable correlation")
	}
	if rho < 0.99 {
		t.Fatalf("tail-aligned rho = %v, want ~1", rho)
	}

	// Not enough history: refuse rather than guess.
	if _, ok := correlationOfCloses(make([]float64, 10), make([]float64, 10)); ok {
		t.Fatal("short series must not produce a correlation")
	}
}

func TestCorrelatedClusterCount(t *testing.T) {
	candidate := make([]float64, 60)
	same := make([]float64, 60)
	inverse := make([]float64, 60)
	unrelated := make([]float64, 60)
	for i := 0; i < 60; i++ {
		candidate[i] = 100 + math.Sin(float64(i)*0.5)*3
		same[i] = 100 + math.Sin(float64(i)*0.5)*3
		inverse[i] = 100 - math.Sin(float64(i)*0.5)*3
		unrelated[i] = 100 + math.Cos(float64(i)*2.7)*3
	}

	count, worst, rho := correlatedClusterCount(candidate, map[string][]float64{
		"AAA": same,
		"BBB": inverse,
		"CCC": unrelated,
	}, 0.8)
	if count != 1 || worst != "AAA" || rho < 0.99 {
		t.Fatalf("cluster = %d/%q/%v, want 1/AAA/~1", count, worst, rho)
	}

	// Nothing above the threshold: no cluster, no worst name.
	count, worst, _ = correlatedClusterCount(candidate, map[string][]float64{"BBB": inverse}, 0.8)
	if count != 0 || worst != "" {
		t.Fatalf("inverse-only cluster = %d/%q, want 0/empty", count, worst)
	}
}

func TestCorrelatedExposureCapSettings(t *testing.T) {
	var rc store.RiskControlConfig
	enabled, cap, threshold := rc.CorrelatedExposureCap()
	if !enabled || cap != store.DefaultMaxCorrelatedPositions || threshold != store.DefaultCorrelationThreshold {
		t.Fatalf("defaults = %v/%d/%.2f, want true/%d/%.2f", enabled, cap, threshold,
			store.DefaultMaxCorrelatedPositions, store.DefaultCorrelationThreshold)
	}

	rc = store.RiskControlConfig{MaxCorrelatedPositions: -1}
	if enabled, _, _ := rc.CorrelatedExposureCap(); enabled {
		t.Fatal("a negative cap must disable the check")
	}

	rc = store.RiskControlConfig{MaxCorrelatedPositions: 99, CorrelationThreshold: 0.1}
	enabled, cap, threshold = rc.CorrelatedExposureCap()
	if !enabled || cap != store.MaxPositions || threshold != store.MinCorrelationThreshold {
		t.Fatalf("clamped = %v/%d/%.2f, want true/%d/%.2f", enabled, cap, threshold,
			store.MaxPositions, store.MinCorrelationThreshold)
	}
}

func TestCheckCorrelatedExposure(t *testing.T) {
	cfg := store.GetDefaultStrategyConfig("en")
	cfg.Indicators.Klines.PrimaryTimeframe = "1h"
	cfg.RiskControl.MaxCorrelatedPositions = 1 // one position per cluster
	at := &AutoTrader{config: AutoTraderConfig{StrategyConfig: &cfg}}

	series := func(phase float64) []market.KlineBar {
		out := make([]market.KlineBar, 0, 60)
		for i := 0; i < 60; i++ {
			out = append(out, market.KlineBar{Close: 100 + math.Sin(float64(i)*0.5+phase)*3})
		}
		return out
	}
	ctx := &kernel.Context{
		MarketDataMap: map[string]*market.Data{
			"AAA": {Symbol: "AAA", TimeframeData: map[string]*market.TimeframeSeriesData{"1h": {Klines: series(0)}}},
			"BBB": {Symbol: "BBB", TimeframeData: map[string]*market.TimeframeSeriesData{"1h": {Klines: series(0)}}},
		},
		Positions: []kernel.PositionInfo{{Symbol: "AAA", Side: "long"}},
	}

	err := at.checkCorrelatedExposure(ctx, "BBB", "open_long")
	if err == nil {
		t.Fatal("a second same-side position at rho≈1 must be refused when the cap is 1")
	}
	for _, want := range []string{"too correlated", "AAA", "cap is 1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("rejection missing %q: %v", want, err)
		}
	}

	// The opposite side is a different bet.
	if err := at.checkCorrelatedExposure(ctx, "BBB", "open_short"); err != nil {
		t.Fatalf("opposite side must not be blocked: %v", err)
	}

	// A cap of 2 allows the pair.
	cfg.RiskControl.MaxCorrelatedPositions = 2
	at.config.StrategyConfig = &cfg
	if err := at.checkCorrelatedExposure(ctx, "BBB", "open_long"); err != nil {
		t.Fatalf("cap 2 must allow the second position: %v", err)
	}

	// Disabled check, missing context and missing candles all fail open.
	cfg.RiskControl.MaxCorrelatedPositions = -1
	at.config.StrategyConfig = &cfg
	if err := at.checkCorrelatedExposure(ctx, "BBB", "open_long"); err != nil {
		t.Fatalf("disabled check must allow: %v", err)
	}
	cfg.RiskControl.MaxCorrelatedPositions = 1
	at.config.StrategyConfig = &cfg
	if err := at.checkCorrelatedExposure(nil, "BBB", "open_long"); err != nil {
		t.Fatalf("nil context must fail open: %v", err)
	}
	empty := &kernel.Context{MarketDataMap: map[string]*market.Data{}}
	if err := at.checkCorrelatedExposure(empty, "BBB", "open_long"); err != nil {
		t.Fatalf("missing candles must fail open: %v", err)
	}
}
