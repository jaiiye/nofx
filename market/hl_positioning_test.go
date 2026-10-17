package market

import (
	"math"
	"sync"
	"testing"
	"time"

	"nofx/provider/hyperliquid"
)

func TestAnnualizedFundingPct(t *testing.T) {
	// 0.0125% per hour == 109.5% APR
	if got := AnnualizedFundingPct(0.000125); math.Abs(got-109.5) > 0.01 {
		t.Fatalf("AnnualizedFundingPct = %v, want 109.5", got)
	}
	if got := AnnualizedFundingPct(0); got != 0 {
		t.Fatalf("zero funding must stay zero, got %v", got)
	}
}

func TestFundingPercentile(t *testing.T) {
	// Too few observations: report unavailable instead of a confident number.
	short := []hyperliquid.FundingPoint{{Rate: 0.0001}, {Rate: 0.0002}}
	if _, samples, ok := FundingPercentile(short, 0.0001, fundingMinSamples); ok || samples != 2 {
		t.Fatalf("short history must be unavailable, got ok=%v samples=%d", ok, samples)
	}

	points := make([]hyperliquid.FundingPoint, 0, 100)
	for i := 0; i < 100; i++ {
		points = append(points, hyperliquid.FundingPoint{TimeMs: int64(i), Rate: float64(i) / 1_000_000})
	}

	// The smallest value sits at the bottom, the largest at the top.
	if pct, _, ok := FundingPercentile(points, 0, fundingMinSamples); !ok || pct > 1 {
		t.Fatalf("minimum funding percentile = %v ok=%v, want ~0", pct, ok)
	}
	if pct, _, ok := FundingPercentile(points, 99.0/1_000_000, fundingMinSamples); !ok || pct < 99 {
		t.Fatalf("maximum funding percentile = %v ok=%v, want ~100", pct, ok)
	}
	// Midpoint lands mid-range.
	if pct, _, ok := FundingPercentile(points, 49.0/1_000_000, fundingMinSamples); !ok || pct < 45 || pct > 55 {
		t.Fatalf("median funding percentile = %v, want ~50", pct)
	}

	// NaN/Inf observations are ignored, and if that leaves too few the reading
	// is unavailable rather than NaN-poisoned.
	poisoned := make([]hyperliquid.FundingPoint, 0, 100)
	for i := 0; i < 100; i++ {
		rate := float64(i) / 1_000_000
		if i%2 == 0 {
			rate = math.NaN()
		}
		poisoned = append(poisoned, hyperliquid.FundingPoint{Rate: rate})
	}
	if _, samples, ok := FundingPercentile(poisoned, 0.00005, fundingMinSamples); ok || samples != 50 {
		t.Fatalf("NaN-filtered samples = %d ok=%v, want 50 samples and unavailable", samples, ok)
	}
}

func TestOIQuadrant(t *testing.T) {
	cases := []struct {
		oi, price float64
		want      string
	}{
		{2, 1, QuadrantNewLongs},
		{2, -1, QuadrantNewShorts},
		{-2, 1, QuadrantShortCovering},
		{-2, -1, QuadrantLongCapitulation},
		{0, 1, ""},
		{0, 0, ""},
	}
	for _, c := range cases {
		if got := OIQuadrant(c.oi, c.price); got != c.want {
			t.Errorf("OIQuadrant(%v,%v) = %q, want %q", c.oi, c.price, got, c.want)
		}
	}
}

// hourlySeries records one sample per hour starting at base.
func hourlySeries(s *OISeries, symbol string, baseMs int64, oi, px []float64) {
	for i := range oi {
		s.Record(symbol, baseMs+int64(i)*int64(time.Hour/time.Millisecond), oi[i], px[i])
	}
}

