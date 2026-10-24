package kernel

import (
	"math"
	"strings"
	"testing"

	"nofx/provider/binance"
	"nofx/store"
)

func quantEngine(t *testing.T) *StrategyEngine {
	t.Helper()
	cfg := store.GetDefaultStrategyConfig("en")
	cfg.Indicators.EnableQuantData = true
	cfg.Indicators.EnableQuantOI = true
	return NewStrategyEngine(&cfg)
}

func syntheticKlines(count int) []binance.PricePoint {
	points := make([]binance.PricePoint, 0, count)
	for i := 0; i < count; i++ {
		points = append(points, binance.PricePoint{
			TimestampMs:   int64(i) * 300000,
			Close:         100 + float64(i),
			QuoteVolume:   1000,
			TakerBuyQuote: 600,
		})
	}
	return points
}

func TestPriceChangesFromKlines(t *testing.T) {
	points := syntheticKlines(289) // 24h of 5m candles
	changes := priceChangesFromKlines(points)

	// Current close is 388; the previous candle is 387.
	if got, want := changes["5m"], 1.0/387.0; math.Abs(got-want) > 1e-12 {
		t.Fatalf("5m change = %v, want %v", got, want)
	}
	// 12 candles back is 376.
	if got, want := changes["1h"], 12.0/376.0; math.Abs(got-want) > 1e-12 {
		t.Fatalf("1h change = %v, want %v", got, want)
	}
	// 288 candles back is 100.
	if got, want := changes["24h"], 288.0/100.0; math.Abs(got-want) > 1e-12 {
		t.Fatalf("24h change = %v, want %v", got, want)
	}
}

func TestOIDeltaFromPoints(t *testing.T) {
	points := make([]binance.OIPoint, 0, 289)
	for i := 0; i < 289; i++ {
		points = append(points, binance.OIPoint{TimestampMs: int64(i) * 300000, OIUSD: 1000 + float64(i)*10})
	}
	oi := oiDeltaFromPoints(points)
	if oi == nil {
		t.Fatal("expected OI data")
	}
	if oi.CurrentOI != 3880 {
		t.Fatalf("current OI = %v, want 3880", oi.CurrentOI)
	}
	delta := oi.Delta["1h"]
	if delta == nil {
		t.Fatal("1h delta missing")
	}
	if delta.OIDelta != 120 {
		t.Fatalf("1h delta = %v, want 120", delta.OIDelta)
	}
	if want := 120.0 / 3760.0 * 100; math.Abs(delta.OIDeltaPercent-want) > 1e-9 {
		t.Fatalf("1h delta%% = %v, want %v", delta.OIDeltaPercent, want)
	}
}

func TestTakerFlowFromKlines(t *testing.T) {
	flow := takerFlowFromKlines(syntheticKlines(20), 12)
	if flow == nil {
		t.Fatal("expected taker flow")
	}
	if flow.BuySharePct != 60 {
		t.Fatalf("buy share = %v, want 60", flow.BuySharePct)
	}
	if flow.BuyUSD != 7200 || flow.SellUSD != 4800 {
		t.Fatalf("buy/sell = %v/%v, want 7200/4800", flow.BuyUSD, flow.SellUSD)
	}
}

func TestFormatQuantDataRendersLocalFields(t *testing.T) {
	engine := quantEngine(t)
	got := engine.formatQuantData(&QuantData{
		Symbol:      "BTCUSDT",
		Price:       63000,
		PriceChange: map[string]float64{"1h": 0.0123},
		OI: map[string]*OIData{
			"binance": {
				CurrentOI: 5_000_000,
				Delta: map[string]*OIDeltaData{
					"1h": {OIDelta: 120, OIDeltaValue: 120, OIDeltaPercent: 3.1915},
				},
			},
		},
		LongShort: &LongShortData{
			AccountRatio: 1.82, AccountLongPct: 64.5,
			TopTraderRatio: 1.40, TopTraderLongPct: 58.3,
		},
		TakerFlow: &TakerFlowData{
			Window: "1h", BuyUSD: 1_200_000, SellUSD: 850_000, BuySharePct: 58.5,
		},
	})

	for _, want := range []string{
		"Price Change: 1h: +1.2300%",
		"Open Interest (binance): 5.00M",
		"OI 1h: +3.1915%",
		"Long/Short (binance): accounts 1.82x (long 64.5%) | top traders 1.40x (long 58.3%)",
		"Taker flow 1h: buy 58.5% / sell 41.5%",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("quant block missing %q:\n%s", want, got)
		}
	}
}

func TestFormatQuantDataOmitsEmptySections(t *testing.T) {
	engine := quantEngine(t)

	if got := engine.formatQuantData(&QuantData{Symbol: "BTCUSDT"}); got != "" {
		t.Fatalf("an empty quant block must be omitted, got %q", got)
	}

	got := engine.formatQuantData(&QuantData{Symbol: "BTCUSDT", LongShort: &LongShortData{AccountRatio: 1.2, AccountLongPct: 54.5}})
	if strings.Contains(got, "Open Interest") || strings.Contains(got, "Taker flow") {
		t.Fatalf("absent sections must not render:\n%s", got)
	}
	if !strings.Contains(got, "Long/Short") {
		t.Fatalf("the present section must render:\n%s", got)
	}
}
