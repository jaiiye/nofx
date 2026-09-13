package market

import (
	"math"
	"testing"

	"nofx/store"
)

// trendingKlines builds a synthetic series with a constant per-bar step. A
// perfectly monotonic series is the cleanest way to assert ADX behaviour: the
// +DI/-DI gap stays wide, so ADX should climb toward its 100 ceiling instead of
// sitting near a neutral reading.
func trendingKlines(n int, start, step float64) []Kline {
	klines := make([]Kline, n)
	price := start
	for i := 0; i < n; i++ {
		open := price
		closePrice := price + step
		klines[i] = Kline{
			OpenTime: int64(i),
			Open:     open,
			High:     math.Max(open, closePrice) + math.Abs(step)*0.1,
			Low:      math.Min(open, closePrice) - math.Abs(step)*0.1,
			Close:    closePrice,
			Volume:   1000,
		}
		price = closePrice
	}
	return klines
}

// flatKlines builds a series with no net movement. Used to confirm ADX reacts
// to trend absence rather than merely to the presence of data.
func flatKlines(n int, price float64) []Kline {
	klines := make([]Kline, n)
	for i := 0; i < n; i++ {
		klines[i] = Kline{
			OpenTime: int64(i),
			Open:     price,
			High:     price,
			Low:      price,
			Close:    price,
			Volume:   1000,
		}
	}
	return klines
}

func TestCalculateADXRequiresEnoughData(t *testing.T) {
	// The seeding needs 2*period+1 bars, so anything shorter must report "no
	// reading" instead of a misleading number.
	short := trendingKlines(20, 100, 1)
	if got := calculateADX(short, 14); got != 0 {
		t.Fatalf("ADX on 20 bars with period 14 = %v, want 0 (insufficient data)", got)
	}

	if got := calculateADX(trendingKlines(100, 100, 1), 0); got != 0 {
		t.Fatalf("ADX with period 0 = %v, want 0", got)
	}
}

// TestCalculateEMAReturnsZeroBelowPeriod pins the contract the entry
// gates depend on: a short series must report 0, never a partial average.
//
// kernel.hasTrendData() uses `EMA200 > 0` to tell "still warming up" apart from
// "trend confirmed". If this ever returned a real number for a short series,
// the trend gate would compare against a bogus line and silently reject valid
// entries instead of skipping the check.
func TestCalculateEMAReturnsZeroBelowPeriod(t *testing.T) {
	for _, n := range []int{0, 1, 10, 50, 100, 150, 199} {
		got := calculateEMA(trendingKlines(n, 100, 1), 200)
		if got != 0 {
			t.Fatalf("EMA200 on %d bars = %v, want 0 (period not yet satisfied)", n, got)
		}
	}
}

// TestCalculateEMAIsExactAtPeriod documents that 200 bars is the threshold, not
// a comfortable margin. Exactly at the period the value is the SMA seed, which
// is why KlineFetchLimit must stay above 200.
func TestCalculateEMAIsExactAtPeriod(t *testing.T) {
	// trendingKlines(200, 100, 1) closes run 101..300; the SMA seed is the mean
	// of the first 200, i.e. 200.5. At exactly period bars the EMA is the seed.
	atPeriod := calculateEMA(trendingKlines(200, 100, 1), 200)
	if atPeriod != 200.5 {
		t.Fatalf("EMA200 on exactly 200 bars = %v, want the SMA seed 200.5", atPeriod)
	}

	// One more bar advances the EMA by the smoothing step, proving the series
	// length is what gates the value.
	beyond := calculateEMA(trendingKlines(201, 100, 1), 200)
	if beyond <= atPeriod {
		t.Fatalf("EMA200 on 201 bars = %v, want it above the %v seed", beyond, atPeriod)
	}
}

// TestKlineFetchLimitCoversEMA200 guards the relationship that makes the trend
// gate usable at all. A fetch of exactly ema200Period leaves no headroom for a
// partially formed candle, which is what KlineFetchLimit exists to avoid.
func TestKlineFetchLimitCoversEMA200(t *testing.T) {
	if KlineFetchLimit <= ema200Period {
		t.Fatalf("KlineFetchLimit (%d) must exceed ema200Period (%d) so EMA200 is available on the latest bar",
			KlineFetchLimit, ema200Period)
	}

	// The fetched window must still produce a usable EMA200 once trimmed to
	// ema200Period bars, which is the situation on the exchange's most recent
	// (possibly still-forming) candle.
	trimmed := trendingKlines(KlineFetchLimit, 100, 1)
	if got := calculateEMA(trimmed[:ema200Period], ema200Period); got == 0 {
		t.Fatal("EMA200 must be available within the fetched window")
	}
}

