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

// calculateADX calculates the Wilder Average Directional Index plus the
// +DI/-DI directional indicators. ADX gauges trend strength (>25 trending,
// <20 range-bound); the DI pair carries direction (+DI > -DI = bullish).
func calculateADX(klines []Kline, period int) (adx, plusDI, minusDI float64) {
	if period <= 0 || len(klines) < period*2 {
		return 0, 0, 0
	}

	tr := make([]float64, 0, len(klines)-1)
	plusDM := make([]float64, 0, len(klines)-1)
	minusDM := make([]float64, 0, len(klines)-1)
	for i := 1; i < len(klines); i++ {
		high, low := klines[i].High, klines[i].Low
		prevHigh, prevLow, prevClose := klines[i-1].High, klines[i-1].Low, klines[i-1].Close

		tr = append(tr, math.Max(high-low, math.Max(math.Abs(high-prevClose), math.Abs(low-prevClose))))

		upMove := high - prevHigh
		downMove := prevLow - low
		switch {
		case upMove > downMove && upMove > 0:
			plusDM = append(plusDM, upMove)
			minusDM = append(minusDM, 0)
		case downMove > upMove && downMove > 0:
			plusDM = append(plusDM, 0)
			minusDM = append(minusDM, downMove)
		default:
			plusDM = append(plusDM, 0)
			minusDM = append(minusDM, 0)
		}
	}

	// Wilder smoothing: first value is the plain sum over `period`, then
	// sum = sum - sum/period + current.
	wilderSum := func(values []float64) []float64 {
		out := make([]float64, 0, len(values)-period+1)
		if len(values) < period {
			return out
		}
		sum := 0.0
		for i := 0; i < period; i++ {
			sum += values[i]
		}
		out = append(out, sum)
		for i := period; i < len(values); i++ {
			sum = sum - sum/float64(period) + values[i]
			out = append(out, sum)
		}
		return out
	}

	sumTR := wilderSum(tr)
	sumPlus := wilderSum(plusDM)
	sumMinus := wilderSum(minusDM)
	if len(sumTR) == 0 {
		return 0, 0, 0
	}

	dx := make([]float64, 0, len(sumTR))
	for i := range sumTR {
		if sumTR[i] == 0 {
			dx = append(dx, 0)
			continue
		}
		pdi := 100 * sumPlus[i] / sumTR[i]
		mdi := 100 * sumMinus[i] / sumTR[i]
		if pdi+mdi == 0 {
			dx = append(dx, 0)
			continue
		}
		dx = append(dx, 100*math.Abs(pdi-mdi)/(pdi+mdi))
	}
	if len(dx) < period {
		return 0, 0, 0
	}

	adx = 0.0
	for i := 0; i < period; i++ {
		adx += dx[i]
	}
	adx /= float64(period)
	for i := period; i < len(dx); i++ {
		adx = (adx*float64(period-1) + dx[i]) / float64(period)
	}

	last := len(sumTR) - 1
	if sumTR[last] > 0 {
		plusDI = 100 * sumPlus[last] / sumTR[last]
		minusDI = 100 * sumMinus[last] / sumTR[last]
	}
	return adx, plusDI, minusDI
}

// supertrendATRSeries returns the Wilder ATR aligned to klines (zero before
// the first computable bar at index `period`).
func supertrendATRSeries(klines []Kline, period int) []float64 {
	out := make([]float64, len(klines))
	if period <= 0 || len(klines) <= period {
		return out
	}

	trs := make([]float64, len(klines))
	for i := 1; i < len(klines); i++ {
		high, low, prevClose := klines[i].High, klines[i].Low, klines[i-1].Close
		trs[i] = math.Max(high-low, math.Max(math.Abs(high-prevClose), math.Abs(low-prevClose)))
	}

	sum := 0.0
	for i := 1; i <= period; i++ {
		sum += trs[i]
	}
	atr := sum / float64(period)
	out[period] = atr
	for i := period + 1; i < len(klines); i++ {
		atr = (atr*float64(period-1) + trs[i]) / float64(period)
		out[i] = atr
	}
	return out
}

// calculateSupertrend returns the latest Supertrend band and its direction
// (+1 = uptrend, band below price; -1 = downtrend, band above price). The
// band trails: it moves only in the trend's favor until a close flips it.
func calculateSupertrend(klines []Kline, period int, multiplier float64) (band float64, direction int) {
	if period <= 0 || multiplier <= 0 || len(klines) <= period {
		return 0, 0
	}
	atr := supertrendATRSeries(klines, period)

	finalUpper, finalLower := 0.0, 0.0
	for i := period; i < len(klines); i++ {
		hl2 := (klines[i].High + klines[i].Low) / 2
		basicUpper := hl2 + multiplier*atr[i]
		basicLower := hl2 - multiplier*atr[i]

		if i == period {
			finalUpper, finalLower = basicUpper, basicLower
		} else {
			if basicUpper < finalUpper || klines[i-1].Close > finalUpper {
				finalUpper = basicUpper
			}
			if basicLower > finalLower || klines[i-1].Close < finalLower {
				finalLower = basicLower
			}
		}

		switch {
		case direction >= 0 && klines[i].Close < finalLower:
			direction = -1
		case direction <= 0 && klines[i].Close > finalUpper:
			direction = 1
		case direction == 0:
			if klines[i].Close >= (finalUpper+finalLower)/2 {
				direction = 1
			} else {
				direction = -1
			}
		}

		if direction == 1 {
			band = finalLower
		} else {
			band = finalUpper
		}
	}
	return band, direction
}
