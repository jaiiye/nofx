package kernel

import (
	"fmt"
	"testing"
	"time"

	"nofx/market"
	"nofx/store"
)

// The studio's token estimate is a pure config heuristic (there is no market data
// at edit time), so it can only stay honest if it is measured against the prompt
// the engine actually builds. This file is that measurement.

// fullIndicatorSeries populates the configured series the way the market layer
// does: the FULL window, which the prompt writer then trims to the display cap.
func fullIndicatorSeries(bars int) []market.IndicatorSeries {
	series := func(label string, count int) market.IndicatorSeries {
		values := make([]float64, 0, count)
		for i := 0; i < count; i++ {
			values = append(values, 100.123456)
		}
		return market.IndicatorSeries{Label: label, Values: values}
	}
	return []market.IndicatorSeries{
		series("EMA(50)", bars),
		series("EMA(200)", bars),
		series("MACD(12,26)", bars),
		series("RSI(14)", bars),
		series("BOLL_UPPER(20,2.0)", bars),
		series("BOLL_MIDDLE(20,2.0)", bars),
		series("BOLL_LOWER(20,2.0)", bars),
		series("ATR(14)", 1),
		series("ADX(14)", 1),
		series("DI+(14)", 1),
		series("DI-(14)", 1),
		series("SUPERTREND(10,3.0)", 1),
		series("SUPERTREND_DIR(10,3.0)", 1),
	}
}

// measurementContextWithPositions mirrors a production cycle: `coins` candidates
// and `positions` open positions, three timeframes, `bars` of kline history, the
// full indicator set, microstructure and positioning.
func measurementContextWithPositions(cfg *store.StrategyConfig, coins, bars, positions int) *Context {
	ctx := &Context{
		CurrentTime:       time.Now().Format(time.RFC3339),
		CallCount:         1,
		Account:           AccountInfo{TotalEquity: 1000, AvailableBalance: 800},
		MarketDataMap:     map[string]*market.Data{},
		MicrostructureMap: map[string]*market.MarketMicrostructure{},
		PositioningMap:    map[string]*market.Positioning{},
	}
	add := func(symbol string, isPosition bool) {
		data := &market.Data{Symbol: symbol, CurrentPrice: 100}
		data.TimeframeData = map[string]*market.TimeframeSeriesData{}
		for _, tf := range cfg.Indicators.Klines.SelectedTimeframes {
			td := &market.TimeframeSeriesData{Timeframe: tf, ConfiguredIndicators: fullIndicatorSeries(bars)}
			for b := 0; b < bars; b++ {
				td.Klines = append(td.Klines, market.KlineBar{
					Time: int64(b) * 300_000, Open: 100, High: 101, Low: 99, Close: 100.5, Volume: 1234.5678,
				})
			}
			data.TimeframeData[tf] = td
		}
		ctx.MarketDataMap[symbol] = data
		ctx.MicrostructureMap[symbol] = &market.MarketMicrostructure{
			SpreadBps: 1, Imbalance: 0.2, ImbalanceSide: "BUY",
			BidDepthUSD: 123456, AskDepthUSD: 234567, LiquidityScore: 85, VWAP: 100.2,
		}
		ctx.PositioningMap[symbol] = &market.Positioning{
			Funding: market.FundingReading{RateHourlyPct: 0.01, AnnualPct: 87, Percentile: 62, PercentileOK: true},
			OIUSD:   1_240_000,
			OI:      []market.OIReading{{Window: time.Hour, OIChangePct: 3.4, PriceChangePct: 1.1, Quadrant: market.QuadrantNewLongs}},
			TakerFlow: &market.TakerFlowReading{
				Window: time.Hour, BuyUSD: 620_000, SellUSD: 410_000, Ratio: 1.5, Samples: 412,
			},
		}
		if isPosition {
			ctx.Positions = append(ctx.Positions, PositionInfo{
				Symbol: symbol, Side: "long", EntryPrice: 99, MarkPrice: 100, Quantity: 1, Leverage: 5,
			})
			return
		}
		ctx.CandidateCoins = append(ctx.CandidateCoins, CandidateCoin{Symbol: symbol})
	}
	// Positions print their candle table on every timeframe — the worst case the
	// pre-flight guard has to cover.
	for i := 0; i < positions; i++ {
		add(fmt.Sprintf("POS%02dUSDT", i), true)
	}
	for i := 0; i < coins; i++ {
		add(fmt.Sprintf("SYM%02dUSDT", i), false)
	}
	return ctx
}

