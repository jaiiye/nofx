package market

import (
	"testing"
	"time"

	"nofx/provider/hyperliquid"
)

func TestVenueFromExchange(t *testing.T) {
	cases := map[string]Venue{
		"hyperliquid":     VenueHyperliquid,
		"hyperliquid-xyz": VenueHyperliquid,
		"xyz":             VenueHyperliquid,
		"bybit":           VenueBybit,
		"okx":             VenueOKX,
		"binance":         VenueBinance,
		"bitget":          VenueBinance,
		"":                VenueBinance,
	}
	for exchange, want := range cases {
		if got := VenueFromExchange(exchange); got != want {
			t.Fatalf("VenueFromExchange(%q) = %s, want %s", exchange, got, want)
		}
	}
}

// Hyperliquid supports 3m and 2h (the old mapping silently degraded them) and
// has no 6h/3d.
func TestHyperliquidTimeframeMapping(t *testing.T) {
	supported := map[string]string{
		"1m": "1m", "3m": "3m", "5m": "5m", "15m": "15m", "30m": "30m",
		"1h": "1h", "2h": "2h", "4h": "4h", "8h": "8h", "12h": "12h",
		"1d": "1d", "1w": "1w", "1M": "1M",
	}
	for tf, want := range supported {
		if got := hyperliquid.MapTimeframe(tf); got != want {
			t.Fatalf("MapTimeframe(%q) = %q, want %q", tf, got, want)
		}
	}
	for _, tf := range []string{"6h", "3d", "5s"} {
		if got := hyperliquid.MapTimeframe(tf); got != "" {
			t.Fatalf("MapTimeframe(%q) = %q, want unsupported", tf, got)
		}
	}
}

func TestHyperliquidCandlesToKlines(t *testing.T) {
	candles := []hyperliquid.Candle{
		{OpenTime: 1000, CloseTime: 2000, Open: "10", High: "12", Low: "9", Close: "11", Volume: "5"},
	}
	klines := hyperliquidCandlesToKlines(candles)
	if len(klines) != 1 {
		t.Fatalf("klines = %d", len(klines))
	}
	got := klines[0]
	if got.Open != 10 || got.High != 12 || got.Low != 9 || got.Close != 11 || got.Volume != 5 {
		t.Fatalf("candle conversion = %+v", got)
	}
	if got.OpenTime != 1000 || got.CloseTime != 2000 {
		t.Fatalf("times = %+v", got)
	}
}

func TestKlineCacheKeyDistinguishesVenues(t *testing.T) {
	keyA := klineCacheKey{Venue: VenueBinance, Symbol: "BTCUSDT", Timeframe: "1h", Limit: 200}
	keyB := klineCacheKey{Venue: VenueHyperliquid, Symbol: "BTCUSDT", Timeframe: "1h", Limit: 200}
	if keyA == keyB {
		t.Fatal("venues must not share a cache entry")
	}
	_ = time.Second // keep the time import meaningful if the test grows
}
