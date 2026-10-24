package market

import (
	"testing"
	"time"

	"nofx/provider/bybit"
)

func fakeTicker(price, changePct, turnover, oi float64) bybit.Ticker {
	return bybit.Ticker{
		MarkPrice:         price,
		LastPrice:         price,
		Change24hPct:      changePct,
		Turnover24h:       turnover,
		OpenInterestValue: oi,
	}
}

func seededUniverse(now time.Time) *UniverseService {
	service := NewUniverseService()
	tickers := map[string]bybit.Ticker{
		"AAAUSDT": fakeTicker(100, 1, 1_000_000, 10_000_000),
		"BBBUSDT": fakeTicker(100, 2, 2_000_000, 20_000_000),
		"CCCUSDT": fakeTicker(100, -1, 3_000_000, 30_000_000),
		"DDDUSDT": fakeTicker(100, 3, 4_000_000, 40_000_000),
		"EEEUSDT": fakeTicker(100, 0.5, 5_000_000, 50_000_000),
		"FFFUSDT": fakeTicker(100, 1.5, 6_000_000, 60_000_000),
	}
	service.apply(tickers, now)
	return service
}

func TestScoreUniverseIsDeterministic(t *testing.T) {
	now := time.Now().UTC()
	service := seededUniverse(now)

	first := service.Coins()
	if len(first) != 6 {
		t.Fatalf("coins = %d, want 6", len(first))
	}
	// Highest turnover and OI with equal volatility should win.
	if first[0].Symbol != "FFFUSDT" {
		t.Fatalf("top = %s, want FFFUSDT", first[0].Symbol)
	}

	// Re-applying the same snapshot must produce the identical order.
	service.apply(map[string]bybit.Ticker{
		"AAAUSDT": fakeTicker(100, 1, 1_000_000, 10_000_000),
		"BBBUSDT": fakeTicker(100, 2, 2_000_000, 20_000_000),
		"CCCUSDT": fakeTicker(100, -1, 3_000_000, 30_000_000),
		"DDDUSDT": fakeTicker(100, 3, 4_000_000, 40_000_000),
		"EEEUSDT": fakeTicker(100, 0.5, 5_000_000, 50_000_000),
		"FFFUSDT": fakeTicker(100, 1.5, 6_000_000, 60_000_000),
	}, now)
	second := service.Coins()
	for i := range first {
		if first[i].Symbol != second[i].Symbol {
			t.Fatalf("order changed at %d: %s vs %s", i, first[i].Symbol, second[i].Symbol)
		}
	}
}

func TestScreenerHonoursLimitAndExcludesNonUSDT(t *testing.T) {
	now := time.Now().UTC()
	service := NewUniverseService()
	service.apply(map[string]bybit.Ticker{
		"AAAUSDT": fakeTicker(100, 1, 1_000_000, 10_000_000),
		"BBBUSDT": fakeTicker(100, 2, 2_000_000, 20_000_000),
		"CCCUSDT": fakeTicker(100, 3, 3_000_000, 30_000_000),
		"BTCPERP": fakeTicker(100, 3, 9_000_000, 90_000_000),
	}, now)

	coins := service.Screener(2)
	if len(coins) != 2 {
		t.Fatalf("screener = %d, want 2", len(coins))
	}
	for _, coin := range coins {
		if coin.Symbol == "BTCPERP" {
			t.Fatalf("a non-USDT contract leaked into the universe: %+v", coin)
		}
	}
}

func TestOIRankingUsesObservedWindow(t *testing.T) {
	base := time.Now().UTC().Add(-2 * time.Hour)
	symbols := []string{"AAAUSDT", "BBBUSDT", "CCCUSDT", "DDDUSDT", "EEEUSDT"}

	service := NewUniverseService()
	snapshot := func(at time.Time, oi map[string]float64) {
		tickers := make(map[string]bybit.Ticker, len(symbols))
		for _, symbol := range symbols {
			value := 100.0
			if oi != nil {
				if override, ok := oi[symbol]; ok {
					value = override
				}
			}
			tickers[symbol] = fakeTicker(100, 0, 1_000_000, value)
		}
		service.apply(tickers, at)
	}

	snapshot(base, nil)
	// One hour later: AAA OI rises 10%, BBB falls 5%, the rest are flat.
	snapshot(base.Add(time.Hour), map[string]float64{"AAAUSDT": 110, "BBBUSDT": 95})
	// Current snapshot two hours after the start (one hour after the baseline).
	snapshot(base.Add(2*time.Hour), map[string]float64{"AAAUSDT": 121, "BBBUSDT": 90})

	top, low, ok := service.OIRanking(time.Hour, 2)
	if !ok {
		t.Fatalf("expected a 1h OI ranking")
	}
	if len(top) != 2 || len(low) != 2 {
		t.Fatalf("top=%d low=%d, want 2 each", len(top), len(low))
	}
	if top[0].Symbol != "AAAUSDT" {
		t.Fatalf("top = %+v, want AAAUSDT", top[0])
	}
	if diff := top[0].OIDeltaPct - 10; diff > 0.001 || diff < -0.001 {
		t.Fatalf("AAA delta = %v, want 10", top[0].OIDeltaPct)
	}
	if low[0].Symbol != "BBBUSDT" {
		t.Fatalf("low = %+v, want BBBUSDT", low[0])
	}
	if diff := low[0].OIDeltaPct - (-5.263157); diff > 0.01 || diff < -0.01 {
		t.Fatalf("BBB delta = %v, want about -5.26", low[0].OIDeltaPct)
	}
}

func TestRankingOmitsUnobservedWindow(t *testing.T) {
	now := time.Now().UTC()
	service := seededUniverse(now)

	if _, _, ok := service.OIRanking(time.Hour, 5); ok {
		t.Fatal("a window that was never observed must not rank")
	}
	if _, _, ok := service.PriceRanking(time.Hour, 5); ok {
		t.Fatal("a window that was never observed must not rank")
	}
}

func TestPriceRanking24hUsesTickerChange(t *testing.T) {
	now := time.Now().UTC()
	service := seededUniverse(now)

	top, low, ok := service.PriceRanking(24*time.Hour, 2)
	if !ok {
		t.Fatal("24h change is available from the ticker, so the ranking must exist")
	}
	if top[0].Symbol != "DDDUSDT" {
		t.Fatalf("top = %+v, want DDDUSDT (highest change)", top[0])
	}
	if low[0].Symbol != "CCCUSDT" {
		t.Fatalf("low = %+v, want CCCUSDT (lowest change)", low[0])
	}
}

func TestSampleAtRejectsStaleBaseline(t *testing.T) {
	now := time.Now().UTC()
	history := []universeSample{{At: now.Add(-2 * time.Hour), Price: 100, OIUSD: 100}}

	// Target is one hour ago but the only sample is two hours old: outside the
	// tolerance, so the window is not usable.
	if _, ok := sampleAt(history, now.Add(-time.Hour)); ok {
		t.Fatal("a stale baseline must not satisfy a shorter window")
	}
	if got, ok := sampleAt(history, now.Add(-2*time.Hour)); !ok || got.Price != 100 {
		t.Fatalf("exact-window sample = %+v, ok=%v", got, ok)
	}
}
