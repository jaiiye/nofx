package market

import "fmt"

// IndicatorParams carries the strategy's configured indicator periods into
// the market calculation layer. Nil/zero fields fall back to the canonical
// defaults, so a partial or missing config always produces complete
// indicator output.
type IndicatorParams struct {
	EMAPeriods []int // default [20, 50]
	RSIPeriods []int // default [7, 14]
	ATRPeriods []int // default [14]
	MACDFast   int   // default 12
	MACDSlow   int   // default 26
}

// DefaultIndicatorParams returns the canonical periods, matching the
// fixed-period fields on the data structs.
func DefaultIndicatorParams() *IndicatorParams {
	return &IndicatorParams{
		EMAPeriods: []int{20, 50},
		RSIPeriods: []int{7, 14},
		ATRPeriods: []int{14},
		MACDFast:   12,
		MACDSlow:   26,
	}
}

// normalized returns a copy with every zero/invalid field replaced by its
// default. MACD requires fast < slow; a swapped pair falls back to the
// canonical 12/26.
func (p *IndicatorParams) normalized() *IndicatorParams {
	n := DefaultIndicatorParams()
	if p == nil {
		return n
	}
	if periods := positivePeriods(p.EMAPeriods); len(periods) > 0 {
		n.EMAPeriods = periods
	}
	if periods := positivePeriods(p.RSIPeriods); len(periods) > 0 {
		n.RSIPeriods = periods
	}
	if periods := positivePeriods(p.ATRPeriods); len(periods) > 0 {
		n.ATRPeriods = periods
	}
	if p.MACDFast > 0 {
		n.MACDFast = p.MACDFast
	}
	if p.MACDSlow > 0 {
		n.MACDSlow = p.MACDSlow
	}
	if n.MACDFast >= n.MACDSlow {
		n.MACDFast, n.MACDSlow = 12, 26
	}
	return n
}

func positivePeriods(periods []int) []int {
	out := make([]int, 0, len(periods))
	for _, v := range periods {
		if v > 0 {
			out = append(out, v)
		}
	}
	return out
}

// calculateMACDWithPeriods computes MACD with explicit fast/slow EMA periods.
func calculateMACDWithPeriods(klines []Kline, fast, slow int) float64 {
	if len(klines) < slow {
		return 0
	}
	emaFast := calculateEMA(klines, fast)
	emaSlow := calculateEMA(klines, slow)
	return emaFast - emaSlow
}

// configuredIndicatorSeries computes the strategy-configured EMA/RSI/MACD
// series over the last `count` bars of klines plus one ATR value per
// configured period. Series values follow the same convention as the
// fixed-period fields: a value is appended once the bar index has enough
// history for the period.
func configuredIndicatorSeries(klines []Kline, p *IndicatorParams, count int) []IndicatorSeries {
	if p == nil {
		return nil
	}
	p = p.normalized()
	start := len(klines) - count
	if start < 0 {
		start = 0
	}

	out := make([]IndicatorSeries, 0, len(p.EMAPeriods)+len(p.RSIPeriods)+len(p.ATRPeriods)+1)
	for _, period := range p.EMAPeriods {
		out = append(out, IndicatorSeries{
			Label:  fmt.Sprintf("EMA(%d)", period),
			Values: indicatorSeriesOverWindow(klines, start, period, func(kl []Kline) float64 { return calculateEMA(kl, period) }),
		})
	}
	for _, period := range p.RSIPeriods {
		out = append(out, IndicatorSeries{
			Label:  fmt.Sprintf("RSI(%d)", period),
			Values: indicatorSeriesOverWindow(klines, start, period, func(kl []Kline) float64 { return calculateRSI(kl, period) }),
		})
	}
	fast, slow := p.MACDFast, p.MACDSlow
	out = append(out, IndicatorSeries{
		Label:  fmt.Sprintf("MACD(%d,%d)", fast, slow),
		Values: indicatorSeriesOverWindow(klines, start, slow, func(kl []Kline) float64 { return calculateMACDWithPeriods(kl, fast, slow) }),
	})
	for _, period := range p.ATRPeriods {
		out = append(out, IndicatorSeries{
			Label:  fmt.Sprintf("ATR(%d)", period),
			Values: []float64{calculateATR(klines, period)},
		})
	}
	return out
}

// indicatorSeriesOverWindow walks the window starting at startIdx, appending
// a value once the bar index provides at least minBars of history (matching
// the fixed-period series, which append when i >= period-1).
func indicatorSeriesOverWindow(klines []Kline, startIdx, minBars int, fn func([]Kline) float64) []float64 {
	if startIdx < 0 {
		startIdx = 0
	}
	values := make([]float64, 0, len(klines)-startIdx)
	for i := startIdx; i < len(klines); i++ {
		if i+1 < minBars {
			continue
		}
		values = append(values, fn(klines[:i+1]))
	}
	return values
}