// stockConfig is a realistic stock strategy: the venue volume board, an hourly
// primary frame with a long analysis window, and the full indicator set.
func stockConfig() *store.StrategyConfig {
	cfg := store.GetDefaultStrategyConfig("en")
	cfg.Indicators.EnableEMA = true
	cfg.Indicators.EnableMACD = true
	cfg.Indicators.EnableRSI = true
	cfg.Indicators.EnableATR = true
	cfg.Indicators.EnableBOLL = true
	cfg.Indicators.EnableADX = true
	cfg.Indicators.EnableSupertrend = true
	cfg.CoinSource.SourceType = "hyper_rank"
	cfg.CoinSource.HyperRankCategory = "stock"
	cfg.CoinSource.HyperRankDirection = store.DefaultHyperRankDirection
	cfg.CoinSource.HyperRankLimit = store.DefaultHyperRankLimit
	cfg.Indicators.Klines.PrimaryTimeframe = "1h"
	cfg.Indicators.Klines.SelectedTimeframes = []string{"1h", "4h", "1d"}
	cfg.Indicators.Klines.EnableMultiTimeframe = true
	cfg.Indicators.Klines.PrimaryCount = 200
	return &cfg
}

// measure returns the config estimate and the prompt the engine really builds,
// both in tokens (the estimator's own English rule: ~4 chars per token).
func measure(cfg *store.StrategyConfig, coins, bars, positions int) (estimate, measured int) {
	engine := NewStrategyEngine(cfg)
	ctx := measurementContextWithPositions(cfg, coins, bars, positions)
	system := len(engine.BuildSystemPrompt(ctx.Account.TotalEquity, "balanced"))
	user := len(engine.BuildUserPrompt(ctx))
	return cfg.EstimateTokens().Total, (system + user) / 4
}

// A pre-flight guard must cover the worst case the prompt can hold (every
// position prints its candle table on every timeframe) without drifting into
// fiction: the bands below are what "in sync" means, and the estimate may never
// fall below the measured prompt.
func TestTokenEstimateTracksRealPrompt(t *testing.T) {
	cfg := stockConfig()
	coins := store.DefaultHyperRankLimit
	bars := cfg.Indicators.Klines.PrimaryCount

	estimate, measured := measure(cfg, coins, bars, store.MaxPositions)
	t.Logf("worst case (candidates=%d positions=%d bars=%d): estimator=%d tokens | measured=%d tokens | ratio=%.2fx",
		coins, store.MaxPositions, bars, estimate, measured, float64(estimate)/float64(measured))
	if estimate < measured {
		t.Errorf("estimator understates the worst-case prompt: %d estimated vs %d measured", estimate, measured)
	}
	if estimate > measured*2 {
		t.Errorf("estimator overstates the worst-case prompt by >2x: %d estimated vs %d measured", estimate, measured)
	}

	// Typical shape, informational: fewer positions than the configured maximum.
	typicalEstimate, typicalMeasured := measure(cfg, coins, bars, 1)
	t.Logf("typical (positions=1): estimator=%d | measured=%d | ratio=%.2fx",
		typicalEstimate, typicalMeasured, float64(typicalEstimate)/float64(typicalMeasured))

	// The display cap — not the analysis window — drives the prompt size, and the
	// estimate has to move with it.
	previousEstimate, previousMeasured := 0, 0
	for _, display := range []int{10, 40, 100, 200} {
		c := *cfg
		c.Indicators.Klines.PromptDisplayBars = display
		est, real := measure(&c, coins, bars, 1)
		t.Logf("prompt_display_bars=%d: estimator=%d | measured=%d", display, est, real)
		if real <= previousMeasured || est <= previousEstimate {
			t.Fatalf("prompt_display_bars=%d did not grow the prompt (est %d→%d, real %d→%d)",
				display, previousEstimate, est, previousMeasured, real)
		}
		previousEstimate, previousMeasured = est, real
		if est < real {
			t.Errorf("estimator understates at prompt_display_bars=%d: %d estimated vs %d measured", display, est, real)
		}
	}
}
