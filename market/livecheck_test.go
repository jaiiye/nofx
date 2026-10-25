//go:build livecheck

// Live endpoint checks. They are excluded from the normal test run because they
// need network access and can take a minute:
//
//	go test -tags livecheck ./market/ -run TestLive -v -timeout 180s
//
// TestLiveWSConnectivity distinguishes "the market is quiet" from "websocket
// egress is blocked" — on a network that blocks Binance WS the probe reports it
// while the Bybit ping still proves the transport works.
package market

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"nofx/provider/bybit"
)

// TestLiveWSConnectivity separates "the venue is quiet" from "websocket egress
// is blocked": a trade stream must deliver messages immediately, and Bybit must
// answer a ping. Without this probe a zero-liquidation run is ambiguous.
func TestLiveWSConnectivity(t *testing.T) {
	binanceConn, _, err := websocket.DefaultDialer.Dial("wss://fstream.binance.com/ws/btcusdt@aggTrade", nil)
	if err != nil {
		t.Logf("binance ws dial failed (egress may be restricted here): %v", err)
	} else {
		defer binanceConn.Close()
		binanceConn.SetReadDeadline(time.Now().Add(15 * time.Second))
		received := 0
		for i := 0; i < 3; i++ {
			if _, _, err := binanceConn.ReadMessage(); err != nil {
				t.Logf("binance ws read %d failed (egress may be restricted here): %v", i, err)
				break
			}
			received++
		}
		if received == 3 {
			t.Log("binance ws: 3 trade messages received")
		}
	}

	bybitConn, _, err := websocket.DefaultDialer.Dial("wss://stream.bybit.com/v5/public/linear", nil)
	if err != nil {
		t.Fatalf("bybit ws dial: %v", err)
	}
	defer bybitConn.Close()
	if err := bybitConn.WriteJSON(map[string]string{"op": "ping"}); err != nil {
		t.Fatalf("bybit ping: %v", err)
	}
	bybitConn.SetReadDeadline(time.Now().Add(15 * time.Second))
	for {
		_, message, err := bybitConn.ReadMessage()
		if err != nil {
			t.Fatalf("bybit pong: %v", err)
		}
		if strings.Contains(string(message), "pong") {
			t.Log("bybit ws: pong received")
			return
		}
	}
}

func TestLiveUniverse(t *testing.T) {
	service := NewUniverseService()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	service.poll(ctx)
	coins := service.Screener(5)
	t.Logf("universe: %d contracts", len(service.Coins()))
	for _, coin := range coins {
		t.Logf("  %-14s score=%+.2f vol=$%.0f oi=$%.0f chg24h=%+.2f%%",
			coin.Symbol, coin.Score, coin.Turnover24h, coin.OIUSD, coin.Change24hPct)
	}
	if len(coins) == 0 {
		t.Fatal("screener returned nothing")
	}

	// Bootstrap runs in the background; wait for the 1h window to become usable.
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		top, low, ok := service.OIRanking(time.Hour, 3)
		if ok {
			t.Logf("1h OI top: %+v", top)
			t.Logf("1h OI low: %+v", low)
			return
		}
		time.Sleep(5 * time.Second)
	}
	t.Fatal("1h OI ranking still unavailable after bootstrap window")
}

func TestLiveTakerFlowRanking(t *testing.T) {
	service := NewUniverseService()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	service.poll(ctx)
	ranking, ok := service.TakerFlowRanking(time.Hour, 3)
	if !ok {
		t.Fatal("taker flow ranking unavailable")
	}
	t.Logf("window=%s", ranking.Window)
	for _, item := range ranking.Top {
		t.Logf("  buy-dominant %-12s %.1f%% ($%.0f)", item.Symbol, item.BuySharePct, item.QuoteVolume)
	}
	for _, item := range ranking.Low {
		t.Logf("  sell-dominant %-12s %.1f%% ($%.0f)", item.Symbol, item.BuySharePct, item.QuoteVolume)
	}
}

func TestLiveHyperliquidUniverse(t *testing.T) {
	service := NewHyperliquidUniverseService()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	service.poll(ctx)
	coins := service.Coins()
	if len(coins) < 20 {
		t.Fatalf("HL universe = %d, want a broad set", len(coins))
	}
	hasXYZ := false
	for _, coin := range coins {
		if strings.Contains(coin.Symbol, ":") {
			hasXYZ = true
			break
		}
	}
	if !hasXYZ {
		t.Fatal("HIP-3 assets missing from the HL universe")
	}
	t.Logf("HL universe: %d assets (xyz included=%v)", len(coins), hasXYZ)
	for _, coin := range service.Screener(5) {
		t.Logf("  %-14s score=%+.2f vol=$%.0f oi=$%.0f chg24h=%+.2f%%",
			coin.Symbol, coin.Score, coin.Turnover24h, coin.OIUSD, coin.Change24hPct)
	}

	// 24h price ranking comes from prevDayPx and is available immediately.
	top, low, ok := service.PriceRanking(24*time.Hour, 3)
	if !ok {
		t.Fatal("24h HL price ranking unavailable")
	}
	t.Logf("HL 24h price top: %+v", top)
	t.Logf("HL 24h price low: %+v", low)

	// OI ranking needs local snapshots; on a cold start it must be omitted.
	if _, _, ok := service.OIRanking(time.Hour, 3); ok {
		t.Fatal("1h OI ranking should be unavailable on a cold start")
	}
}

