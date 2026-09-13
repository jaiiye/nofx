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

// TestEffectiveRiskPerTradePctDisablesOnZero pins the "0 means disabled"
// contract. Returning the default here would silently impose a risk budget the
// user never configured, so zero must stay zero.
func TestEffectiveRiskPerTradePctDisablesOnZero(t *testing.T) {
	var nilCfg *RiskControlConfig
	if got := nilCfg.EffectiveRiskPerTradePct(); got != 0 {
		t.Fatalf("nil config resolved to %v, want 0 (disabled)", got)
	}

	unset := RiskControlConfig{}
	if got := unset.EffectiveRiskPerTradePct(); got != 0 {
		t.Fatalf("unset RiskPerTradePct resolved to %v, want 0 (disabled)", got)
	}

	explicit := RiskControlConfig{RiskPerTradePct: 1.5}
	if got := explicit.EffectiveRiskPerTradePct(); got != 1.5 {
		t.Fatalf("explicit RiskPerTradePct resolved to %v, want 1.5", got)
	}
}

// TestEffectiveADXThresholdDefaults covers the lenient default the user chose
// for mid/short-term trading.
func TestEffectiveADXThresholdDefaults(t *testing.T) {
	var nilCfg *IndicatorConfig
	if got := nilCfg.EffectiveADXThreshold(); got != DefaultADXThreshold {
		t.Fatalf("nil config resolved to %d, want %d", got, DefaultADXThreshold)
	}

	unset := IndicatorConfig{}
	if got := unset.EffectiveADXThreshold(); got != DefaultADXThreshold {
		t.Fatalf("unset ADXThreshold resolved to %d, want %d", got, DefaultADXThreshold)
	}

	explicit := IndicatorConfig{ADXThreshold: 30}
	if got := explicit.EffectiveADXThreshold(); got != 30 {
		t.Fatalf("explicit ADXThreshold resolved to %d, want 30", got)
	}
}

// TestDefaultProfileEnablesTrendAndChopFilters pins the shipped entry-gate
// defaults: EMA (for EMA200), ATR (stop + sizing), ADX and Keltner.
func TestDefaultProfileEnablesTrendAndChopFilters(t *testing.T) {
	cfg := GetDefaultStrategyConfig("zh")
	cfg.ClampLimits()

	ind := cfg.Indicators
	if !ind.EnableEMA {
		t.Fatal("default must enable EMA so EMA200 is available for the trend filter")
	}
	if !ind.EnableATR {
		t.Fatal("default must enable ATR for stop distance and position sizing")
	}
	if !ind.EnableADX {
		t.Fatal("default must enable ADX so the chop filter has data")
	}
	if !ind.EnableKeltner {
		t.Fatal("default must enable the Keltner channel for breakout entries")
	}
	if ind.ADXThreshold != DefaultADXThreshold {
		t.Fatalf("ADXThreshold = %d, want %d", ind.ADXThreshold, DefaultADXThreshold)
	}
	if cfg.RiskControl.RiskPerTradePct != DefaultRiskPerTradePct {
		t.Fatalf("RiskPerTradePct = %v, want %v",
			cfg.RiskControl.RiskPerTradePct, DefaultRiskPerTradePct)
	}
}

// TestClampLimitsBoundsADXAndRiskBudget ensures out-of-range values are pulled
// back rather than persisted as-is, while zero stays untouched so it keeps its
// "disabled / use default" meaning.
func TestClampLimitsBoundsADXAndRiskBudget(t *testing.T) {
	cfg := GetDefaultStrategyConfig("zh")

	cfg.Indicators.ADXThreshold = MaxADXThreshold + 100
	cfg.RiskControl.RiskPerTradePct = MaxRiskPerTradePct + 10
	cfg.ClampLimits()
	if cfg.Indicators.ADXThreshold != MaxADXThreshold {
		t.Fatalf("ADXThreshold = %d, want clamped to %d", cfg.Indicators.ADXThreshold, MaxADXThreshold)
	}
	if cfg.RiskControl.RiskPerTradePct != MaxRiskPerTradePct {
		t.Fatalf("RiskPerTradePct = %v, want clamped to %v",
			cfg.RiskControl.RiskPerTradePct, MaxRiskPerTradePct)
	}

	cfg.Indicators.ADXThreshold = 1
	cfg.RiskControl.RiskPerTradePct = 0.001
	cfg.ClampLimits()
	if cfg.Indicators.ADXThreshold != MinADXThreshold {
		t.Fatalf("ADXThreshold = %d, want raised to %d", cfg.Indicators.ADXThreshold, MinADXThreshold)
	}
	if cfg.RiskControl.RiskPerTradePct != MinRiskPerTradePct {
		t.Fatalf("RiskPerTradePct = %v, want raised to %v",
			cfg.RiskControl.RiskPerTradePct, MinRiskPerTradePct)
	}

	// Zero must survive clamping: it means "disabled", not "use the minimum".
	cfg.Indicators.ADXThreshold = 0
	cfg.RiskControl.RiskPerTradePct = 0
	cfg.ClampLimits()
	if cfg.Indicators.ADXThreshold != 0 {
		t.Fatalf("zero ADXThreshold was rewritten to %d; it must stay 0", cfg.Indicators.ADXThreshold)
	}
	if cfg.RiskControl.RiskPerTradePct != 0 {
		t.Fatalf("zero RiskPerTradePct was rewritten to %v; it must stay 0", cfg.RiskControl.RiskPerTradePct)
	}
}
