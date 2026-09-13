package market

import "math"

// calculateEMA calculates EMA
func calculateEMA(klines []Kline, period int) float64 {
	if len(klines) < period {
		return 0
	}

	// Calculate SMA as initial EMA
	sum := 0.0
	for i := 0; i < period; i++ {
		sum += klines[i].Close
	}
	ema := sum / float64(period)

	// Calculate EMA
	multiplier := 2.0 / float64(period+1)
	for i := period; i < len(klines); i++ {
		ema = (klines[i].Close-ema)*multiplier + ema
	}

	return ema
}

// calculateMACD calculates MACD
func calculateMACD(klines []Kline) float64 {
	if len(klines) < 26 {
		return 0
	}

	// Calculate 12-period and 26-period EMA
	ema12 := calculateEMA(klines, 12)
	ema26 := calculateEMA(klines, 26)

	// MACD = EMA12 - EMA26
	return ema12 - ema26
}

// calculateRSI calculates RSI
func calculateRSI(klines []Kline, period int) float64 {
	if len(klines) <= period {
		return 0
	}

	gains := 0.0
	losses := 0.0

	// Calculate initial average gain/loss
	for i := 1; i <= period; i++ {
		change := klines[i].Close - klines[i-1].Close
		if change > 0 {
			gains += change
		} else {
			losses += -change
		}
	}

	avgGain := gains / float64(period)
	avgLoss := losses / float64(period)

	// Use Wilder smoothing method to calculate subsequent RSI
	for i := period + 1; i < len(klines); i++ {
		change := klines[i].Close - klines[i-1].Close
		if change > 0 {
			avgGain = (avgGain*float64(period-1) + change) / float64(period)
			avgLoss = (avgLoss * float64(period-1)) / float64(period)
		} else {
			avgGain = (avgGain * float64(period-1)) / float64(period)
			avgLoss = (avgLoss*float64(period-1) + (-change)) / float64(period)
		}
	}

	if avgLoss == 0 {
		return 100
	}

	rs := avgGain / avgLoss
	rsi := 100 - (100 / (1 + rs))

	return rsi
}

// calculateATR calculates ATR
func calculateATR(klines []Kline, period int) float64 {
	if len(klines) <= period {
		return 0
	}

	trs := make([]float64, len(klines))
	for i := 1; i < len(klines); i++ {
		high := klines[i].High
		low := klines[i].Low
		prevClose := klines[i-1].Close

		tr1 := high - low
		tr2 := math.Abs(high - prevClose)
		tr3 := math.Abs(low - prevClose)

		trs[i] = math.Max(tr1, math.Max(tr2, tr3))
	}

	// Calculate initial ATR
	sum := 0.0
	for i := 1; i <= period; i++ {
		sum += trs[i]
	}
	atr := sum / float64(period)

	// Wilder smoothing
	for i := period + 1; i < len(klines); i++ {
		atr = (atr*float64(period-1) + trs[i]) / float64(period)
	}

	return atr
}

// calculateBOLL calculates Bollinger Bands (upper, middle, lower)
// period: typically 20, multiplier: typically 2
func calculateBOLL(klines []Kline, period int, multiplier float64) (upper, middle, lower float64) {
	if len(klines) < period {
		return 0, 0, 0
	}

	// Calculate SMA (middle band)
	sum := 0.0
	for i := len(klines) - period; i < len(klines); i++ {
		sum += klines[i].Close
	}
	sma := sum / float64(period)

	// Calculate standard deviation
	variance := 0.0
	for i := len(klines) - period; i < len(klines); i++ {
		diff := klines[i].Close - sma
		variance += diff * diff
	}
	stdDev := math.Sqrt(variance / float64(period))

	// Calculate bands
	middle = sma
	upper = sma + multiplier*stdDev
	lower = sma - multiplier*stdDev

	return upper, middle, lower
}