func TestCalculateADXStaysInRangeAndDetectsTrend(t *testing.T) {
	trending := trendingKlines(120, 100, 1)
	got := calculateADX(trending, 14)

	if got <= 0 || got > 100 {
		t.Fatalf("ADX on a trending series = %v, want within (0, 100]", got)
	}
	// A monotonic series leaves one directional side dominant, so a healthy
	// reading should sit well above the neutral band.
	if got < 50 {
		t.Fatalf("ADX on a clean uptrend = %.2f, expected a strong reading (>= 50)", got)
	}
}

func TestCalculateADXFlatSeriesReadsLow(t *testing.T) {
	// No movement at all: TR is zero, so DX is zero and ADX must follow.
	flat := flatKlines(120, 100)
	if got := calculateADX(flat, 14); got != 0 {
		t.Fatalf("ADX on a flat series = %v, want 0", got)
	}
}

// TestCalculateADXMatchesReferenceImplementation pins the ADX result against a
// separately written Wilder implementation. The synthetic series is a two-sided
// random walk so the reading lands mid-range (rather than saturating at 100,
// which a monotonic series does and which would mask sign or smoothing errors).
//
// The expected value was produced by an independent Python implementation and
// rounded from 4-decimal inputs, so a tolerance rather than equality is used.
func TestCalculateADXMatchesReferenceImplementation(t *testing.T) {
	closes := []float64{
		99.5191, 99.2658, 99.22, 99.7492, 100.692, 99.3576, 98.9959, 99.0803,
		98.2432, 98.0815, 97.7436, 97.8318, 97.3333, 97.7457, 97.8624, 97.3593,
		97.2721, 97.5574, 98.6191, 98.8764, 98.4922, 98.2625, 98.2221, 99.1562,
		99.7175, 99.4434, 98.9656, 99.0642, 99.2802, 100.0526, 99.8969, 99.5726,
		98.939, 98.824, 98.0951, 97.5392, 97.5114, 97.1768, 97.4724, 96.5721,
		97.0963, 97.3685, 96.9832, 97.7932, 97.8134, 97.6662, 97.5134, 97.122,
		96.805, 96.7513, 96.8803, 96.8276, 97.2482, 97.9466, 97.631, 97.9414,
		98.0115, 98.4501, 99.0602, 98.8108, 98.7118, 98.6512, 99.3235, 98.4301,
		98.6124, 98.7446, 99.2941, 99.0272, 99.5337, 99.5452, 99.6013, 100.4591,
		101.3203, 101.2687, 101.4001, 101.6099, 102.881, 102.8612, 102.7936, 102.5994,
		102.4845, 102.5103, 103.6858, 103.8797, 104.5434, 104.9452, 104.5847, 104.9239,
		105.2092, 104.9376, 104.4767, 104.1847, 103.9217, 103.3418, 102.7597, 102.7331,
		102.5591, 102.5694, 103.0421, 102.8026, 102.988, 103.2619, 102.9328, 102.8799,
		103.018, 102.8254, 102.8302, 102.6859, 102.3516, 102.4102, 103.2891, 102.2503,
		102.4867, 103.2239, 103.8303, 104.6167, 104.0766, 104.3627, 104.7426, 105.3669,
		104.4199, 104.2373, 105.3675, 105.6889, 104.7835, 104.3173, 104.1449, 103.6803,
		103.0244, 104.3102, 105.5122, 106.004, 106.0266, 106.0491, 106.5086, 106.5035,
		106.3572, 106.5087, 107.1101, 106.9502, 106.8447, 107.1272, 106.8465, 107.5289,
		108.403, 107.3546, 107.7023, 106.6497, 106.964, 106.5309,
	}
	highs := []float64{
		100.3255, 99.5481, 99.3008, 99.8111, 100.9805, 101.1212, 99.5118, 99.3997,
		99.1101, 98.4003, 98.4787, 98.0944, 98.3219, 97.8217, 98.2447, 98.2101,
		97.7793, 97.5877, 99.03, 98.8877, 98.9059, 98.6877, 98.5372, 99.2954,
		100.1964, 99.8342, 99.4454, 99.5407, 99.6183, 100.4899, 100.1044, 100.0012,
		99.5727, 98.9518, 98.9501, 98.5196, 97.5822, 97.9259, 97.7366, 97.7365,
		97.2269, 97.6348, 97.7743, 98.2024, 97.9912, 97.943, 98.1348, 97.6237,
		97.434, 97.1315, 97.3352, 96.9696, 97.734, 98.309, 98.3991, 98.4316,
		98.077, 98.7134, 99.4733, 99.1805, 98.8764, 99.0035, 99.5744, 99.5436,
		98.6986, 98.9076, 99.3471, 99.6802, 99.9899, 99.8013, 99.8403, 100.9302,
		101.7403, 101.3566, 101.792, 101.94, 102.9908, 103.376, 103.119, 103.1547,
		102.6085, 102.5424, 103.7381, 104.0149, 104.9529, 105.2305, 105.2893, 105.2411,
		105.2426, 105.4858, 105.201, 104.5019, 104.5644, 104.0952, 103.7084, 103.227,
		102.9806, 102.9133, 103.3955, 103.0693, 103.1158, 103.6972, 103.4085, 103.0645,
		103.1402, 103.0185, 102.9307, 102.8751, 102.838, 102.7854, 103.4838, 103.6511,
		102.9327, 103.2936, 104.2327, 104.9581, 104.6832, 104.642, 104.9873, 105.6184,
		105.7353, 104.7845, 105.6144, 106.0723, 105.7626, 105.0674, 104.6533, 104.4032,
		104.1271, 104.319, 105.737, 106.1093, 106.503, 106.4925, 106.7516, 106.7339,
		106.6615, 106.9282, 107.5609, 107.6095, 107.0878, 107.27, 107.3827, 107.971,
		108.8733, 108.7691, 107.8454, 107.9384, 107.4521, 107.2426,
	}
	lows := []float64{
		99.4829, 99.0121, 99.1747, 99.1084, 99.5508, 99.2127, 98.5878, 98.8097,
		98.1402, 97.7887, 97.3941, 97.306, 97.2743, 97.0889, 97.4592, 97.0621,
		96.7998, 96.9214, 97.4151, 98.3882, 98.1081, 97.8267, 97.7804, 98.0145,
		99.0808, 99.2009, 98.7561, 98.6203, 99.0372, 98.8813, 99.5797, 99.4915,
		98.8634, 98.3868, 97.9214, 97.0427, 97.4603, 97.0961, 97.1035, 96.0829,
		96.3888, 96.7068, 96.4908, 96.6133, 97.7787, 97.32, 97.0194, 97.0085,
		96.3549, 96.3515, 96.3601, 96.433, 96.6297, 97.1632, 97.2277, 97.3023,
		97.9343, 97.5447, 98.3446, 98.5176, 98.2568, 98.1991, 98.3853, 98.3385,
		98.1933, 98.3532, 98.4644, 98.7733, 98.8056, 99.1873, 99.0744, 99.4715,
		100.3905, 101.1483, 100.8201, 101.3286, 101.1336, 102.445, 102.624, 102.5897,
		102.3188, 101.992, 102.3775, 103.621, 103.7504, 104.1932, 104.3721, 104.1839,
		104.4925, 104.4742, 104.3575, 104.0838, 103.7767, 103.3327, 102.4842, 102.68,
		102.1418, 102.0679, 102.2514, 102.7377, 102.7209, 102.6527, 102.7031, 102.399,
		102.397, 102.6346, 102.573, 102.4861, 102.2352, 102.0229, 102.2471, 101.9287,
		101.9366, 102.2248, 102.8107, 103.4837, 103.8963, 103.7628, 104.361, 104.475,
		104.2938, 104.1347, 104.046, 105.059, 104.6566, 104.3111, 103.7988, 103.4479,
		102.9248, 102.7949, 104.1759, 105.2215, 105.9377, 105.6749, 106.0367, 106.3525,
		105.9371, 106.2972, 106.3638, 106.6556, 106.8206, 106.3769, 106.7515, 106.4405,
		107.2543, 107.1292, 107.3301, 106.4779, 106.5196, 106.3337,
	}

	if len(closes) != len(highs) || len(closes) != len(lows) {
		t.Fatal("fixture arrays must be the same length")
	}

	ks := make([]Kline, len(closes))
	for i := range closes {
		ks[i] = Kline{Open: closes[i], High: highs[i], Low: lows[i], Close: closes[i]}
	}

	const want = 28.379592
	got := calculateADX(ks, 14)
	if math.Abs(got-want) > 0.01 {
		t.Fatalf("ADX(14) = %.6f, want %.6f (reference implementation)", got, want)
	}
}

