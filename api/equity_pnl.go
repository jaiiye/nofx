package api

import (
	"nofx/store"
	"nofx/trader"
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

// deriveSnapshotBaselines returns the PnL baseline in effect at each snapshot. A
// row that carries one keeps it; a row written before the baseline was stored
// gets money in reconstructed by trader.BaselineFromWallet, which places a
// deposit where it actually happened instead of applying one baseline to the
// whole curve — that would show the deposit as profit after it and as a loss
// before it.
//
// points must be in ascending time order, steps ascending by ExitTimeMs.
func deriveSnapshotBaselines(points []snapshotBaselineInput, steps []store.ClosedPnLPt) []float64 {
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
		// A derived value that cannot be a capital base reports no PnL rather
		// than a negative one.
		if derived := trader.BaselineFromWallet(point.Wallet, netRealized); derived > 0 {
			baselines[i] = derived
		}
	}

	return baselines
}

// snapshotBaselines returns the PnL baseline in effect at each of a trader's
// snapshots, in the same order.
func (s *Server) snapshotBaselines(traderID string, snapshots []*store.EquitySnapshot) ([]float64, error) {
	steps, err := s.store.Position().GetClosedPnLPoints(traderID)
	if err != nil {
		return nil, err
	}

	points := make([]snapshotBaselineInput, 0, len(snapshots))
	for _, snap := range snapshots {
		points = append(points, snapshotBaselineInput{
			TimeMs: snap.Timestamp.UnixMilli(),
			Wallet: snap.Balance,
			Stored: snap.InitialBalance,
		})
	}

	return deriveSnapshotBaselines(points, steps), nil
}
