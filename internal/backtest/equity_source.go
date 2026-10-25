package backtest

import (
	"sort"
	"time"

	"nofx/store"
)

// StoreEquitySource builds an equity lookup from the recorded snapshots. It
// returns the most recent snapshot at or before the queried time, so risk
// sizing sees the account as it stood at that decision rather than today's
// balance. Returns nil when the range holds no snapshots, leaving the caller to
// fall back to Config.Equity.
func StoreEquitySource(st *store.Store, traderID string, from, to time.Time) func(time.Time) float64 {
	if st == nil || traderID == "" {
		return nil
	}
	// One cycle of slack: the first decision in the range can precede the first
	// snapshot written inside it.
	snaps, err := st.Equity().GetByTimeRange(traderID, from.Add(-24*time.Hour), to)
	if err != nil || len(snaps) == 0 {
		return nil
	}

	times := make([]time.Time, len(snaps))
	values := make([]float64, len(snaps))
	for i, s := range snaps {
		times[i] = s.Timestamp
		values[i] = s.TotalEquity
	}

	return func(at time.Time) float64 {
		i := sort.Search(len(times), func(i int) bool { return times[i].After(at) })
		if i == 0 {
			return values[0]
		}
		return values[i-1]
	}
}
