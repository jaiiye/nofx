package trader

import (
	"encoding/json"
	"testing"
	"time"

	"nofx/market"
	"nofx/store"
)

func TestLoadPositionProfilesReadsFreshSnapshotsOnly(t *testing.T) {
	st, err := store.New(t.TempDir() + "/nofx.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	bins, coverage := market.BinPositions([]market.AddressPosition{
		{Address: "0xaaa", Side: "long", Size: 10, EntryPrice: 97, LiquidationPx: 92},
		{Address: "0xbbb", Side: "short", Size: 4, EntryPrice: 108},
	}, 100, 10, 4)
	raw, err := json.Marshal(bins)
	if err != nil {
		t.Fatalf("marshal bins: %v", err)
	}
	save := func(symbol string, at time.Time, binsJSON string) {
		t.Helper()
		if err := st.MarketStructure().Save(&store.PositionBinsSnapshot{
			Symbol:           symbol,
			MarketType:       "core_perp",
			MarkPrice:        100,
			BandPct:          10,
			BinCount:         4,
			AddressesSeen:    coverage.AddressesSeen,
			PositionsSeen:    coverage.PositionsSeen,
			PositionsPlaced:  coverage.PositionsPlaced,
			PositionsOutside: coverage.PositionsOutside,
			WeightSource:     coverage.WeightSource,
			BinsJSON:         binsJSON,
			Timestamp:        at,
		}); err != nil {
			t.Fatalf("save %s: %v", symbol, err)
		}
	}

	// Snapshots are stored under the canonical symbol form (what the engine hands
	// the indexer), and the caller's symbols are normalized the same way before the
	// lookup while the result stays keyed by what the caller passed.
	save(market.Normalize("BTC"), time.Now().UTC(), string(raw))
	save(market.Normalize("SOL"), time.Now().UTC().Add(-2*market.HLPositionRefreshInterval()), string(raw))
	save(market.Normalize("ETH"), time.Now().UTC(), "not json")

	at := &AutoTrader{id: "t1", name: "profile-test", store: st}

	profiles := at.loadPositionProfiles([]string{"btc"})
	profile, ok := profiles["btc"]
	if !ok || profile == nil {
		t.Fatalf("a fresh snapshot must load under the caller's symbol, got %v", profiles)
	}
	if profile.Coverage.PositionsPlaced != coverage.PositionsPlaced || profile.TotalNotional <= 0 {
		t.Fatalf("profile lost its data: %+v", profile)
	}
	if profile.CostPOC <= 0 {
		t.Fatalf("profile carries no levels: %+v", profile)
	}

	// Stale snapshots are withheld: structure that no longer describes the market
	// is worse than none, because nothing in it says it has aged.
	if stale := at.loadPositionProfiles([]string{"SOL"}); len(stale) != 0 {
		t.Fatalf("a snapshot older than two refreshes must be withheld, got %d", len(stale))
	}
	// Unreadable buckets are dropped rather than shown as empty structure.
	if broken := at.loadPositionProfiles([]string{"ETH"}); len(broken) != 0 {
		t.Fatalf("unreadable buckets must be dropped, got %d", len(broken))
	}
	// A symbol that was never collected is simply absent.
	if missing := at.loadPositionProfiles([]string{"DOGE"}); len(missing) != 0 {
		t.Fatalf("an uncollected symbol produced %d profiles", len(missing))
	}

	// A trader without a store returns nothing instead of failing.
	empty := &AutoTrader{id: "t2", name: "no-store"}
	if profiles := empty.loadPositionProfiles([]string{"BTC"}); profiles != nil {
		t.Fatalf("a trader without a store must return no profiles, got %v", profiles)
	}
	if profiles := at.loadPositionProfiles(nil); profiles != nil {
		t.Fatalf("no symbols means no profiles, got %v", profiles)
	}
}