func TestOISeriesChange(t *testing.T) {
	base := time.Now().UnixMilli()
	now := base + int64(5*time.Hour/time.Millisecond)

	s := NewOISeries()
	// OI 100 -> 150 over six hourly samples, price 50 -> 55.
	hourlySeries(s, "BTCUSDT", base,
		[]float64{100, 110, 120, 130, 140, 150},
		[]float64{50, 51, 52, 53, 54, 55})

	oiPct, pxPct, ok := s.Change("BTCUSDT", time.Hour, now)
	if !ok {
		t.Fatal("1h change must be available with hourly samples")
	}
	// Baseline is the oldest sample still inside the window: 140 -> 150 and
	// 54 -> 55.
	if math.Abs(oiPct-7.142857) > 0.01 || math.Abs(pxPct-1.851851) > 0.01 {
		t.Fatalf("1h change = %v%% / %v%%, want ~7.14%% / ~1.85%%", oiPct, pxPct)
	}

	oiPct, pxPct, ok = s.Change("BTCUSDT", 4*time.Hour, now)
	if !ok {
		t.Fatal("4h change must be available with hourly samples")
	}
	// The 4h window reaches back to base+1h: 110 -> 150 and 51 -> 55.
	if math.Abs(oiPct-36.363636) > 0.01 || math.Abs(pxPct-7.843137) > 0.01 {
		t.Fatalf("4h change = %v%% / %v%%, want ~36.36%% / ~7.84%%", oiPct, pxPct)
	}

	// Unknown symbol: unavailable, not zero.
	if _, _, ok := s.Change("NOPEUSDT", time.Hour, now); ok {
		t.Fatal("unknown symbol must be unavailable")
	}

	// A fresh series with only a short span must not claim an hourly change.
	fresh := NewOISeries()
	fresh.Record("SOLUSDT", base, 100, 10)
	fresh.Record("SOLUSDT", base+int64(40*time.Minute/time.Millisecond), 130, 11)
	if _, _, ok := fresh.Change("SOLUSDT", 4*time.Hour, base+int64(40*time.Minute/time.Millisecond)); ok {
		t.Fatal("40 minutes of samples must not be reported as a 4h change")
	}

	// Degenerate inputs are rejected.
	empty := NewOISeries()
	if _, _, ok := empty.Change("BTCUSDT", time.Hour, now); ok {
		t.Fatal("empty series must be unavailable")
	}
	if _, _, ok := s.Change("BTCUSDT", 0, now); ok {
		t.Fatal("zero window must be unavailable")
	}
}

func TestOISeriesRejectsDenseSamples(t *testing.T) {
	base := time.Now().UnixMilli()
	s := NewOISeries()
	s.Record("BTCUSDT", base, 100, 50)
	// Same cycle, data assembled again a minute later: must not add a sample.
	s.Record("BTCUSDT", base+60_000, 101, 50.5)

	s.mu.Lock()
	count := len(s.bySymbol["BTCUSDT"])
	s.mu.Unlock()
	if count != 1 {
		t.Fatalf("dense samples must be skipped, got %d samples", count)
	}
}

func TestFlowWindowReading(t *testing.T) {
	now := time.Now().UnixMilli()
	f := NewFlowWindow(5) // small threshold keeps the test focused

	// Too few prints: unavailable.
	f.AddTaker("BTCUSDT", now-60_000, true, 1000)
	if _, ok := f.Reading("BTCUSDT", time.Hour, now); ok {
		t.Fatal("a single print must not produce a flow ratio")
	}

	// Buys 3000 vs sells 1000 -> 3x.
	f.AddTaker("BTCUSDT", now-120_000, true, 2000)
	f.AddTaker("BTCUSDT", now-180_000, false, 1000)
	f.AddTaker("BTCUSDT", now-240_000, true, 500)
	f.AddTaker("BTCUSDT", now-300_000, false, 500)

	reading, ok := f.Reading("BTCUSDT", time.Hour, now)
	if !ok {
		t.Fatal("flow ratio must be available with enough prints")
	}
	if reading.BuyUSD != 3500 || reading.SellUSD != 1500 {
		t.Fatalf("flow sums = buy %v sell %v, want 3500 / 1500", reading.BuyUSD, reading.SellUSD)
	}
	if math.Abs(reading.Ratio-3500.0/1500.0) > 1e-9 || reading.Samples != 5 {
		t.Fatalf("flow ratio = %v samples=%d, want 2.33 / 5", reading.Ratio, reading.Samples)
	}

	// Prints older than the window are excluded.
	if _, ok := f.Reading("BTCUSDT", 30*time.Second, now); ok {
		t.Fatal("expired prints must fall out of a short window")
	}

	// No prints at all: unavailable.
	if _, ok := f.Reading("ETHUSDT", time.Hour, now); ok {
		t.Fatal("symbol without prints must be unavailable")
	}

	// Health reporting.
	if age := f.LastPrintAge("BTCUSDT", now); age <= 0 || age > time.Minute {
		t.Fatalf("LastPrintAge = %v, want ~1m", age)
	}
	if age := f.LastPrintAge("ETHUSDT", now); age != 0 {
		t.Fatalf("LastPrintAge for an unseen symbol = %v, want 0", age)
	}
}