// calculateADX calculates the Average Directional Index (Wilder).
//
// ADX measures trend strength irrespective of direction, which is why it is
// used as a chop filter rather than a directional signal: a high reading says
// "a trend exists", not "which way". The full DMI chain is required because
// DX is derived from the smoothed +DI/-DI pair.
//
// Requires at least 2*period+1 candles to seed both the initial DI window and
// the first smoothed DX. Returns 0 when there is not enough data, matching the
// convention used by the other indicator helpers.
func calculateADX(klines []Kline, period int) float64 {
	if period <= 0 || len(klines) < 2*period+1 {
		return 0
	}

	// Step 1: per-bar directional movement and true range.
	plusDM := make([]float64, len(klines))
	minusDM := make([]float64, len(klines))
	tr := make([]float64, len(klines))

	for i := 1; i < len(klines); i++ {
		upMove := klines[i].High - klines[i-1].High
		downMove := klines[i-1].Low - klines[i].Low

		// Only the larger of the two moves counts, and only when positive.
		if upMove > downMove && upMove > 0 {
			plusDM[i] = upMove
		}
		if downMove > upMove && downMove > 0 {
			minusDM[i] = downMove
		}

		highLow := klines[i].High - klines[i].Low
		highPrevClose := math.Abs(klines[i].High - klines[i-1].Close)
		lowPrevClose := math.Abs(klines[i].Low - klines[i-1].Close)
		tr[i] = math.Max(highLow, math.Max(highPrevClose, lowPrevClose))
	}

	// Step 2: seed with a simple sum over the first `period` bars, then apply
	// Wilder smoothing for the remainder.
	sumTR, sumPlusDM, sumMinusDM := 0.0, 0.0, 0.0
	for i := 1; i <= period; i++ {
		sumTR += tr[i]
		sumPlusDM += plusDM[i]
		sumMinusDM += minusDM[i]
	}

	// Accumulate DX values so the ADX itself can be smoothed at the end.
	dxValues := make([]float64, 0, len(klines)-period)
	dxValues = append(dxValues, dxFromSmoothed(sumTR, sumPlusDM, sumMinusDM))

	for i := period + 1; i < len(klines); i++ {
		sumTR = sumTR - sumTR/float64(period) + tr[i]
		sumPlusDM = sumPlusDM - sumPlusDM/float64(period) + plusDM[i]
		sumMinusDM = sumMinusDM - sumMinusDM/float64(period) + minusDM[i]
		dxValues = append(dxValues, dxFromSmoothed(sumTR, sumPlusDM, sumMinusDM))
	}

	// Step 3: ADX is the Wilder average of DX. The first ADX needs `period`
	// DX samples, so anything shorter cannot produce a reading yet.
	if len(dxValues) < period {
		return 0
	}

	adx := 0.0
	for i := 0; i < period; i++ {
		adx += dxValues[i]
	}
	adx /= float64(period)

	for i := period; i < len(dxValues); i++ {
		adx = (adx*float64(period-1) + dxValues[i]) / float64(period)
	}

	return adx
}

// dxFromSmoothed converts smoothed +DM/-DM/TR accumulators into a single DX
// value in 0..100. A zero TR means no movement at all, so there is no trend.
func dxFromSmoothed(sumTR, sumPlusDM, sumMinusDM float64) float64 {
	if sumTR == 0 {
		return 0
	}

	plusDI := 100 * sumPlusDM / sumTR
	minusDI := 100 * sumMinusDM / sumTR

	diSum := plusDI + minusDI
	if diSum == 0 {
		return 0
	}

	return 100 * math.Abs(plusDI-minusDI) / diSum
}