func TestLiveHyperliquidKlines(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	spot, err := GetKlinesForVenue(ctx, VenueHyperliquid, "BTC", "3m", 200)
	if err != nil {
		t.Fatalf("hl 3m klines: %v", err)
	}
	if len(spot) < 100 {
		t.Fatalf("hl 3m klines = %d, want >=100", len(spot))
	}
	t.Logf("HL BTC 3m: %d bars, last close=%.2f, last openTime=%s",
		len(spot), spot[len(spot)-1].Close, time.UnixMilli(spot[len(spot)-1].OpenTime).UTC().Format(time.RFC3339))

	// A range longer than one candleSnapshot page exercises the backward pager.
	end := time.Now().UTC()
	start := end.Add(-30 * 24 * time.Hour)
	history, err := GetKlinesRangeForVenue(ctx, VenueHyperliquid, "BTC", "3m", start, end)
	if err != nil {
		t.Fatalf("hl 3m range: %v", err)
	}
	t.Logf("HL BTC 3m range 30d: %d bars, first=%s", len(history),
		time.UnixMilli(history[0].OpenTime).UTC().Format(time.RFC3339))
	if len(history) < 5000 {
		t.Fatalf("expected the pager to cover more than one page, got %d", len(history))
	}
}

func TestLiveVenueMetrics(t *testing.T) {
	collector := NewVenueMetricsCollector(nil)
	collector.SetSymbols([]string{"BTC", "ETH"})
	collector.poll(context.Background())

	for _, symbol := range []string{"BTC", "ETH"} {
		reading, ok := collector.Snapshot(symbol)
		if !ok {
			t.Fatalf("%s: no cross-venue reading", symbol)
		}
		t.Logf("%s: venues=%d totalOI=$%.0f spread=%+.4f%%/h pair=%s crowded=%q",
			symbol, len(reading.Venues), reading.OITotalUSD, reading.FundingSpread1hPct,
			reading.FundingSpreadPair, reading.CrowdedSide)
		for _, venue := range reading.Venues {
			t.Logf("  %-12s oi=$%.0f funding=%+.4f%%/h interval=%gh assumed=%v premium=%+.3f%%",
				venue.Venue, venue.OIUSD, venue.Funding1hPct, venue.FundingIntervalH,
				venue.FundingAssumed, venue.PremiumPct)
		}
	}
}

func TestLiveVenueMetricsSinkReceivesBatch(t *testing.T) {
	var batches []*VenueMetricsSnapshot
	collector := NewVenueMetricsCollector(func(snapshot *VenueMetricsSnapshot) error {
		batches = append(batches, snapshot)
		return nil
	})
	collector.SetSymbols([]string{"BTC", "ETH"})

	// The collector persists every fifth poll; five polls therefore exercise the
	// exact code path the kernel sink uses.
	for i := 0; i < 5; i++ {
		collector.poll(context.Background())
	}

	if len(batches) == 0 {
		t.Fatal("no batch reached the sink after five polls")
	}
	for _, batch := range batches {
		t.Logf("batch: %d quotes at %s", len(batch.Quotes), batch.Timestamp.Format(time.RFC3339))
		for _, quote := range batch.Quotes {
			if quote.Symbol == "BTCUSDT" && quote.Venue == "hyperliquid" {
				t.Logf("  HL BTC: oi=$%.0f funding=%+.4f%%/h interval=%gh ok=%v",
					quote.OIUSD, quote.FundingRate, quote.FundingIntervalH, quote.SourceOK)
			}
		}
	}
}

func TestLiveLiquidationStream(t *testing.T) {
	watcher := NewLiquidationWatcher(nil)

	// Subscribe to a wide slice of the market: liquidations of majors alone can
	// be silent for minutes, which would make a short run inconclusive about the
	// ingestion path.
	client := bybit.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()
	tickers, err := client.Tickers(ctx)
	if err != nil {
		t.Fatalf("bybit tickers: %v", err)
	}
	symbols := make([]string, 0, len(tickers))
	for symbol := range tickers {
		symbols = append(symbols, symbol)
	}
	t.Logf("watching %d Bybit symbols", len(symbols))
	watcher.SetSymbols(symbols)

	watcher.start(ctx)

	for i := 0; i < 6; i++ {
		select {
		case <-ctx.Done():
			break
		case <-time.After(10 * time.Second):
			count, age := watcher.Coverage()
			t.Logf("t+%2ds: symbols with prints=%d newestAge=%s received=%v",
				(i+1)*10, count, age.Round(time.Second), watcher.ReceivedCounts())
		}
	}

	watcher.mu.RLock()
	for key, message := range watcher.lastErr {
		t.Logf("feed error [%s]: %s", key, message)
	}
	watcher.mu.RUnlock()
	t.Logf("received before symbol filter: %v", watcher.ReceivedCounts())

	watcher.pendingMu.Lock()
	pending := len(watcher.pending)
	watcher.pendingMu.Unlock()
	if pending > 0 {
		t.Logf("pending prints in buffer: %d", pending)
	}
}