func TestCalculateKeltnerBandsAreATRScaled(t *testing.T) {
	klines := trendingKlines(120, 100, 1)

	upper, middle, lower := calculateKeltner(klines, 20, 14, 2.0)

	wantMiddle := calculateEMA(klines, 20)
	if middle != wantMiddle {
		t.Fatalf("Keltner middle = %v, want EMA(20) = %v", middle, wantMiddle)
	}

	atr := calculateATR(klines, 14)
	if atr <= 0 {
		t.Fatalf("test fixture produced a non-positive ATR (%v); cannot assert band width", atr)
	}

	wantUpper := wantMiddle + 2.0*atr
	wantLower := wantMiddle - 2.0*atr

	if math.Abs(upper-wantUpper) > 1e-9 {
		t.Fatalf("Keltner upper = %v, want %v", upper, wantUpper)
	}
	if math.Abs(lower-wantLower) > 1e-9 {
		t.Fatalf("Keltner lower = %v, want %v", lower, wantLower)
	}

	// Bands must bracket the middle line, otherwise the breakout check is
	// meaningless.
	if !(upper > middle && middle > lower) {
		t.Fatalf("bands not ordered correctly: upper=%v middle=%v lower=%v", upper, middle, lower)
	}
}

// TestCalculateKeltnerMultiplierScalesBands exercises the algorithm's own
// multiplier parameter. The multiplier is NOT user-configurable (the call sites
// pass store.KeltnerATRMultiplier), but the function still has to scale
// correctly so a future change to that constant behaves predictably.
func TestCalculateKeltnerMultiplierScalesBands(t *testing.T) {
	klines := trendingKlines(120, 100, 1)

	narrowUpper, _, narrowLower := calculateKeltner(klines, 20, 14, 1.0)
	wideUpper, _, wideLower := calculateKeltner(klines, 20, 14, 2.0)

	if wideUpper <= narrowUpper {
		t.Fatalf("multiplier 2.0 upper (%v) should exceed multiplier 1.0 upper (%v)", wideUpper, narrowUpper)
	}
	if wideLower >= narrowLower {
		t.Fatalf("multiplier 2.0 lower (%v) should sit below multiplier 1.0 lower (%v)", wideLower, narrowLower)
	}
}

