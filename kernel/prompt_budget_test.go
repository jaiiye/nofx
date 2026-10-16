package kernel

import (
	"testing"
	"time"

	"nofx/market"
	"nofx/store"
)

// syntheticPromptContext builds a representative decision context: 1 position
// + 4 candidates, each with three timeframes of 20 bars and the full
// indicator set, plus microstructure. Used to measure and guard prompt size.
func syntheticPromptContext() (*store.StrategyConfig, *Context) {
	return syntheticPromptContextWithBars(200)
}

func syntheticPromptContextWithBars(bars int) (*store.StrategyConfig, *Context) {
	cfg := store.GetDefaultStrategyConfig("en")
	cfg.Indicators.EnableEMA = true
	cfg.Indicators.EnableMACD = true
	cfg.Indicators.EnableRSI = true
	cfg.Indicators.EnableATR = true
	cfg.Indicators.EnableBOLL = true
	cfg.Indicators.EnableADX = true
	cfg.Indicators.EnableSupertrend = true

	ctx := &Context{
		CurrentTime:       time.Now().Format(time.RFC3339),
		CallCount:         1,
		Account:           AccountInfo{TotalEquity: 1000, AvailableBalance: 800, PositionCount: 1},
		MarketDataMap:     map[string]*market.Data{},
		MicrostructureMap: map[string]*market.MarketMicrostructure{},
	}
	for i, symbol := range []string{"BTCUSDT", "ETHUSDT", "SOLUSDT", "DOGEUSDT", "ADAUSDT"} {
		data := &market.Data{Symbol: symbol, CurrentPrice: 100}
		data.TimeframeData = map[string]*market.TimeframeSeriesData{}
		for _, tf := range []string{"5m", "15m", "1h"} {
			td := &market.TimeframeSeriesData{Timeframe: tf}
			for b := 0; b < bars; b++ {
				td.Klines = append(td.Klines, market.KlineBar{
					Time: int64(b) * 300_000, Open: 100, High: 101, Low: 99, Close: 100.5, Volume: 1234.5678,
				})
				td.EMA20Values = append(td.EMA20Values, 100.1234)
			}
			data.TimeframeData[tf] = td
		}
		ctx.MarketDataMap[symbol] = data
		ctx.MicrostructureMap[symbol] = &market.MarketMicrostructure{
			SpreadPct: 0.01, SpreadBps: 1, Imbalance: 0.2, ImbalanceSide: "BUY",
			BidDepthUSD: 123456, AskDepthUSD: 234567,
			SupportLevels: []float64{99.1, 98.2, 97.3}, ResistanceLevels: []float64{101.1, 102.2, 103.3},
			LiquidityScore: 85, VWAP: 100.2, VWAPDeviationPct: 0.3,
		}
		if i == 0 {
			ctx.Positions = []PositionInfo{{Symbol: symbol, Side: "long", EntryPrice: 99, MarkPrice: 100, Quantity: 1, Leverage: 5}}
		} else {
			ctx.CandidateCoins = append(ctx.CandidateCoins, CandidateCoin{Symbol: symbol})
		}
	}
	return &cfg, ctx
}

// Prompt size is governed by the DISPLAY cap, not by the analysis window: an
// hourly strategy needs a long window (200+ bars for EMA200 / multi-day
// structure) while the model only needs the recent shape. The invariant below
// is what keeps a longer window from inflating every prompt — a magic-number
// budget would pass even if that property broke.
func TestPromptSizeIndependentOfAnalysisWindow(t *testing.T) {
	sizeFor := func(bars int) (total, candidate, position int) {
		cfg, ctx := syntheticPromptContextWithBars(bars)
		engine := NewStrategyEngine(cfg)
		system := engine.BuildSystemPrompt(ctx.Account.TotalEquity, "balanced")
		user := engine.BuildUserPrompt(ctx)
		return len(system) + len(user),
			len(engine.formatMarketData(ctx.MarketDataMap["ETHUSDT"], false)),
			len(engine.formatMarketData(ctx.MarketDataMap["BTCUSDT"], true))
	}

	small, smallCand, smallPos := sizeFor(200)
	large, largeCand, largePos := sizeFor(400)
	t.Logf("200-bar window: total=%d candidate=%d position=%d", small, smallCand, smallPos)
	t.Logf("400-bar window: total=%d candidate=%d position=%d", large, largeCand, largePos)

	// Doubling the window must not grow the prompt: only the display cap's
	// worth of bars/values is printed either way.
	if large > small*105/100 {
		t.Fatalf("prompt grew with the analysis window: %d -> %d bytes", small, large)
	}
	if largeCand > smallCand*105/100 || largePos > smallPos*105/100 {
		t.Fatalf("per-symbol block grew with the window: candidate %d->%d, position %d->%d",
			smallCand, largeCand, smallPos, largePos)
	}

	// Loose absolute guard for the reference context (5 symbols, 3 timeframes,
	// every indicator enabled): a large jump here means something is printed
	// that should be capped.
	const budget = 40_000
	if small > budget {
		t.Fatalf("reference prompt too large: %d bytes > %d", small, budget)
	}
}
