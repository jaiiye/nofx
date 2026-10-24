package store

import (
	"testing"
	"time"
)

func newMetricsTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := New(t.TempDir() + "/nofx.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return st
}

func TestVenueMetricsRoundTripAndDedupe(t *testing.T) {
	st := newMetricsTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	rows := []*VenueMetricsRow{
		{Venue: "hyperliquid", Symbol: "BTC", VenueSymbol: "BTC", Timestamp: now, MarkPrice: 63000, OIUSD: 1.2e9, FundingRate1hPct: 0.0011, FundingIntervalH: 1, SourceOK: true},
		{Venue: "binance", Symbol: "BTC", VenueSymbol: "BTCUSDT", Timestamp: now, MarkPrice: 63010, OIUSD: 4.5e9, FundingRate1hPct: -0.0002, FundingIntervalH: 8, SourceOK: true},
	}
	if err := st.MarketMetrics().SaveVenueMetrics(rows); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := st.MarketMetrics().LatestAt("hyperliquid", "BTC", now)
	if err != nil {
		t.Fatalf("latest hyperliquid: %v", err)
	}
	if got == nil || got.OIUSD != 1.2e9 {
		t.Fatalf("hyperliquid row = %+v", got)
	}
	binance, err := st.MarketMetrics().LatestAt("binance", "BTC", now)
	if err != nil {
		t.Fatalf("latest binance: %v", err)
	}
	if binance == nil || binance.OIUSD != 4.5e9 {
		t.Fatalf("binance row = %+v", binance)
	}

	// A retry after a partial failure must not duplicate the same reading.
	if err := st.MarketMetrics().SaveVenueMetrics(rows); err != nil {
		t.Fatalf("save retry: %v", err)
	}
	if count := countVenueMetrics(t, st, "BTC"); count != 2 {
		t.Fatalf("rows after retry = %d, want 2", count)
	}

	// Degraded rows are stored but the point-in-time read never serves them.
	if err := st.MarketMetrics().SaveVenueMetrics([]*VenueMetricsRow{
		{Venue: "bybit", Symbol: "BTC", Timestamp: now, SourceOK: false},
	}); err != nil {
		t.Fatalf("save degraded: %v", err)
	}
	if count := countVenueMetrics(t, st, "BTC"); count != 3 {
		t.Fatalf("rows after degraded = %d, want 3 stored", count)
	}
	bybit, err := st.MarketMetrics().LatestAt("bybit", "BTC", now)
	if err != nil {
		t.Fatalf("latest degraded: %v", err)
	}
	if bybit != nil {
		t.Fatalf("a degraded row must not be served, got %+v", bybit)
	}
}

func countVenueMetrics(t *testing.T, st *Store, symbol string) int64 {
	t.Helper()
	var count int64
	if err := st.GormDB().Model(&VenueMetricsRow{}).Where("symbol = ?", symbol).Count(&count).Error; err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return count
}

func TestVenueMetricsLatestAtIsPointInTime(t *testing.T) {
	st := newMetricsTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	if err := st.MarketMetrics().SaveVenueMetrics([]*VenueMetricsRow{
		{Venue: "hyperliquid", Symbol: "BTC", Timestamp: now.Add(-2 * time.Hour), MarkPrice: 60000, SourceOK: true},
		{Venue: "hyperliquid", Symbol: "BTC", Timestamp: now, MarkPrice: 63000, SourceOK: true},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	at, err := st.MarketMetrics().LatestAt("hyperliquid", "BTC", now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("latest at: %v", err)
	}
	if at == nil || at.MarkPrice != 60000 {
		t.Fatalf("row at -1h = %+v, want the older reading", at)
	}

	newest, err := st.MarketMetrics().LatestAt("hyperliquid", "BTC", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("latest at now: %v", err)
	}
	if newest == nil || newest.MarkPrice != 63000 {
		t.Fatalf("row now = %+v, want the newest reading", newest)
	}

	missing, err := st.MarketMetrics().LatestAt("bybit", "BTC", now)
	if err != nil {
		t.Fatalf("latest missing venue: %v", err)
	}
	if missing != nil {
		t.Fatalf("expected no row for an unseen venue, got %+v", missing)
	}
}

func TestLiquidationSumAndDedupe(t *testing.T) {
	st := newMetricsTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	prints := []*LiquidationRow{
		{Venue: "binance", Symbol: "BTC", VenueSymbol: "BTCUSDT", Timestamp: now.Add(-10 * time.Minute), Side: "long", Price: 62000, Qty: 2, NotionalUSD: 124000},
		{Venue: "binance", Symbol: "BTC", VenueSymbol: "BTCUSDT", Timestamp: now.Add(-5 * time.Minute), Side: "long", Price: 62000, Qty: 1, NotionalUSD: 62000},
		{Venue: "bybit", Symbol: "BTC", VenueSymbol: "BTCUSDT", Timestamp: now.Add(-5 * time.Minute), Side: "short", Price: 62100, Qty: 3, NotionalUSD: 186300},
	}
	if err := st.MarketMetrics().SaveLiquidations(prints); err != nil {
		t.Fatalf("save: %v", err)
	}

	long, short, count, err := st.MarketMetrics().LiquidationSum("BTC", now.Add(-time.Hour), now)
	if err != nil {
		t.Fatalf("sum: %v", err)
	}
	if long != 186000 || short != 186300 || count != 3 {
		t.Fatalf("long=%v short=%v count=%d", long, short, count)
	}

	// A websocket replay after reconnect must not double-count.
	if err := st.MarketMetrics().SaveLiquidations(prints); err != nil {
		t.Fatalf("save replay: %v", err)
	}
	_, _, count, err = st.MarketMetrics().LiquidationSum("BTC", now.Add(-time.Hour), now)
	if err != nil {
		t.Fatalf("sum after replay: %v", err)
	}
	if count != 3 {
		t.Fatalf("count after replay = %d, want 3", count)
	}
}

func TestMarketMetricsCleanOldRecords(t *testing.T) {
	st := newMetricsTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	if err := st.MarketMetrics().SaveVenueMetrics([]*VenueMetricsRow{
		{Venue: "hyperliquid", Symbol: "BTC", Timestamp: now.AddDate(0, 0, -10), SourceOK: true},
		{Venue: "hyperliquid", Symbol: "BTC", Timestamp: now, SourceOK: true},
	}); err != nil {
		t.Fatalf("save metrics: %v", err)
	}
	if err := st.MarketMetrics().SaveLiquidations([]*LiquidationRow{
		{Venue: "binance", Symbol: "BTC", VenueSymbol: "BTCUSDT", Timestamp: now.AddDate(0, 0, -10), Side: "long"},
		{Venue: "binance", Symbol: "BTC", VenueSymbol: "BTCUSDT", Timestamp: now, Side: "long"},
	}); err != nil {
		t.Fatalf("save prints: %v", err)
	}

	removed, err := st.MarketMetrics().CleanOldRecords(5)
	if err != nil {
		t.Fatalf("clean: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}

	if removed, err := st.MarketMetrics().CleanOldRecords(0); err != nil || removed != 0 {
		t.Fatalf("clean(0) = %d, err %v", removed, err)
	}
}
