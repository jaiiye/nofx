//go:build livecheck

// Live checks for the local quant path:
//
//	go test -tags livecheck ./kernel/ -run TestLiveQuantData -v

package kernel

import (
	"testing"

	"nofx/store"
)

func TestLiveQuantData(t *testing.T) {
	cfg := store.GetDefaultStrategyConfig("en")
	cfg.Indicators.EnableQuantData = true
	cfg.Indicators.EnableQuantOI = true
	// The default config is a Hyperliquid universe, which intentionally skips
	// exchange quant data. Use a static Binance-listed symbol for this probe.
	cfg.CoinSource.SourceType = "static"
	cfg.CoinSource.StaticCoins = []string{"BTCUSDT"}
	cfg.CoinSource.UseHyperAll = false
	cfg.CoinSource.UseHyperMain = false
	engine := NewStrategyEngine(&cfg)

	data, err := engine.FetchQuantData("BTCUSDT")
	if err != nil {
		t.Fatalf("quant data: %v", err)
	}
	if data == nil {
		t.Fatal("quant data is nil")
	}
	t.Logf("price=%.2f changes=%v", data.Price, data.PriceChange)
	for exchange, oi := range data.OI {
		t.Logf("%s OI=$%.0f", exchange, oi.CurrentOI)
		for _, window := range []string{"5m", "1h", "4h", "24h"} {
			if delta, ok := oi.Delta[window]; ok {
				t.Logf("  OI %s: %+.3f%%", window, delta.OIDeltaPercent)
			}
		}
	}
	if data.LongShort != nil {
		t.Logf("long/short: accounts %.2fx (long %.1f%%) | top %.2fx (long %.1f%%)",
			data.LongShort.AccountRatio, data.LongShort.AccountLongPct,
			data.LongShort.TopTraderRatio, data.LongShort.TopTraderLongPct)
	}
	if data.TakerFlow != nil {
		t.Logf("taker %s: buy %.1f%% ($%.0f) / sell $%.0f",
			data.TakerFlow.Window, data.TakerFlow.BuySharePct, data.TakerFlow.BuyUSD, data.TakerFlow.SellUSD)
	}
	t.Logf("prompt block:\n%s", engine.formatQuantData(data))
}
