package api

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
