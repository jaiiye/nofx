package market

import (
	"fmt"
	"sync"

	"nofx/logger"
)

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

	ADXPeriods           []int   // default [14]; emits ADX(p) plus DI+(p)/DI-(p)
	BOLLPeriods          []int   // default [20]
	BOLLMultiplier       float64 // default 2.0
	SupertrendPeriod     int     // default 10
	SupertrendMultiplier float64 // default 3.0; emits SUPERTREND(p,m) plus _DIR
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

		ADXPeriods:           []int{14},
		BOLLPeriods:          []int{20},
		BOLLMultiplier:       2.0,
		SupertrendPeriod:     10,
		SupertrendMultiplier: 3.0,
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
	if periods := positivePeriods(p.ADXPeriods); len(periods) > 0 {
		n.ADXPeriods = periods
	}
	if periods := positivePeriods(p.BOLLPeriods); len(periods) > 0 {
		n.BOLLPeriods = periods
	}
	if p.BOLLMultiplier > 0 {
		n.BOLLMultiplier = p.BOLLMultiplier
	}
	if p.SupertrendPeriod > 0 {
		n.SupertrendPeriod = p.SupertrendPeriod
	}
	if p.SupertrendMultiplier > 0 {
		n.SupertrendMultiplier = p.SupertrendMultiplier
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
// series over the last `count` bars of klines plus the single-value gauges
// (ATR / ADX / DI± / BOLL bands / Supertrend) per configured period. Series
// values follow the same convention as the fixed-period fields: a value is
// appended once the bar index has enough history for the period.
//
// An indicator whose period exceeds the available window is omitted instead of
// being emitted as 0: a fabricated zero reads as "zero volatility / no trend"
// and the model acts on it. The omission is logged once per indicator so a
// strategy configured beyond its analysis window is visible in the logs.
func configuredIndicatorSeries(klines []Kline, p *IndicatorParams, count int) []IndicatorSeries {
	if p == nil {
		return nil
	}
	p = p.normalized()
	start := len(klines) - count
	if start < 0 {
		start = 0
	}
	bars := len(klines)

	out := make([]IndicatorSeries, 0, len(p.EMAPeriods)+len(p.RSIPeriods)+len(p.ATRPeriods)+1)
	appendSeries := func(label string, values []float64, need int) {
		if len(values) == 0 {
			warnIndicatorUnavailable(label, bars, need)
			return
		}
		out = append(out, IndicatorSeries{Label: label, Values: values})
	}

	for _, period := range p.EMAPeriods {
		appendSeries(fmt.Sprintf("EMA(%d)", period),
			indicatorSeriesOverWindow(klines, start, period, func(kl []Kline) float64 { return calculateEMA(kl, period) }),
			period)
	}
	for _, period := range p.RSIPeriods {
		appendSeries(fmt.Sprintf("RSI(%d)", period),
			indicatorSeriesOverWindow(klines, start, period, func(kl []Kline) float64 { return calculateRSI(kl, period) }),
			period)
	}
	fast, slow := p.MACDFast, p.MACDSlow
	appendSeries(fmt.Sprintf("MACD(%d,%d)", fast, slow),
		indicatorSeriesOverWindow(klines, start, slow, func(kl []Kline) float64 { return calculateMACDWithPeriods(kl, fast, slow) }),
		slow)

	// Single-value gauges: only computed when the window can actually produce
	// them (the window requirements mirror the calculators' own guards).
	for _, period := range p.ATRPeriods {
		label := fmt.Sprintf("ATR(%d)", period)
		if bars <= period {
			warnIndicatorUnavailable(label, bars, period+1)
			continue
		}
		appendSeries(label, []float64{calculateATR(klines, period)}, period+1)
	}

	// ADX with its directional indicators: strength and direction together,
	// as single latest values (regime gauges, not plotted series).
	for _, period := range p.ADXPeriods {
		need := period * 2
		if bars < need {
			warnIndicatorUnavailable(fmt.Sprintf("ADX(%d)", period), bars, need)
			continue
		}
		adx, plusDI, minusDI := calculateADX(klines, period)
		appendSeries(fmt.Sprintf("ADX(%d)", period), []float64{adx}, need)
		appendSeries(fmt.Sprintf("DI+(%d)", period), []float64{plusDI}, need)
		appendSeries(fmt.Sprintf("DI-(%d)", period), []float64{minusDI}, need)
	}

	// Bollinger Bands with the configured period and standard-deviation
	// multiplier (three series per period: upper/middle/lower).
	for _, period := range p.BOLLPeriods {
		mult := p.BOLLMultiplier
		upper := indicatorSeriesOverWindow(klines, start, period, func(kl []Kline) float64 {
			u, _, _ := calculateBOLL(kl, period, mult)
			return u
		})
		middle := indicatorSeriesOverWindow(klines, start, period, func(kl []Kline) float64 {
			_, m, _ := calculateBOLL(kl, period, mult)
			return m
		})
		lower := indicatorSeriesOverWindow(klines, start, period, func(kl []Kline) float64 {
			_, _, l := calculateBOLL(kl, period, mult)
			return l
		})
		appendSeries(fmt.Sprintf("BOLL_UPPER(%d,%.1f)", period, mult), upper, period)
		appendSeries(fmt.Sprintf("BOLL_MIDDLE(%d,%.1f)", period, mult), middle, period)
		appendSeries(fmt.Sprintf("BOLL_LOWER(%d,%.1f)", period, mult), lower, period)
	}

	// Supertrend: trailing band plus its direction (+1 up / -1 down).
	if p.SupertrendPeriod > 0 {
		bandLabel := fmt.Sprintf("SUPERTREND(%d,%.1f)", p.SupertrendPeriod, p.SupertrendMultiplier)
		dirLabel := fmt.Sprintf("SUPERTREND_DIR(%d,%.1f)", p.SupertrendPeriod, p.SupertrendMultiplier)
		if bars <= p.SupertrendPeriod {
			warnIndicatorUnavailable(bandLabel, bars, p.SupertrendPeriod+1)
			return out
		}
		band, direction := calculateSupertrend(klines, p.SupertrendPeriod, p.SupertrendMultiplier)
		appendSeries(bandLabel, []float64{band}, p.SupertrendPeriod+1)
		appendSeries(dirLabel, []float64{float64(direction)}, p.SupertrendPeriod+1)
	}
	return out
}

// indicatorWarnings dedupes the "this indicator does not fit the window" log:
// without it the same line repeats per symbol and per timeframe on every cycle.
var indicatorWarnings sync.Map

func warnIndicatorUnavailable(label string, have, need int) {
	if _, seen := indicatorWarnings.LoadOrStore(label, struct{}{}); seen {
		return
	}
	logger.Infof("⚠️ %s omitted from the prompt: needs %d bars, the window has %d — widen klines.primary_count / longer_count or lower the period",
		label, need, have)
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
