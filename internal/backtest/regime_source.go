package backtest

import (
	"sort"
	"time"

	"nofx/market"
	"nofx/store"
)

const (
	// regimeCascadeWindow is the burst window compared against its own history.
	regimeCascadeWindow = 15 * time.Minute
	// regimeCascadeBaselineWindows is how many preceding windows form the
	// baseline. Windows with no prints are left out: an empty window can mean a
	// quiet market or a missing feed, and the two must not be confused.
	regimeCascadeBaselineWindows = 4
	// regimeCascadeMinBaseline is the minimum number of non-empty baseline
	// windows before a multiple means anything.
	regimeCascadeMinBaseline = 2
	// regimeCascadeMultiple is how far above the baseline median a window must
	// be to count as a cascade.
	regimeCascadeMultiple  = 3.0
	regimeCascadeMinPrints = 10
	// regimeCrowdedThresholdPct is the hourly funding that marks one side as
	// crowded, matching the prompt's threshold.
	regimeCrowdedThresholdPct = 0.005
)

// StoreRegimeSource builds a RegimeSource from the rows the collectors persist.
// Reads are point-in-time, so a replayed decision is labelled with what was
// known when it was taken. It returns nil for a nil store, which leaves the
// replay unchanged.
func StoreRegimeSource(st *store.Store) RegimeSource {
	if st == nil {
		return nil
	}

	return func(symbol string, at time.Time) Regime {
		canonical := market.Normalize(symbol)
		regime := Regime{}

		// Crowding comes from the venue that charges funding hourly, so its rate
		// needs no interval normalization.
		if row, err := st.MarketMetrics().LatestAt("hyperliquid", canonical, at); err == nil && row != nil {
			switch {
			case row.FundingRate1hPct >= regimeCrowdedThresholdPct:
				regime.CrowdedSide = "long"
			case row.FundingRate1hPct <= -regimeCrowdedThresholdPct:
				regime.CrowdedSide = "short"
			}
		}

		longUSD, shortUSD, prints, err := st.MarketMetrics().LiquidationSum(canonical, at.Add(-regimeCascadeWindow), at)
		if err != nil || prints < regimeCascadeMinPrints {
			return regime
		}
		current := longUSD + shortUSD

		baseline := make([]float64, 0, regimeCascadeBaselineWindows)
		for i := 1; i <= regimeCascadeBaselineWindows; i++ {
			to := at.Add(-time.Duration(i) * regimeCascadeWindow)
			from := to.Add(-regimeCascadeWindow)
			windowLong, windowShort, _, err := st.MarketMetrics().LiquidationSum(canonical, from, to)
			if err != nil {
				continue
			}
			if total := windowLong + windowShort; total > 0 {
				baseline = append(baseline, total)
			}
		}
		if len(baseline) < regimeCascadeMinBaseline {
			return regime
		}
		sort.Float64s(baseline)
		median := baseline[len(baseline)/2]
		if median <= 0 || current < median*regimeCascadeMultiple {
			return regime
		}

		regime.Cascade = true
		if total := longUSD + shortUSD; total > 0 {
			switch share := longUSD / total * 100; {
			case share >= 70:
				regime.CrowdedSide = "long"
			case share <= 30:
				regime.CrowdedSide = "short"
			}
		}
		return regime
	}
}
