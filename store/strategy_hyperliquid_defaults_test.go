package store

import "testing"

func TestDefaultHyperliquidStrategyDoesNotEnableNofxOSData(t *testing.T) {
	cfg := GetDefaultStrategyConfig("zh")
	assertHyperliquidStockRankDefault(t, cfg)
	ind := cfg.Indicators
	if ind.NofxOSAPIKey != "" {
		t.Fatalf("default should not include a NofxOS API key for Hyperliquid strategies")
	}
	if ind.EnableQuantData || ind.EnableQuantOI || ind.EnableQuantNetflow || ind.EnableOIRanking || ind.EnableNetFlowRanking || ind.EnablePriceRanking {
		t.Fatalf("default Hyperliquid strategy must not enable NofxOS datasets: %+v", ind)
	}
	if !ind.EnableRawKlines {
		t.Fatalf("raw Hyperliquid klines must stay enabled")
	}
}

func TestHyperliquidRankDefaultSurvivesClampAndNormalize(t *testing.T) {
	cfg := GetDefaultStrategyConfig("zh")
	cfg.CoinSource.UseAI500 = true
	cfg.ClampLimits()
	assertHyperliquidStockRankDefault(t, cfg)
	if cfg.CoinSource.UseAI500 {
		t.Fatalf("Hyperliquid rank strategy must clear stale AI500 flag: %+v", cfg.CoinSource)
	}
}

func TestEmptyCoinSourceInfersHyperliquidRankNotAI500(t *testing.T) {
	cfg := GetDefaultStrategyConfig("zh")
	cfg.CoinSource = CoinSourceConfig{}
	cfg.NormalizeProductSchema()
	assertHyperliquidStockRankDefault(t, cfg)
}

func assertHyperliquidStockRankDefault(t *testing.T, cfg StrategyConfig) {
	t.Helper()
	if cfg.CoinSource.SourceType != "hyper_rank" || cfg.CoinSource.HyperRankCategory != "stock" || cfg.CoinSource.HyperRankDirection != "gainers" || cfg.CoinSource.HyperRankLimit != 5 {
		t.Fatalf("coin source = %+v, want Hyperliquid dynamic stock gainers top 5", cfg.CoinSource)
	}
}

// TestEffectiveMaxPositionsCoversUnsetAndOverCeiling pins the contract shared
// by ClampLimits() and the runtime risk check. A drift between the two would
// silently throttle a strategy below what the editor shows.
func TestEffectiveMaxPositionsCoversUnsetAndOverCeiling(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{"unset falls back to default", 0, DefaultMaxPositions},
		{"negative falls back to default", -1, DefaultMaxPositions},
		{"default value is preserved", DefaultMaxPositions, DefaultMaxPositions},
		{"between default and ceiling is preserved", 5, 5},
		{"at ceiling is preserved", MaxPositions, MaxPositions},
		{"over ceiling is clamped", MaxPositions + 10, MaxPositions},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := RiskControlConfig{MaxPositions: tc.in}
			if got := cfg.EffectiveMaxPositions(); got != tc.want {
				t.Fatalf("EffectiveMaxPositions(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}

	var nilCfg *RiskControlConfig
	if got := nilCfg.EffectiveMaxPositions(); got != DefaultMaxPositions {
		t.Fatalf("nil config should resolve to %d, got %d", DefaultMaxPositions, got)
	}
}

// TestDefaultMaxPositionsStaysBelowClampCeiling guards against the default
// being configured above the ceiling, which would make the editor offer a
// value that ClampLimits() immediately rewrites. The default is deliberately
// below the ceiling so users can still raise it in the editor.
func TestDefaultMaxPositionsStaysBelowClampCeiling(t *testing.T) {
	if DefaultMaxPositions > MaxPositions {
		t.Fatalf("DefaultMaxPositions (%d) must not exceed MaxPositions (%d)", DefaultMaxPositions, MaxPositions)
	}
	if DefaultMaxPositions >= MaxPositions {
		t.Fatalf("DefaultMaxPositions (%d) should leave headroom below the ceiling (%d)", DefaultMaxPositions, MaxPositions)
	}
	cfg := GetDefaultStrategyConfig("zh")
	cfg.ClampLimits()
	if cfg.RiskControl.MaxPositions != DefaultMaxPositions {
		t.Fatalf("default config MaxPositions clamped to %d, want %d", cfg.RiskControl.MaxPositions, DefaultMaxPositions)
	}
}

// TestDefaultRiskProfileMatchesUpstreamPreset pins the concentrated-position
// profile adopted from upstream: 10x leverage, 5x equity notional per position,
// full margin, 78 minimum confidence.
func TestDefaultRiskProfileMatchesUpstreamPreset(t *testing.T) {
	cfg := GetDefaultStrategyConfig("zh")
	cfg.ClampLimits()
	risk := cfg.RiskControl

	if risk.MaxPositions != DefaultMaxPositions {
		t.Fatalf("MaxPositions = %d, want %d", risk.MaxPositions, DefaultMaxPositions)
	}
	if risk.BTCETHMaxLeverage != 10 || risk.AltcoinMaxLeverage != 10 {
		t.Fatalf("leverage = %d/%d, want 10/10", risk.BTCETHMaxLeverage, risk.AltcoinMaxLeverage)
	}
	if risk.BTCETHMaxPositionValueRatio != 5.0 || risk.AltcoinMaxPositionValueRatio != 5.0 {
		t.Fatalf("position value ratio = %v/%v, want 5.0/5.0",
			risk.BTCETHMaxPositionValueRatio, risk.AltcoinMaxPositionValueRatio)
	}
	if risk.MaxMarginUsage != 1.0 {
		t.Fatalf("MaxMarginUsage = %v, want 1.0", risk.MaxMarginUsage)
	}
	if risk.MinConfidence != 78 {
		t.Fatalf("MinConfidence = %d, want 78", risk.MinConfidence)
	}
	if risk.MinRiskRewardRatio != 3.0 {
		t.Fatalf("MinRiskRewardRatio = %v, want 3.0", risk.MinRiskRewardRatio)
	}
}

// TestEffectiveMinPositionSizeCoversUnset keeps the runtime fallback aligned
// with the documented default instead of a scattered literal.
func TestEffectiveMinPositionSizeCoversUnset(t *testing.T) {
	cfg := RiskControlConfig{}
	if got := cfg.EffectiveMinPositionSize(); got != DefaultMinPositionSize {
		t.Fatalf("unset MinPositionSize resolved to %v, want %v", got, DefaultMinPositionSize)
	}

	explicit := RiskControlConfig{MinPositionSize: 25}
	if got := explicit.EffectiveMinPositionSize(); got != 25 {
		t.Fatalf("explicit MinPositionSize resolved to %v, want 25", got)
	}
}