// calculateKeltner calculates Keltner Channel bands (upper, middle, lower).
//
// Unlike Bollinger Bands, which scale with price dispersion, the Keltner width
// is driven by ATR. That makes the bands less prone to pinching during quiet
// periods and to ballooning after a single outlier bar, which is the property
// that makes it a cleaner breakout trigger than a standard-deviation envelope.
//
//	upper  = EMA(close, emaPeriod) + multiplier * ATR(atrPeriod)
//	middle = EMA(close, emaPeriod)
//	lower  = EMA(close, emaPeriod) - multiplier * ATR(atrPeriod)
func calculateKeltner(klines []Kline, emaPeriod, atrPeriod int, multiplier float64) (upper, middle, lower float64) {
	if emaPeriod <= 0 || atrPeriod <= 0 || len(klines) < emaPeriod {
		return 0, 0, 0
	}

	middle = calculateEMA(klines, emaPeriod)
	if middle == 0 {
		return 0, 0, 0
	}

	atr := calculateATR(klines, atrPeriod)
	if atr == 0 {
		// Degenerate case: no measurable range. Collapse the bands onto the
		// middle line rather than inventing a width.
		return middle, middle, middle
	}

	offset := multiplier * atr
	return middle + offset, middle, middle - offset
}

// calculateDonchian calculates Donchian channel (highest high, lowest low) for given period
func calculateDonchian(klines []Kline, period int) (upper, lower float64) {
	if len(klines) == 0 || period <= 0 {
		return 0, 0
	}

	// Use all available klines if period > len(klines)
	start := len(klines) - period
	if start < 0 {
		start = 0
	}

	upper = klines[start].High
	lower = klines[start].Low

	for i := start + 1; i < len(klines); i++ {
		if klines[i].High > upper {
			upper = klines[i].High
		}
		if klines[i].Low < lower {
			lower = klines[i].Low
		}
	}

	return upper, lower
}

// Box period constants (in 1h candles)
const (
	ShortBoxPeriod = 72  // 3 days of 1h candles
	MidBoxPeriod   = 240 // 10 days of 1h candles
	LongBoxPeriod  = 500 // ~21 days of 1h candles
)

// Long-term trend filter period. EMA200 needs a full 200-bar warmup before it
// produces a value, which is why the kline fetch count must exceed it — see
// KlineFetchLimit.
const ema200Period = 200

// KlineFetchLimit is how many candles are pulled per timeframe. It must stay
// comfortably above ema200Period so the EMA200 is available on the most recent
// bar; a fetch of exactly 200 leaves no margin for a partially closed candle.
const KlineFetchLimit = 250

// calculateBoxData calculates multi-period box data from klines
func calculateBoxData(klines []Kline, currentPrice float64) *BoxData {
	box := &BoxData{
		CurrentPrice: currentPrice,
	}

	if len(klines) == 0 {
		return box
	}

	box.ShortUpper, box.ShortLower = calculateDonchian(klines, ShortBoxPeriod)
	box.MidUpper, box.MidLower = calculateDonchian(klines, MidBoxPeriod)
	box.LongUpper, box.LongLower = calculateDonchian(klines, LongBoxPeriod)

	return box
}

// ========== Exported indicator calculation functions (for testing) ==========

// ExportCalculateEMA exports calculateEMA for testing
func ExportCalculateEMA(klines []Kline, period int) float64 {
	return calculateEMA(klines, period)
}

// ExportCalculateMACD exports calculateMACD for testing
func ExportCalculateMACD(klines []Kline) float64 {
	return calculateMACD(klines)
}

// ExportCalculateRSI exports calculateRSI for testing
func ExportCalculateRSI(klines []Kline, period int) float64 {
	return calculateRSI(klines, period)
}

// ExportCalculateATR exports calculateATR for testing
func ExportCalculateATR(klines []Kline, period int) float64 {
	return calculateATR(klines, period)
}

// ExportCalculateBOLL exports calculateBOLL for testing
func ExportCalculateBOLL(klines []Kline, period int, multiplier float64) (upper, middle, lower float64) {
	return calculateBOLL(klines, period, multiplier)
}

// ExportCalculateDonchian exports calculateDonchian for testing
func ExportCalculateDonchian(klines []Kline, period int) (float64, float64) {
	return calculateDonchian(klines, period)
}

// ExportCalculateBoxData exports calculateBoxData for testing
func ExportCalculateBoxData(klines []Kline, currentPrice float64) *BoxData {
	return calculateBoxData(klines, currentPrice)
}
