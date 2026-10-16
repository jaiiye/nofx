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
		"ADX(14)": false, "DI+(14)": false, "DI-(14)": false,
		"BOLL_UPPER(20,2.0)": false, "BOLL_MIDDLE(20,2.0)": false, "BOLL_LOWER(20,2.0)": false,
		"SUPERTREND(10,3.0)": false, "SUPERTREND_DIR(10,3.0)": false,
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

// trendingKlines builds a clean uptrend then a clean downtrend leg.
func trendingKlines(up, down int) []Kline {
	klines := make([]Kline, 0, up+down)
	price := 100.0
	for i := 0; i < up; i++ {
		price += 1
		klines = append(klines, Kline{OpenTime: int64(i) * 60_000, Open: price - 1, High: price + 0.2, Low: price - 1.2, Close: price, Volume: 10})
	}
	for i := 0; i < down; i++ {
		price -= 1
		klines = append(klines, Kline{OpenTime: int64(up+i) * 60_000, Open: price + 1, High: price + 1.2, Low: price - 0.2, Close: price, Volume: 10})
	}
	return klines
}

// choppyKlines oscillates tightly around a level.
func choppyKlines(n int) []Kline {
	klines := make([]Kline, n)
	for i := 0; i < n; i++ {
		price := 100.0
		if i%2 == 0 {
			price = 100.2
		}
		klines[i] = Kline{OpenTime: int64(i) * 60_000, Open: 100, High: price + 0.05, Low: price - 0.05, Close: price, Volume: 10}
	}
	return klines
}

func TestCalculateADXTrendVsChop(t *testing.T) {
	trending := trendingKlines(60, 0) // pure uptrend
	adx, plusDI, minusDI := calculateADX(trending, 14)
	if adx < 40 {
		t.Fatalf("uptrend ADX = %.1f, want a strong trend reading (>=40)", adx)
	}
	if plusDI <= minusDI {
		t.Fatalf("uptrend DI: +DI %.1f must exceed -DI %.1f", plusDI, minusDI)
	}

	chop := choppyKlines(80)
	chopADX, _, _ := calculateADX(chop, 14)
	if chopADX >= adx {
		t.Fatalf("chop ADX %.1f should be below trend ADX %.1f", chopADX, adx)
	}

	// Insufficient history degrades to zeros rather than garbage.
	if a, p, m := calculateADX(trendingKlines(10, 0), 14); a != 0 || p != 0 || m != 0 {
		t.Fatalf("short series must return zeros, got %.2f %.2f %.2f", a, p, m)
	}
}

func TestCalculateSupertrendDirection(t *testing.T) {
	up := trendingKlines(60, 0)
	band, dir := calculateSupertrend(up, 10, 3.0)
	if dir != 1 {
		t.Fatalf("uptrend direction = %d, want +1", dir)
	}
	if band <= 0 || band >= up[len(up)-1].Close {
		t.Fatalf("uptrend band = %.2f, want below the last close %.2f", band, up[len(up)-1].Close)
	}

	down := trendingKlines(20, 40) // trend flips down
	band, dir = calculateSupertrend(down, 10, 3.0)
	if dir != -1 {
		t.Fatalf("downtrend direction = %d, want -1", dir)
	}
	if band <= down[len(down)-1].Close {
		t.Fatalf("downtrend band = %.2f, want above the last close %.2f", band, down[len(down)-1].Close)
	}
}

func TestConfiguredBOLLUsesConfiguredPeriodAndMultiplier(t *testing.T) {
	klines := makeKlines(120)
	series := configuredIndicatorSeries(klines, &IndicatorParams{
		BOLLPeriods:    []int{30},
		BOLLMultiplier: 2.5,
	}, 30)

	var upper, middle, lower []float64
	for _, s := range series {
		switch s.Label {
		case "BOLL_UPPER(30,2.5)":
			upper = s.Values
		case "BOLL_MIDDLE(30,2.5)":
			middle = s.Values
		case "BOLL_LOWER(30,2.5)":
			lower = s.Values
		}
	}
	if upper == nil || middle == nil || lower == nil {
		t.Fatalf("missing configured BOLL series: %v", series)
	}

	wantU, wantM, wantL := calculateBOLL(klines, 30, 2.5)
	if got := middle[len(middle)-1]; math.Abs(got-wantM) > 1e-9 {
		t.Fatalf("BOLL middle = %v, want %v", got, wantM)
	}
	if got := upper[len(upper)-1]; math.Abs(got-wantU) > 1e-9 {
		t.Fatalf("BOLL upper = %v, want %v", got, wantU)
	}
	if got := lower[len(lower)-1]; math.Abs(got-wantL) > 1e-9 {
		t.Fatalf("BOLL lower = %v, want %v", got, wantL)
	}
}

func TestConfiguredADXAndSupertrendSeriesEmitted(t *testing.T) {
	klines := trendingKlines(80, 0)
	series := configuredIndicatorSeries(klines, DefaultIndicatorParams(), 20)

	labels := map[string]bool{}
	for _, s := range series {
		labels[s.Label] = true
	}
	for _, want := range []string{"ADX(14)", "DI+(14)", "DI-(14)", "SUPERTREND(10,3.0)", "SUPERTREND_DIR(10,3.0)"} {
		if !labels[want] {
			t.Fatalf("missing series %s (have %v)", want, labels)
		}
	}
}

// A period longer than the available window used to reach the model as a
// fabricated 0 (e.g. "ATR(300): 0.0000"), which reads as "zero volatility / no
// trend" instead of "not computable". Those series must be omitted.
func TestConfiguredIndicatorSeriesOmitsUnavailablePeriods(t *testing.T) {
	klines := makeKlines(120) // window shorter than the periods below
	series := configuredIndicatorSeries(klines, &IndicatorParams{
		EMAPeriods:       []int{30, 200},
		RSIPeriods:       []int{14, 300},
		ATRPeriods:       []int{7, 300},
		ADXPeriods:       []int{14, 100},
		BOLLPeriods:      []int{20, 400},
		SupertrendPeriod: 300,
	}, 30)

	labels := map[string]bool{}
	for _, s := range series {
		labels[s.Label] = true
		if len(s.Values) == 0 {
			t.Fatalf("series %s emitted with no values", s.Label)
		}
	}

	for _, want := range []string{"EMA(30)", "RSI(14)", "ATR(7)", "ADX(14)", "BOLL_UPPER(20,2.0)"} {
		if !labels[want] {
			t.Errorf("computable series %s missing (have %v)", want, labels)
		}
	}
	for _, bad := range []string{
		"EMA(200)", "RSI(300)", "ATR(300)", "ADX(100)", "DI+(100)",
		"BOLL_UPPER(400,2.0)", "SUPERTREND(300,3.0)", "SUPERTREND_DIR(300,3.0)",
	} {
		if labels[bad] {
			t.Errorf("series %s cannot be computed from %d bars but was emitted", bad, len(klines))
		}
	}

	// Boundary: exactly enough history must still produce a value.
	exact := configuredIndicatorSeries(makeKlines(200), &IndicatorParams{EMAPeriods: []int{200}}, 30)
	found := false
	for _, s := range exact {
		if s.Label == "EMA(200)" && len(s.Values) == 1 && s.Values[0] != 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("EMA(200) over a 200-bar window must emit exactly one value, got %v", exact)
	}
}
