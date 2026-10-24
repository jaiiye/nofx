package backtest

import (
	"testing"
	"time"

	"nofx/store"
)

func TestStoreRegimeSourceCascadeAndCrowding(t *testing.T) {
	st, err := store.New(t.TempDir() + "/nofx.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)

	// Four quiet 15m windows of $100, then a $1000 burst in the last window.
	rows := make([]*store.LiquidationRow, 0, 14)
	for window := 1; window <= 4; window++ {
		at := now.Add(-time.Duration(window) * regimeCascadeWindow).Add(-time.Minute)
		rows = append(rows, &store.LiquidationRow{
			Venue: "binance", Symbol: "BTCUSDT", VenueSymbol: "BTCUSDT",
			Timestamp: at, Side: "long", Price: 100, Qty: 1, NotionalUSD: 100,
		})
	}
	for i := 0; i < 10; i++ {
		rows = append(rows, &store.LiquidationRow{
			Venue: "binance", Symbol: "BTCUSDT", VenueSymbol: "BTCUSDT",
			Timestamp: now.Add(-time.Duration(i) * time.Second), Side: "long",
			Price: 100, Qty: 1, NotionalUSD: 100,
		})
	}
	if err := st.MarketMetrics().SaveLiquidations(rows); err != nil {
		t.Fatalf("save liquidations: %v", err)
	}
	if err := st.MarketMetrics().SaveVenueMetrics([]*store.VenueMetricsRow{{
		Venue: "hyperliquid", Symbol: "BTCUSDT", Timestamp: now.Add(-time.Minute),
		FundingRate1hPct: 0.01, FundingIntervalH: 1, SourceOK: true,
	}}); err != nil {
		t.Fatalf("save venue metrics: %v", err)
	}

	src := StoreRegimeSource(st)
	got := src("BTC", now)
	if !got.Cascade {
		t.Fatalf("expected a cascade: %+v", got)
	}
	if got.CrowdedSide != "long" {
		t.Fatalf("crowded side = %q, want long", got.CrowdedSide)
	}

	// A decision taken before the burst must not be labelled with it.
	early := src("BTC", now.Add(-30*time.Minute))
	if early.Cascade {
		t.Fatalf("cascade leaked into an earlier decision: %+v", early)
	}
}

func TestByRegimeLabelsAndOrder(t *testing.T) {
	trades := []Trade{
		{Symbol: "BTCUSDT", NetPnL: 10, EntryCascade: true, EntryCrowdedSide: "long"},
		{Symbol: "BTCUSDT", NetPnL: -5, EntryCrowdedSide: "long"},
		{Symbol: "ETHUSDT", NetPnL: 3, EntryCrowdedSide: "short"},
		{Symbol: "ETHUSDT", NetPnL: 1},
	}

	got := byRegime(trades)
	if len(got) != 4 {
		t.Fatalf("regimes = %d, want 4", len(got))
	}
	wantOrder := []string{"cascade", "crowded_long", "crowded_short", "calm"}
	for i, want := range wantOrder {
		if got[i].Regime != want {
			t.Fatalf("regime[%d] = %q, want %q", i, got[i].Regime, want)
		}
	}
	if got[1].Trades != 1 || got[1].WinRate != 0 {
		t.Fatalf("crowded_long = %+v", got[1])
	}
	if got[3].WinRate != 100 {
		t.Fatalf("calm = %+v", got[3])
	}
}
