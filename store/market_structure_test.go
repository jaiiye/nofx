package store

import (
	"testing"
	"time"
)

func TestMarketStructureSnapshotRoundTrip(t *testing.T) {
	st, err := New(t.TempDir() + "/nofx.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	snapshot := &PositionBinsSnapshot{
		MarketType:       "core_perp",
		Symbol:           "BTC",
		MarkPrice:        63000,
		BandPct:          15,
		BinCount:         40,
		AddressesSeen:    120,
		PositionsSeen:    300,
		PositionsPlaced:  280,
		PositionsOutside: 20,
		WeightSource:     "notional (size x mark)",
		BinsJSON:         `[{"index":0,"longCost":1000}]`,
	}
	if err := st.MarketStructure().Save(snapshot); err != nil {
		t.Fatalf("save: %v", err)
	}

	latest, err := st.MarketStructure().GetLatest("BTC", 10)
	if err != nil {
		t.Fatalf("get latest: %v", err)
	}
	if len(latest) != 1 {
		t.Fatalf("rows = %d, want 1", len(latest))
	}
	got := latest[0]
	if got.Symbol != "BTC" || got.MarketType != "core_perp" || got.MarkPrice != 63000 {
		t.Fatalf("row = %+v", got)
	}
	if got.PositionsPlaced != 280 || got.PositionsOutside != 20 || got.AddressesSeen != 120 {
		t.Fatalf("coverage did not survive the round trip: %+v", got)
	}
	if got.BinsJSON != `[{"index":0,"longCost":1000}]` {
		t.Fatalf("bins = %q", got.BinsJSON)
	}
	if got.Timestamp.IsZero() {
		t.Fatal("a snapshot without a timestamp must be stamped on save")
	}

	// A symbol with no snapshots is empty, not an error.
	if rows, err := st.MarketStructure().GetLatest("ETH", 10); err != nil || len(rows) != 0 {
		t.Fatalf("unknown symbol = %d rows, err %v", len(rows), err)
	}
}

// LatestAt is the read the offline replay needs: the structure that was known
// when a decision was taken, not the newest one.
func TestMarketStructureLatestAt(t *testing.T) {
	st, err := New(t.TempDir() + "/nofx.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	now := time.Now().UTC()
	if err := st.MarketStructure().Save(&PositionBinsSnapshot{Symbol: "BTC", Timestamp: now.Add(-2 * time.Hour), MarkPrice: 60000}); err != nil {
		t.Fatalf("save old: %v", err)
	}
	if err := st.MarketStructure().Save(&PositionBinsSnapshot{Symbol: "BTC", Timestamp: now, MarkPrice: 63000}); err != nil {
		t.Fatalf("save new: %v", err)
	}

	at, err := st.MarketStructure().LatestAt("BTC", now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("latest at: %v", err)
	}
	if at == nil || at.MarkPrice != 60000 {
		t.Fatalf("row at -1h = %+v, want the older snapshot", at)
	}

	newest, err := st.MarketStructure().LatestAt("BTC", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("latest at now: %v", err)
	}
	if newest == nil || newest.MarkPrice != 63000 {
		t.Fatalf("row now = %+v, want the newest snapshot", newest)
	}

	missing, err := st.MarketStructure().LatestAt("BTC", now.Add(-48*time.Hour))
	if err != nil {
		t.Fatalf("latest before any snapshot: %v", err)
	}
	if missing != nil {
		t.Fatalf("expected no row before the first snapshot, got %+v", missing)
	}
}

func TestMarketStructureCleanOldRecords(t *testing.T) {
	st, err := New(t.TempDir() + "/nofx.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	now := time.Now().UTC()
	if err := st.MarketStructure().Save(&PositionBinsSnapshot{Symbol: "BTC", Timestamp: now.AddDate(0, 0, -10)}); err != nil {
		t.Fatalf("save stale: %v", err)
	}
	if err := st.MarketStructure().Save(&PositionBinsSnapshot{Symbol: "BTC", Timestamp: now}); err != nil {
		t.Fatalf("save fresh: %v", err)
	}

	removed, err := st.MarketStructure().CleanOldRecords(5)
	if err != nil {
		t.Fatalf("clean: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	rows, err := st.MarketStructure().GetLatest("BTC", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows after clean = %d, err %v", len(rows), err)
	}

	// A non-positive retention window is a no-op rather than "delete everything".
	if removed, err := st.MarketStructure().CleanOldRecords(0); err != nil || removed != 0 {
		t.Fatalf("clean(0) = %d, err %v", removed, err)
	}
}
