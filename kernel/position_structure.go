package kernel

import (
	"context"
	"encoding/json"

	"nofx/logger"
	"nofx/market"
	"nofx/store"
)

// StartPositionStructure wires the holder-position index (market) to the store
// and starts its refresh loop. It returns the index so boot can stop it.
//
// This is the only place a collected market snapshot becomes a stored row.
// Everything downstream — the decision prompt, the structure gate and the
// offline replay — reads stored snapshots instead of asking the venue again, so
// rendering a prompt or replaying a past decision never triggers new upstream
// requests.
func StartPositionStructure(ctx context.Context, st *store.Store) *market.PositionIndex {
	var sink market.PositionSnapshotSink
	if st != nil {
		sink = func(snapshot *market.PositionSnapshot) error {
			bins, err := json.Marshal(snapshot.Bins)
			if err != nil {
				return err
			}
			return st.MarketStructure().Save(&store.PositionBinsSnapshot{
				MarketType:       snapshot.MarketType,
				Symbol:           snapshot.Symbol,
				Timestamp:        snapshot.Timestamp,
				MarkPrice:        snapshot.MarkPrice,
				BandPct:          snapshot.BandPct,
				BinCount:         snapshot.BinCount,
				AddressesSeen:    snapshot.Coverage.AddressesSeen,
				PositionsSeen:    snapshot.Coverage.PositionsSeen,
				PositionsPlaced:  snapshot.Coverage.PositionsPlaced,
				PositionsOutside: snapshot.Coverage.PositionsOutside,
				WeightSource:     snapshot.Coverage.WeightSource,
				BinsJSON:         string(bins),
			})
		}
	}

	index := market.NewPositionIndex(sink)
	market.SetHLPositionIndex(index)
	index.Start(ctx)
	logger.Infof("📊 Holder-position index started (public venue positions, refresh every 5 minutes)")
	return index
}
