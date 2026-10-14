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
			for b := 0; b < 20; b++ {
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

// TestPromptBudget guards against prompt bloat: the synthetic 1-position +
// 4-candidate context below is the yardstick. If a change pushes the system +
// user prompt past this budget, either the change is genuinely worth ~KB of
// per-decision tokens or it is redundant data that should be trimmed. Run with
// -v to see the current sizes.
func TestPromptBudget(t *testing.T) {
	cfg, ctx := syntheticPromptContext()
	engine := NewStrategyEngine(cfg)

	system := engine.BuildSystemPrompt(ctx.Account.TotalEquity, "balanced")
	user := engine.BuildUserPrompt(ctx)
	total := len(system) + len(user)

	t.Logf("system=%d bytes, user=%d bytes, total=%d bytes", len(system), len(user), total)
	t.Logf("one candidate block=%d bytes", len(engine.formatMarketData(ctx.MarketDataMap["ETHUSDT"], false)))
	t.Logf("one position block=%d bytes", len(engine.formatMarketData(ctx.MarketDataMap["BTCUSDT"], true)))

	const budget = 26_000
	if total > budget {
		t.Fatalf("prompt budget exceeded: %d bytes > %d (system %d + user %d)", total, budget, len(system), len(user))
	}
}
