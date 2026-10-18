package trader

import (
	"fmt"
	"math"
	"strings"

	"nofx/kernel"
)

// Three positions in names that move together are one bet paying three sets of
// fees. Correlation on daily/hourly returns of liquid crypto and of same-sector
// equities is routinely 0.8+, which is why "max positions: 3" overstates the
// number of independent risks the book carries. The cap below counts a
// same-side cluster as one bet and refuses the entry that would exceed it.
//
// The threshold and cap are config-driven (risk_control.max_correlated_positions,
// correlation_threshold) with defaults that only bite on genuinely redundant
// exposure: two positions per cluster, rho >= 0.8.

// minCorrelationBars is the shortest return series accepted. Below this the
// estimate is noise, so the check fails open rather than blocking on a guess.
const minCorrelationBars = 20

// returnsFromCloses converts a close series into simple returns.
func returnsFromCloses(closes []float64) []float64 {
	if len(closes) < 2 {
		return nil
	}
	out := make([]float64, 0, len(closes)-1)
	for i := 1; i < len(closes); i++ {
		prev := closes[i-1]
		if prev <= 0 {
			out = append(out, 0)
			continue
		}
		out = append(out, (closes[i]-prev)/prev)
	}
	return out
}

// pearsonCorrelation is the Pearson r of two equal-length series. It returns 0
// when either side has no variance (a flat series carries no information, and a
// division by zero would otherwise poison the result).
func pearsonCorrelation(a, b []float64) float64 {
	n := len(a)
	if n == 0 || n != len(b) {
		return 0
	}
	var sumA, sumB float64
	for i := 0; i < n; i++ {
		sumA += a[i]
		sumB += b[i]
	}
	meanA, meanB := sumA/float64(n), sumB/float64(n)

	var cov, varA, varB float64
	for i := 0; i < n; i++ {
		da, db := a[i]-meanA, b[i]-meanB
		cov += da * db
		varA += da * da
		varB += db * db
	}
	if varA <= 0 || varB <= 0 {
		return 0
	}
	return cov / math.Sqrt(varA*varB)
}

// correlationOfCloses aligns two close series to their common tail and returns
// the correlation of their returns. ok is false when there is not enough history.
func correlationOfCloses(a, b []float64) (float64, bool) {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	if n < minCorrelationBars+1 {
		return 0, false
	}
	ra := returnsFromCloses(a[len(a)-n:])
	rb := returnsFromCloses(b[len(b)-n:])
	if len(ra) < minCorrelationBars || len(rb) < minCorrelationBars {
		return 0, false
	}
	return pearsonCorrelation(ra, rb), true
}

// correlatedClusterCount counts the same-side symbols that correlate at or above
// the threshold with the candidate, and reports the most correlated one so the
// rejection can name it.
func correlatedClusterCount(candidate []float64, existing map[string][]float64, threshold float64) (count int, worstSymbol string, worstRho float64) {
	worstRho = -2
	for symbol, closes := range existing {
		rho, ok := correlationOfCloses(candidate, closes)
		if !ok || rho < threshold {
			continue
		}
		count++
		if rho > worstRho {
			worstRho, worstSymbol = rho, symbol
		}
	}
	if count == 0 {
		return 0, "", 0
	}
	return count, worstSymbol, worstRho
}

// primaryCloses extracts a symbol's closed-bar closes from the decision context,
// preferring the strategy's primary timeframe.
func primaryCloses(ctx *kernel.Context, symbol, primary string) []float64 {
	if ctx == nil {
		return nil
	}
	data := ctx.MarketDataMap[symbol]
	if data == nil {
		// Match the way positions are keyed when the symbol case differs.
		for key, candidate := range ctx.MarketDataMap {
			if strings.EqualFold(key, symbol) {
				data = candidate
				break
			}
		}
	}
	if data == nil {
		return nil
	}
	pick := func(tf string) []float64 {
		series := data.TimeframeData[tf]
		if series == nil {
			return nil
		}
		closes := make([]float64, 0, len(series.Klines))
		for _, k := range series.Klines {
			closes = append(closes, k.Close)
		}
		return closes
	}
	if closes := pick(primary); len(closes) > 0 {
		return closes
	}
	// Fall back to any timeframe that carries candles (deterministic order).
	for _, tf := range []string{"1h", "4h", "15m", "5m", "1d", "30m", "1m", "2h", "6h", "8h", "12h", "3d", "1w"} {
		if closes := pick(tf); len(closes) > 0 {
			return closes
		}
	}
	return nil
}

// checkCorrelatedExposure refuses an opening decision that would put more than
// the configured number of same-side positions into one correlation cluster.
//
// It fails open on missing data — a symbol without candles is not evidence of
// low correlation, and a data gap must not stop trading.
func (at *AutoTrader) checkCorrelatedExposure(ctx *kernel.Context, symbol, action string) error {
	if ctx == nil || at.config.StrategyConfig == nil {
		return nil
	}
	enabled, cap, threshold := at.config.StrategyConfig.RiskControl.CorrelatedExposureCap()
	if !enabled {
		return nil
	}

	primary := at.config.StrategyConfig.Indicators.Klines.PrimaryTimeframe
	candidate := primaryCloses(ctx, symbol, primary)
	if len(candidate) == 0 {
		return nil
	}

	side := sideLabel(action)
	existing := make(map[string][]float64, len(ctx.Positions))
	for _, pos := range ctx.Positions {
		if strings.EqualFold(pos.Symbol, symbol) || !strings.EqualFold(pos.Side, side) {
			continue
		}
		if closes := primaryCloses(ctx, pos.Symbol, primary); len(closes) > 0 {
			existing[pos.Symbol] = closes
		}
	}
	if len(existing) == 0 {
		return nil
	}

	count, worst, rho := correlatedClusterCount(candidate, existing, threshold)
	if count+1 <= cap {
		return nil
	}

	at.logInfof("🚫 Correlated exposure: %s %s would be position %d in a same-side cluster at ρ≥%.2f (cap %d) — most correlated: %s (ρ=%.2f)",
		symbol, side, count+1, threshold, cap, worst, rho)
	return fmt.Errorf("%s %s is too correlated with %s (ρ=%.2f) to add another %s position: %d already at ρ≥%.2f, cap is %d — size up the existing name or wait",
		symbol, side, worst, rho, side, count, threshold, cap)
}
