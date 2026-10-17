package kernel

import (
	"strings"
	"testing"
	"time"

	"nofx/market"
	"nofx/store"
)

func positioningEngine(lang string) *StrategyEngine {
	cfg := store.GetDefaultStrategyConfig(lang)
	return NewStrategyEngine(&cfg)
}

// A symbol with no reading must produce nothing at all — no empty header, no
// zeros. The model reads a printed 0 as "balanced positioning", not "unknown".
func TestFormatPositioningOmissions(t *testing.T) {
	engine := positioningEngine("en")

	if got := engine.formatPositioning(nil, "BTCUSDT"); got != "" {
		t.Fatalf("nil context must render nothing, got %q", got)
	}
	if got := engine.formatPositioning(&Context{}, "BTCUSDT"); got != "" {
		t.Fatalf("empty positioning map must render nothing, got %q", got)
	}

	ctx := &Context{PositioningMap: map[string]*market.Positioning{"BTCUSDT": nil}}
	if got := engine.formatPositioning(ctx, "BTCUSDT"); got != "" {
		t.Fatalf("nil reading must render nothing, got %q", got)
	}
	if got := engine.formatPositioning(ctx, "ETHUSDT"); got != "" {
		t.Fatalf("unread symbol must render nothing, got %q", got)
	}

	// A reading whose every derived value is unavailable renders nothing.
	empty := &Context{PositioningMap: map[string]*market.Positioning{"BTCUSDT": {}}}
	if got := engine.formatPositioning(empty, "BTCUSDT"); got != "" {
		t.Fatalf("a reading with no derivable values must render nothing, got %q", got)
	}
}

func TestFormatPositioningRendersAvailableParts(t *testing.T) {
	engine := positioningEngine("en")
	ctx := &Context{PositioningMap: map[string]*market.Positioning{
		"BTCUSDT": {
			Funding: market.FundingReading{
				RateHourlyPct: 0.0125,
				AnnualPct:     109.5,
				Percentile:    92,
				PercentileOK:  true,
				Samples:       168,
			},
			OIUSD:      1_240_000,
			PremiumPct: 0.08,
			OI: []market.OIReading{{
				Window:         time.Hour,
				OIChangePct:    3.4,
				PriceChangePct: 1.1,
				Quadrant:       market.QuadrantNewLongs,
			}},
			TakerFlow: &market.TakerFlowReading{
				Window:  time.Hour,
				BuyUSD:  620_000,
				SellUSD: 410_000,
				Ratio:   1.5122,
				Samples: 412,
			},
		},
	}}

	out := engine.formatPositioning(ctx, "BTCUSDT")
	for _, want := range []string{
		"HL POSITIONING",
		"not an account long/short ratio",
		"funding +0.0125%/h",
		"+110% APR",
		"longs pay shorts",
		"92nd pct",
		"crowded long",
		"premium +0.080%",
		"OI $1.24M",
		"OI 1h +3.40% / price +1.10%",
		"new longs entering",
		"Taker flow 1h: buy $620.0K / sell $410.0K = 1.51x (412 prints)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("positioning output missing %q\n---\n%s", want, out)
		}
	}
}

// Each clause stands on its own: a missing funding baseline or a feed without
// enough prints must drop only that clause.
func TestFormatPositioningPartialClauses(t *testing.T) {
	engine := positioningEngine("en")

	noPercentile := &Context{PositioningMap: map[string]*market.Positioning{
		"BTCUSDT": {
			Funding: market.FundingReading{RateHourlyPct: -0.005, AnnualPct: -43.8},
			OIUSD:   900_000,
		},
	}}
	out := engine.formatPositioning(noPercentile, "BTCUSDT")
	if !strings.Contains(out, "funding -0.0050%/h") || !strings.Contains(out, "shorts pay longs") {
		t.Fatalf("funding clause missing: %s", out)
	}
	if strings.Contains(out, "pct of the coin's own 7d funding") {
		t.Errorf("percentile must be omitted without a baseline: %s", out)
	}
	if strings.Contains(out, "Taker flow") || strings.Contains(out, "OI 1h") {
		t.Errorf("clauses without data must be omitted: %s", out)
	}

	flowOnly := &Context{PositioningMap: map[string]*market.Positioning{
		"BTCUSDT": {TakerFlow: &market.TakerFlowReading{
			Window: time.Hour, BuyUSD: 0, SellUSD: 250_000, Ratio: 0, Samples: 90,
		}},
	}}
	out = engine.formatPositioning(flowOnly, "BTCUSDT")
	if !strings.Contains(out, "all sells") || !strings.Contains(out, "Taker flow 1h") {
		t.Fatalf("all-sell flow must read as all sells: %s", out)
	}
	if strings.Contains(out, "funding") {
		t.Errorf("funding must be omitted when unavailable: %s", out)
	}
}

func TestFormatPositioningLocalized(t *testing.T) {
	engine := positioningEngine("zh")
	ctx := &Context{PositioningMap: map[string]*market.Positioning{
		"BTCUSDT": {
			Funding: market.FundingReading{RateHourlyPct: 0.02, AnnualPct: 175.2, Percentile: 96, PercentileOK: true},
			OIUSD:   2_000_000,
		},
	}}

	out := engine.formatPositioning(ctx, "BTCUSDT")
	for _, want := range []string{"HL 持仓信号", "不是账户多空比", "资金费 +0.0200%/小时", "多头付空头", "第 96 百分位", "多头拥挤"} {
		if !strings.Contains(out, want) {
			t.Errorf("zh positioning output missing %q\n---\n%s", want, out)
		}
	}
}

// The crowding rules belong to the playbook and must follow the switch.
func TestPositioningPlaybookRulesFollowSwitch(t *testing.T) {
	cfg := store.GetDefaultStrategyConfig("en")
	cfg.Indicators.EnablePositioning = boolPtr(true)
	prompt := NewStrategyEngine(&cfg).BuildSystemPrompt(1000, "balanced")
	for _, want := range []string{"Positioning / crowding", "Open interest × price", "Taker flow ratio"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("positioning playbook rule missing: %q", want)
		}
	}

	cfg.Indicators.EnablePositioning = boolPtr(false)
	prompt = NewStrategyEngine(&cfg).BuildSystemPrompt(1000, "balanced")
	if strings.Contains(prompt, "Positioning / crowding") {
		t.Error("positioning rules must disappear when the feature is off")
	}
}
