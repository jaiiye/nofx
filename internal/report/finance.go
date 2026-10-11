// Package report provides pure financial-metric helpers shared by the stats
// report and the decision-replay backtester. All functions are pure so they
// can be unit-tested without a database.
package report

import (
	"math"

	"nofx/store"
)

// DailyEquity resamples snapshots to one equity value per UTC day (the last
// snapshot of each day), ascending by time.
func DailyEquity(snaps []*store.EquitySnapshot) []float64 {
	if len(snaps) == 0 {
		return nil
	}
	byDay := map[string]float64{}
	order := []string{}
	for _, s := range snaps {
		day := s.Timestamp.UTC().Format("2006-01-02")
		if _, seen := byDay[day]; !seen {
			order = append(order, day)
		}
		byDay[day] = s.TotalEquity
	}
	out := make([]float64, 0, len(order))
	for _, d := range order {
		out = append(out, byDay[d])
	}
	return out
}

// DailyReturns converts an equity series to simple per-period returns.
func DailyReturns(equities []float64) []float64 {
	if len(equities) < 2 {
		return nil
	}
	out := make([]float64, 0, len(equities)-1)
	for i := 1; i < len(equities); i++ {
		prev := equities[i-1]
		if prev <= 0 {
			continue
		}
		out = append(out, (equities[i]-prev)/prev)
	}
	return out
}

// Mean returns the arithmetic mean (0 for an empty slice).
func Mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

// StdDev returns the sample standard deviation (0 for <2 points).
func StdDev(xs []float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	m := Mean(xs)
	var ss float64
	for _, x := range xs {
		d := x - m
		ss += d * d
	}
	return math.Sqrt(ss / float64(len(xs)-1))
}

// DownsideDeviation returns the root-mean-square of negative returns — the
// Sortino denominator.
func DownsideDeviation(xs []float64) float64 {
	var ss float64
	for _, x := range xs {
		if x < 0 {
			ss += x * x
		}
	}
	if ss == 0 {
		return 0
	}
	return math.Sqrt(ss / float64(len(xs)))
}

// Sharpe returns the annualized Sharpe ratio. periodsPerYear is 365 for daily
// crypto returns. Returns 0 when there is no dispersion.
func Sharpe(returns []float64, periodsPerYear float64) float64 {
	if len(returns) < 2 {
		return 0
	}
	sd := StdDev(returns)
	if sd == 0 {
		return 0
	}
	return Mean(returns) / sd * math.Sqrt(periodsPerYear)
}

// Sortino returns the annualized Sortino ratio (downside deviation only).
func Sortino(returns []float64, periodsPerYear float64) float64 {
	if len(returns) < 2 {
		return 0
	}
	dd := DownsideDeviation(returns)
	if dd == 0 {
		return 0
	}
	return Mean(returns) / dd * math.Sqrt(periodsPerYear)
}

// MaxDrawdownPct returns the largest peak-to-trough decline of an equity
// series, in percent.
func MaxDrawdownPct(equities []float64) float64 {
	var peak, maxDD float64
	for _, e := range equities {
		if e > peak {
			peak = e
		}
		if peak > 0 {
			dd := (peak - e) / peak * 100
			if dd > maxDD {
				maxDD = dd
			}
		}
	}
	return maxDD
}

// AnnualizedReturnPct approximates CAGR from the equity series using the
// actual elapsed span between the first and last snapshot.
func AnnualizedReturnPct(snaps []*store.EquitySnapshot) float64 {
	if len(snaps) < 2 {
		return 0
	}
	first, last := snaps[0], snaps[len(snaps)-1]
	if first.TotalEquity <= 0 {
		return 0
	}
	span := last.Timestamp.Sub(first.Timestamp)
	if span <= 0 {
		return 0
	}
	total := last.TotalEquity / first.TotalEquity
	years := span.Hours() / 24 / 365
	if years <= 0 {
		return 0
	}
	return (math.Pow(total, 1/years) - 1) * 100
}
