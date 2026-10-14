package market

import (
	"math"
	"testing"
)

func makeKlines(n int) []Kline {
	klines := make([]Kline, n)
	for i := 0; i < n; i++ {
		price := 100.0 + float64(i)
		klines[i] = Kline{
			OpenTime: int64(i) * 60_000,
			Open:     price, High: price + 1, Low: price - 1, Close: price,
			Volume: 10, CloseTime: int64(i)*60_000 + 59_999,
		}
	}
	return klines
}

func TestNormalizedIndicatorParams(t *testing.T) {
	// Zero values fall back to the canonical defaults.
	n := (&IndicatorParams{}).normalized()
	d := DefaultIndicatorParams()
	if len(n.EMAPeriods) != len(d.EMAPeriods) || n.MACDFast != 12 || n.MACDSlow != 26 {
		t.Fatalf("zero params must normalize to defaults, got %+v", n)
	}

	// Invalid periods are dropped; valid ones kept.
	n = (&IndicatorParams{EMAPeriods: []int{0, -5, 30}, MACDFast: 26, MACDSlow: 12}).normalized()
	if len(n.EMAPeriods) != 1 || n.EMAPeriods[0] != 30 {
		t.Fatalf("invalid periods must be filtered, got %v", n.EMAPeriods)
	}
	if n.MACDFast != 12 || n.MACDSlow != 26 {
		t.Fatalf("swapped MACD pair must fall back to 12/26, got %d/%d", n.MACDFast, n.MACDSlow)
	}

	// nil stays nil-safe.
	if got := configuredIndicatorSeries(makeKlines(50), nil, 10); got != nil {
		t.Fatalf("nil params must produce no configured series, got %d", len(got))
	}
}

func TestConfiguredIndicatorSeriesLabelsAndValues(t *testing.T) {
	klines := makeKlines(120)
	params := &IndicatorParams{
		EMAPeriods: []int{30, 100},
		RSIPeriods: []int{10},
		ATRPeriods: []int{7},
		MACDFast:   8,
		MACDSlow:   21,
	}
	series := configuredIndicatorSeries(klines, params, 30)

	want := map[string]bool{
		"EMA(30)": false, "EMA(100)": false, "RSI(10)": false,
		"MACD(8,21)": false, "ATR(7)": false,
	}
	for _, s := range series {
		if _, ok := want[s.Label]; !ok {
			t.Fatalf("unexpected series label %q", s.Label)
		}
		want[s.Label] = true
		if len(s.Values) == 0 {
			t.Fatalf("series %s has no values", s.Label)
		}
	}
	for label, found := range want {
		if !found {
			t.Fatalf("missing series %s", label)
		}
	}

	// EMA(30) last value must equal the direct calculation over the same prefix.
	for _, s := range series {
		if s.Label == "EMA(30)" {
			got := s.Values[len(s.Values)-1]
			wantVal := calculateEMA(klines, 30)
			if math.Abs(got-wantVal) > 1e-9 {
				t.Fatalf("EMA(30) last value = %v, want %v", got, wantVal)
			}
		}
	}

	// MACD(8,21) matches the parameterized calculation.
	for _, s := range series {
		if s.Label == "MACD(8,21)" {
			got := s.Values[len(s.Values)-1]
			wantVal := calculateMACDWithPeriods(klines, 8, 21)
			if math.Abs(got-wantVal) > 1e-9 {
				t.Fatalf("MACD(8,21) last value = %v, want %v", got, wantVal)
			}
		}
	}
}

func TestConfiguredSeriesMatchesFixedFieldsAtDefaults(t *testing.T) {
	klines := makeKlines(200)

	// With default periods the configured EMA(20)/EMA(50)/RSI(7)/RSI(14)
	// series must match the fixed fields the legacy prompt path prints.
	series := calculateTimeframeSeries(klines, "5m", 30, DefaultIndicatorParams())
	if len(series.ConfiguredIndicators) == 0 {
		t.Fatalf("default params must still produce configured series")
	}

	find := func(label string) []float64 {
		for _, s := range series.ConfiguredIndicators {
			if s.Label == label {
				return s.Values
			}
		}
		t.Fatalf("missing series %s", label)
		return nil
	}

	if a, b := find("EMA(20)"), series.EMA20Values; len(a) != len(b) {
		t.Fatalf("EMA(20) length %d != fixed %d", len(a), len(b))
	} else {
		for i := range a {
			if math.Abs(a[i]-b[i]) > 1e-9 {
				t.Fatalf("EMA(20)[%d] = %v, fixed %v", i, a[i], b[i])
			}
		}
	}
	if a, b := find("RSI(14)"), series.RSI14Values; len(a) != len(b) {
		t.Fatalf("RSI(14) length %d != fixed %d", len(a), len(b))
	}
}
