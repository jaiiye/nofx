package api

import (
	"nofx/logger"
	"nofx/store"
)

// snapshotBaselineInput is one equity point reduced to what deriving its baseline
// needs: when it was taken, the wallet balance then (Balance = equity −
// unrealized), and the baseline stored with the row (0 for rows written before
// the per-snapshot baseline existed).
type snapshotBaselineInput struct {
	TimeMs int64
	Wallet float64
	Stored float64
}

// deriveSnapshotBaselines returns the PnL baseline in effect at each snapshot.
// Rows that carry one keep it; rows that do not get money in reconstructed as
// wallet − realized PnL accumulated by then, which puts a deposit where it
// actually happened instead of applying one baseline to the whole curve (that
// would show the deposit as profit before the deposit and as a loss after it).
// fallback is used when the reconstruction is not usable.
//
// points must be in ascending time order, steps ascending by ExitTimeMs.
func deriveSnapshotBaselines(points []snapshotBaselineInput, steps []store.ClosedPnLPt, fallback float64) []float64 {
	baselines := make([]float64, len(points))
	netRealized := 0.0
	next := 0

	for i, point := range points {
		for next < len(steps) && steps[next].ExitTimeMs <= point.TimeMs {
			netRealized += steps[next].Net
			next++
		}

		if point.Stored > 0 {
			baselines[i] = point.Stored
			continue
		}
		if derived := point.Wallet - netRealized; derived > 0 {
			baselines[i] = derived
			continue
		}
		baselines[i] = fallback
	}

	return baselines
}

// snapshotBaselines returns the PnL baseline in effect at each of a trader's
// snapshots, in the same order. Snapshots written before the per-snapshot
// baseline existed are reconstructed from the trader's realized PnL series; if
// that series cannot be read, the stored (or configured) baseline is kept.
func (s *Server) snapshotBaselines(traderID string, snapshots []*store.EquitySnapshot, fallback float64) []float64 {
	steps, err := s.store.Position().GetClosedPnLPoints(traderID)
	if err != nil {
		logger.Errorf("[API] Failed to read closed PnL points for %s: %v", traderID, err)
	}

	points := make([]snapshotBaselineInput, 0, len(snapshots))
	for _, snap := range snapshots {
		stored := snap.InitialBalance
		if err != nil {
			// Without the realized-PnL series the pre-ledger rows cannot be
			// reconstructed, so hand the walker the resolved baseline instead.
			stored = resolveSnapshotBaseline(stored, fallback)
		}
		points = append(points, snapshotBaselineInput{
			TimeMs: snap.Timestamp.UnixMilli(),
			Wallet: snap.Balance,
			Stored: stored,
		})
	}

	return deriveSnapshotBaselines(points, steps, fallback)
}

// equityPointPnL returns the cash-flow neutral PnL of an equity snapshot and
// its percentage, given the PnL baseline that was in effect when the snapshot
// was taken (stores.EquitySnapshot.InitialBalance). Using the baseline of the
// point — rather than the trader's current one — is what keeps a deposit or
// withdrawal from showing up as profit on the earlier part of the curve.
func equityPointPnL(totalEquity, baseline float64) (float64, float64) {
	if baseline <= 0 {
		return 0, 0
	}
	pnl := totalEquity - baseline
	return pnl, pnl / baseline * 100
}

// resolveSnapshotBaseline picks the baseline for one snapshot: the value stored
// with the row, falling back to the caller's default for rows written before
// the baseline was recorded.
func resolveSnapshotBaseline(snapshotBaseline, fallback float64) float64 {
	if snapshotBaseline > 0 {
		return snapshotBaseline
	}
	return fallback
}