// All-sell flow must not divide by zero, and concurrency (the websocket
// callbacks) must not corrupt the window.
func TestFlowWindowEdgeCasesAndConcurrency(t *testing.T) {
	now := time.Now().UnixMilli()

	allBuy := NewFlowWindow(2)
	allBuy.AddTaker("BTCUSDT", now, true, 100)
	allBuy.AddTaker("BTCUSDT", now, true, 200)
	reading, ok := allBuy.Reading("BTCUSDT", time.Hour, now)
	if !ok || !math.IsInf(reading.Ratio, 1) {
		t.Fatalf("all-buy flow must report an infinite ratio, got %v ok=%v", reading.Ratio, ok)
	}

	// All-sell flow is a valid (extreme) reading: ratio 0 with zero buy notional,
	// distinct from "unknown" which is reported as unavailable.
	allSell := NewFlowWindow(2)
	allSell.AddTaker("BTCUSDT", now, false, 100)
	allSell.AddTaker("BTCUSDT", now, false, 200)
	sellReading, ok := allSell.Reading("BTCUSDT", time.Hour, now)
	if !ok || sellReading.Ratio != 0 || sellReading.BuyUSD != 0 {
		t.Fatalf("all-sell flow = %+v ok=%v, want ratio 0 with no buy notional", sellReading, ok)
	}

	concurrent := NewFlowWindow(2)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				concurrent.AddTaker("SOLUSDT", now, i%2 == 0, 10)
			}
		}(i)
	}
	wg.Wait()

	reading, ok = concurrent.Reading("SOLUSDT", time.Hour, now)
	if !ok {
		t.Fatal("concurrent fills must still produce a reading")
	}
	if reading.Samples != 400 {
		t.Fatalf("concurrent samples = %d, want 400", reading.Samples)
	}
	if math.Abs(reading.Ratio-1) > 1e-9 {
		t.Fatalf("balanced concurrent flow ratio = %v, want 1", reading.Ratio)
	}
}

func TestTakerFlowRatioThresholds(t *testing.T) {
	if _, ok := TakerFlowRatio(100, 100, 29, 30); ok {
		t.Fatal("below the sample threshold the ratio must be unavailable")
	}
	if ratio, ok := TakerFlowRatio(100, 50, 30, 30); !ok || math.Abs(ratio-2) > 1e-9 {
		t.Fatalf("ratio = %v ok=%v, want 2", ratio, ok)
	}
	if _, ok := TakerFlowRatio(0, 0, 30, 30); ok {
		t.Fatal("no notional must not report a ratio")
	}
}

func TestVenueCoinName(t *testing.T) {
	cases := map[string]string{
		"BTCUSDT":    "BTC",
		"ETHUSDC":    "ETH",
		"solusdt":    "sol",
		"xyz:SILVER": "xyz:SILVER",
		"BTC":        "BTC",
		"":           "",
	}
	for in, want := range cases {
		if got := venueCoinName(in); got != want {
			t.Errorf("venueCoinName(%q) = %q, want %q", in, got, want)
		}
	}
}