// TestKeltnerCallSitesUseSharedMultiplier guards the invariant behind removing
// the keltner_multiplier config: the bands are computed with exactly the
// constant the prompt advertises, so what the model is told matches the data.
func TestKeltnerCallSitesUseSharedMultiplier(t *testing.T) {
	klines := trendingKlines(120, 100, 1)

	cUpper, cMiddle, cLower := calculateKeltner(klines, 20, 14, store.KeltnerATRMultiplier)
	lUpper, lMiddle, lLower := calculateKeltner(klines, 20, 14, 2.0)

	if cUpper != lUpper || cMiddle != lMiddle || cLower != lLower {
		t.Fatalf("KeltnerATRMultiplier (%v) disagrees with the historical 2.0: "+
			"constant=(%v, %v, %v) literal=(%v, %v, %v)",
			store.KeltnerATRMultiplier, cUpper, cMiddle, cLower, lUpper, lMiddle, lLower)
	}
}

func TestCalculateKeltnerDegenerateInputs(t *testing.T) {
	klines := trendingKlines(120, 100, 1)

	// Insufficient history and invalid periods must report zeroes rather than
	// partial bands the caller might misread as a tradeable level.
	if u, m, l := calculateKeltner(klines[:10], 20, 14, 2.0); u != 0 || m != 0 || l != 0 {
		t.Fatalf("insufficient data = (%v, %v, %v), want zeroes", u, m, l)
	}
	if u, m, l := calculateKeltner(klines, 0, 14, 2.0); u != 0 || m != 0 || l != 0 {
		t.Fatalf("zero emaPeriod = (%v, %v, %v), want zeroes", u, m, l)
	}
	if u, m, l := calculateKeltner(klines, 20, 0, 2.0); u != 0 || m != 0 || l != 0 {
		t.Fatalf("zero atrPeriod = (%v, %v, %v), want zeroes", u, m, l)
	}

	// A perfectly flat series produces a zero ATR; the bands should collapse
	// onto the middle line instead of fabricating width.
	flat := flatKlines(120, 100)
	upper, middle, lower := calculateKeltner(flat, 20, 14, 2.0)
	if upper != middle || middle != lower {
		t.Fatalf("flat series = (%v, %v, %v), want all equal to the middle line", upper, middle, lower)
	}
	if middle != 100 {
		t.Fatalf("flat series middle = %v, want 100", middle)
	}
}
