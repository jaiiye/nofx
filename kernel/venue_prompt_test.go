package kernel

import (
	"strings"
	"testing"
	"time"

	"nofx/market"
	"nofx/store"
)

func venuePromptEngine(t *testing.T) *StrategyEngine {
	t.Helper()
	cfg := store.GetDefaultStrategyConfig("en")
	return NewStrategyEngine(&cfg)
}

func TestFormatCrossVenueRendersPerVenueAndCaveat(t *testing.T) {
	engine := venuePromptEngine(t)
	ctx := &Context{CrossVenueMap: map[string]*market.CrossVenue{
		"BTCUSDT": {
			Symbol: "BTCUSDT",
			Venues: []market.VenueMetrics{
				{Venue: "binance", OIUSD: 315_000_000, Funding1hPct: 0.001, FundingIntervalH: 8},
				{Venue: "bybit", OIUSD: 126_000_000, Funding1hPct: 0.002, FundingIntervalH: 8},
				{Venue: "hyperliquid", OIUSD: 63_000_000, Funding1hPct: 0.012, FundingIntervalH: 1},
			},
			OITotalUSD:         504_000_000,
			OISharePct:         map[string]float64{"binance": 62.5, "bybit": 25, "hyperliquid": 12.5},
			FundingSpread1hPct: 0.011,
			FundingSpreadPair:  "hyperliquid/binance",
			FundingSpreadOK:    true,
			CrowdedSide:        "long",
		},
	}}

	got := engine.formatCrossVenue(ctx, "BTCUSDT")
	for _, want := range []string{
		"CROSS-VENUE OI / FUNDING",
		"Binance: OI $315.00M",
		"Hyperliquid: OI $63.00M",
		"funding spread +0.0110%/h (hyperliquid/binance)",
		"longs crowded",
		"not market-wide exposure",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("cross-venue block missing %q:\n%s", want, got)
		}
	}
}

func TestFormatCrossVenueOmitsWithoutReading(t *testing.T) {
	engine := venuePromptEngine(t)
	if got := engine.formatCrossVenue(&Context{}, "BTCUSDT"); got != "" {
		t.Fatalf("expected an omitted section, got %q", got)
	}
	if got := engine.formatCrossVenue(&Context{CrossVenueMap: map[string]*market.CrossVenue{"ETHUSDT": {Symbol: "ETHUSDT"}}}, "BTCUSDT"); got != "" {
		t.Fatalf("expected an omitted section for an absent symbol, got %q", got)
	}
}

func TestFormatLiquidationFlowRendersCascade(t *testing.T) {
	engine := venuePromptEngine(t)
	now := time.Now().UnixMilli()
	ctx := &Context{LiquidationMap: map[string]*market.LiquidationReading{
		"BTCUSDT": {
			Symbol:         "BTCUSDT",
			LongUSD:        4_000_000,
			ShortUSD:       500_000,
			TotalUSD:       4_500_000,
			LongSharePct:   88.9,
			PrintCount:     30,
			LargestUSD:     800_000,
			ShortWindowUSD: 3_200_000,
			BaselineUSD:    400_000,
			ZScore:         4.2,
			BaselineOK:     true,
			Cascade:        true,
			CascadeSide:    "long",
			ComputedAtMs:   now,
		},
	}}

	got := engine.formatLiquidationFlow(ctx, "BTCUSDT")
	for _, want := range []string{
		"LIQUIDATION FLOW",
		"1h long $4.00M / short $500.0K",
		"long share 89%",
		"z=4.2",
		"CASCADE: longs",
		"do not chase",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("liquidation block missing %q:\n%s", want, got)
		}
	}
}

func TestFormatLiquidationFlowOmitsWithoutReading(t *testing.T) {
	engine := venuePromptEngine(t)
	if got := engine.formatLiquidationFlow(&Context{}, "BTCUSDT"); got != "" {
		t.Fatalf("expected an omitted section, got %q", got)
	}
}

func TestPlaybookMentionsNewDataOnlyWhenEnabled(t *testing.T) {
	cfg := store.GetDefaultStrategyConfig("en")
	engine := NewStrategyEngine(&cfg)

	prompt := engine.BuildSystemPrompt(1000, "balanced")
	for _, want := range []string{"Cross-venue OI is listed per venue", "Liquidation flow is event data"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("enabled rule missing: %q", want)
		}
	}

	cfg.Indicators.EnableCrossVenue = boolPtr(false)
	cfg.Indicators.EnableLiquidationFlow = boolPtr(false)
	engine = NewStrategyEngine(&cfg)
	prompt = engine.BuildSystemPrompt(1000, "balanced")
	for _, bad := range []string{"Cross-venue OI is listed per venue", "Liquidation flow is event data"} {
		if strings.Contains(prompt, bad) {
			t.Errorf("disabled rule present: %q", bad)
		}
	}
}

func TestVenueSectionsStayWithinTokenEstimate(t *testing.T) {
	engine := venuePromptEngine(t)
	now := time.Now().UnixMilli()

	cross := engine.formatCrossVenue(&Context{CrossVenueMap: map[string]*market.CrossVenue{
		"BTCUSDT": {
			Symbol: "BTCUSDT",
			Venues: []market.VenueMetrics{
				{Venue: "binance", OIUSD: 315_000_000, Funding1hPct: -0.00123, FundingIntervalH: 8, PremiumPct: -0.042},
				{Venue: "bybit", OIUSD: 126_000_000, Funding1hPct: 0.00234, FundingIntervalH: 8, PremiumPct: 0.011, FundingAssumed: true},
				{Venue: "hyperliquid", OIUSD: 63_000_000, Funding1hPct: 0.01234, FundingIntervalH: 1},
			},
			OITotalUSD:         504_000_000,
			OISharePct:         map[string]float64{"binance": 62.5, "bybit": 25, "hyperliquid": 12.5},
			FundingSpread1hPct: 0.01357,
			FundingSpreadPair:  "hyperliquid/binance",
			FundingSpreadOK:    true,
			CrowdedSide:        "long",
		},
	}}, "BTCUSDT")

	liquidation := engine.formatLiquidationFlow(&Context{LiquidationMap: map[string]*market.LiquidationReading{
		"BTCUSDT": {
			Symbol:         "BTCUSDT",
			LongUSD:        4_000_000,
			ShortUSD:       500_000,
			TotalUSD:       4_500_000,
			LongSharePct:   88.9,
			PrintCount:     30,
			LargestUSD:     800_000,
			ShortWindowUSD: 3_200_000,
			BaselineUSD:    400_000,
			ZScore:         4.2,
			BaselineOK:     true,
			Cascade:        true,
			CascadeSide:    "long",
			ComputedAtMs:   now,
		},
	}}, "BTCUSDT")

	t.Logf("cross-venue=%d chars, liquidation=%d chars, combined=%d", len(cross), len(liquidation), len(cross)+len(liquidation))

	// These hard bounds mirror promptCrossVenueChars / promptLiquidationChars in
	// store/strategy.go. If a formatter grows past them, the token estimate
	// starts undershooting the prompt it is supposed to guard.
	if len(cross) > 520 {
		t.Errorf("cross-venue section is %d chars, estimate allows 520", len(cross))
	}
	if len(liquidation) > 300 {
		t.Errorf("liquidation section is %d chars, estimate allows 260 (hard bound 300)", len(liquidation))
	}
}
