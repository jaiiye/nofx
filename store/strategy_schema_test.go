package store

import (
	"encoding/json"
	"testing"
	"time"
)

func TestStrategyConfigMarshalSeparatesGridAndAIConfig(t *testing.T) {
	cfg := GetDefaultStrategyConfig("zh")
	cfg.StrategyType = "grid_trading"
	cfg.GridConfig = &GridStrategyConfig{
		Symbol:          "BTCUSDT",
		GridCount:       20,
		TotalInvestment: 200,
		Leverage:        2,
		UseATRBounds:    true,
		ATRMultiplier:   2,
		Distribution:    "uniform",
	}

	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal grid config: %v", err)
	}

	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatalf("unmarshal grid config map: %v", err)
	}
	if asMap["strategy_type"] != "grid_trading" {
		t.Fatalf("expected grid strategy_type, got %v", asMap["strategy_type"])
	}
	if _, ok := asMap["grid_config"]; !ok {
		t.Fatalf("expected grid_config in grid strategy JSON: %s", string(raw))
	}
	for _, key := range []string{"ai_config", "coin_source", "indicators", "risk_control", "prompt_sections", "custom_prompt"} {
		if _, ok := asMap[key]; ok {
			t.Fatalf("did not expect %s in grid strategy JSON: %s", key, string(raw))
		}
	}
}

func TestStrategyConfigUnmarshalLegacyFlatAIConfig(t *testing.T) {
	raw := []byte(`{
		"strategy_type":"ai_trading",
		"coin_source":{"source_type":"static","static_coins":["ETHUSDT"]},
		"indicators":{"klines":{"primary_timeframe":"15m"}},
		"risk_control":{"max_positions":2,"min_confidence":80},
		"prompt_sections":{"entry_standards":"trend only"},
		"custom_prompt":"prefer ETH"
	}`)

	var cfg StrategyConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal legacy flat config: %v", err)
	}
	if cfg.CoinSource.SourceType != "static" || len(cfg.CoinSource.StaticCoins) != 1 || cfg.CoinSource.StaticCoins[0] != "ETHUSDT" {
		t.Fatalf("legacy coin source was not normalized: %+v", cfg.CoinSource)
	}
	if cfg.Indicators.Klines.PrimaryTimeframe != "15m" {
		t.Fatalf("legacy indicators were not normalized: %+v", cfg.Indicators.Klines)
	}

	normalized, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal normalized config: %v", err)
	}
	var asMap map[string]any
	if err := json.Unmarshal(normalized, &asMap); err != nil {
		t.Fatalf("unmarshal normalized map: %v", err)
	}
	if _, ok := asMap["ai_config"]; !ok {
		t.Fatalf("expected ai_config after normalizing legacy config: %s", string(normalized))
	}
	if _, ok := asMap["coin_source"]; ok {
		t.Fatalf("did not expect legacy coin_source at top level: %s", string(normalized))
	}
}

func TestStrategyConfigTradingModeRoundTrip(t *testing.T) {
	cfg := GetDefaultStrategyConfig("en")
	cfg.TradingMode = "aggressive"

	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}

	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatalf("unmarshal config map: %v", err)
	}
	aiCfg, ok := asMap["ai_config"].(map[string]any)
	if !ok {
		t.Fatalf("expected ai_config object: %s", string(raw))
	}
	if aiCfg["trading_mode"] != "aggressive" {
		t.Fatalf("ai_config.trading_mode = %v, want aggressive: %s", aiCfg["trading_mode"], string(raw))
	}

	var back StrategyConfig
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal round-trip: %v", err)
	}
	if back.TradingMode != "aggressive" {
		t.Fatalf("TradingMode after round-trip = %q, want aggressive", back.TradingMode)
	}
}

func TestStrategyConfigUnmarshalTradingModePrecedence(t *testing.T) {
	// Legacy flat form: trading_mode sits at the top level.
	flat := []byte(`{
		"strategy_type":"ai_trading",
		"trading_mode":"scalping",
		"coin_source":{"source_type":"static","static_coins":["ETHUSDT"]},
		"indicators":{"klines":{"primary_timeframe":"15m"}},
		"risk_control":{"max_positions":2}
	}`)
	var flatCfg StrategyConfig
	if err := json.Unmarshal(flat, &flatCfg); err != nil {
		t.Fatalf("unmarshal flat trading_mode: %v", err)
	}
	if flatCfg.TradingMode != "scalping" {
		t.Fatalf("flat TradingMode = %q, want scalping", flatCfg.TradingMode)
	}

	// Nested ai_config wins over the top-level echo.
	nested := []byte(`{
		"strategy_type":"ai_trading",
		"trading_mode":"aggressive",
		"ai_config":{
			"trading_mode":"conservative",
			"coin_source":{"source_type":"static","static_coins":["ETHUSDT"]},
			"indicators":{"klines":{"primary_timeframe":"15m"}},
			"risk_control":{"max_positions":2}
		}
	}`)
	var nestedCfg StrategyConfig
	if err := json.Unmarshal(nested, &nestedCfg); err != nil {
		t.Fatalf("unmarshal nested trading_mode: %v", err)
	}
	if nestedCfg.TradingMode != "conservative" {
		t.Fatalf("nested TradingMode = %q, want conservative", nestedCfg.TradingMode)
	}
}

func TestStrategyConfigNormalizeProductSchemaForLLMLabels(t *testing.T) {
	cfg := GetDefaultStrategyConfig("zh")
	patch := map[string]any{
		"strategy_type": "AI 策略",
		"ai_config": map[string]any{
			"coin_source": map[string]any{
				"source_type": "AI500",
			},
			"indicators": map[string]any{
				"klines": map[string]any{
					"primary_timeframe":   "1分钟",
					"selected_timeframes": []any{`["1m"`, `"5m"`, `"15m"]`},
				},
			},
		},
	}

	merged, err := MergeStrategyConfig(cfg, patch)
	if err != nil {
		t.Fatalf("merge strategy config: %v", err)
	}
	merged.ClampLimits()

	if merged.StrategyType != "ai_trading" {
		t.Fatalf("strategy_type = %q, want ai_trading", merged.StrategyType)
	}
	if merged.CoinSource.SourceType != "ai500" {
		t.Fatalf("source_type = %q, want ai500", merged.CoinSource.SourceType)
	}
	if !merged.CoinSource.UseAI500 || merged.CoinSource.UseOITop || merged.CoinSource.UseOILow {
		t.Fatalf("coin source flags not normalized: %+v", merged.CoinSource)
	}
	if merged.Indicators.Klines.PrimaryTimeframe != "1m" {
		t.Fatalf("primary_timeframe = %q, want 1m", merged.Indicators.Klines.PrimaryTimeframe)
	}
	want := []string{"1m", "5m", "15m"}
	if len(merged.Indicators.Klines.SelectedTimeframes) != len(want) {
		t.Fatalf("selected_timeframes = %+v, want %+v", merged.Indicators.Klines.SelectedTimeframes, want)
	}
	for i := range want {
		if merged.Indicators.Klines.SelectedTimeframes[i] != want[i] {
			t.Fatalf("selected_timeframes = %+v, want %+v", merged.Indicators.Klines.SelectedTimeframes, want)
		}
	}
}

// ADX / Supertrend are printed for the primary frame only, and the primary
// frame drives candle-close alignment. The strategy studio keeps the primary
// inside the selected list, but an API/agent write could pick a primary outside
// it — which silently drops those gauges from the prompt.
func TestNormalizeKeepsPrimaryTimeframeSelected(t *testing.T) {
	contains := func(list []string, want string) bool {
		for _, v := range list {
			if v == want {
				return true
			}
		}
		return false
	}

	cfg := GetDefaultStrategyConfig("en")
	cfg.Indicators.Klines.PrimaryTimeframe = "1h"
	cfg.Indicators.Klines.SelectedTimeframes = []string{"15m", "4h", "1m"}
	cfg.NormalizeProductSchema()

	got := cfg.Indicators.Klines.SelectedTimeframes
	if !contains(got, "1h") {
		t.Fatalf("primary timeframe dropped from selected_timeframes: %v", got)
	}
	if got[0] != "1h" {
		t.Fatalf("primary timeframe should come first, got %v", got)
	}
	if len(got) != 4 {
		t.Fatalf("existing frames must be kept, got %v", got)
	}

	// Already-present primary leaves the order untouched.
	cfg.Indicators.Klines.SelectedTimeframes = []string{"15m", "1h", "4h"}
	cfg.NormalizeProductSchema()
	if got := cfg.Indicators.Klines.SelectedTimeframes; len(got) != 3 || got[0] != "15m" || got[1] != "1h" {
		t.Fatalf("selected_timeframes reordered unnecessarily: %v", got)
	}

	// A full list is capped, and the primary still survives the cap.
	cfg.Indicators.Klines.SelectedTimeframes = []string{"1m", "5m", "15m", "4h"}
	cfg.ClampLimits()
	got = cfg.Indicators.Klines.SelectedTimeframes
	if len(got) != MaxTimeframes {
		t.Fatalf("selected_timeframes = %v, want %d entries", got, MaxTimeframes)
	}
	if !contains(got, "1h") {
		t.Fatalf("primary timeframe lost while capping: %v", got)
	}

	// No primary configured: nothing to enforce.
	cfg.Indicators.Klines.PrimaryTimeframe = ""
	cfg.Indicators.Klines.SelectedTimeframes = []string{"5m"}
	cfg.NormalizeProductSchema()
	if got := cfg.Indicators.Klines.SelectedTimeframes; len(got) != 1 || got[0] != "5m" {
		t.Fatalf("selected_timeframes changed without a primary: %v", got)
	}
}

// The entry mode must default to the historical behaviour: an unset, empty or
// misspelled value can never switch live trading to resting maker orders, and
// the wait bound is clamped rather than accepted blindly.
func TestNormalizeEntryOrderModeAndTimeout(t *testing.T) {
	modeCases := []struct {
		in   string
		want string
	}{
		{"", EntryOrderModeMarket},
		{"market", EntryOrderModeMarket},
		{"MAKER", EntryOrderModeMaker},
		{" maker ", EntryOrderModeMaker},
		{"Maker", EntryOrderModeMaker},
		{"limit", EntryOrderModeMarket}, // a typo must not change execution
		{"makerr", EntryOrderModeMarket},
	}
	for _, c := range modeCases {
		if got := NormalizeEntryOrderMode(c.in); got != c.want {
			t.Errorf("NormalizeEntryOrderMode(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	timeoutCases := []struct {
		in   int
		want int
	}{
		{0, DefaultMakerEntryTimeoutSec},
		{-5, DefaultMakerEntryTimeoutSec},
		{1, MinMakerEntryTimeoutSec},
		{5, MinMakerEntryTimeoutSec},
		{20, 20},
		{60, MaxMakerEntryTimeoutSec},
		{600, MaxMakerEntryTimeoutSec},
	}
	for _, c := range timeoutCases {
		if got := NormalizeMakerEntryTimeoutSec(c.in); got != c.want {
			t.Errorf("NormalizeMakerEntryTimeoutSec(%d) = %d, want %d", c.in, got, c.want)
		}
	}

	// MakerEntry is the single read used by the order path.
	var rc RiskControlConfig
	if maker, timeout := rc.MakerEntry(); maker || timeout != time.Duration(DefaultMakerEntryTimeoutSec)*time.Second {
		t.Fatalf("default MakerEntry = %v/%v, want false/%ds", maker, timeout, DefaultMakerEntryTimeoutSec)
	}
	rc = RiskControlConfig{EntryOrderMode: "maker", MakerEntryTimeoutSec: 9}
	if maker, timeout := rc.MakerEntry(); !maker || timeout != 9*time.Second {
		t.Fatalf("MakerEntry = %v/%v, want true/9s", maker, timeout)
	}

	// ClampLimits persists normalized values, so a stored config cannot carry an
	// unknown mode forward.
	cfg := GetDefaultStrategyConfig("en")
	cfg.RiskControl.EntryOrderMode = "Limit"
	cfg.RiskControl.MakerEntryTimeoutSec = 900
	cfg.ClampLimits()
	if cfg.RiskControl.EntryOrderMode != EntryOrderModeMarket {
		t.Fatalf("entry mode = %q, want market", cfg.RiskControl.EntryOrderMode)
	}
	if cfg.RiskControl.MakerEntryTimeoutSec != MaxMakerEntryTimeoutSec {
		t.Fatalf("timeout = %d, want %d", cfg.RiskControl.MakerEntryTimeoutSec, MaxMakerEntryTimeoutSec)
	}
}

// A missing/invalid margin-usage value must mean "use the product default",
// not "silently cap at 10%" and never "skip the cap". The pre-trade gate calls
// the same function, so a zero written by any path cannot disable the check.
func TestNormalizeMarginUsageLimit(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{0, DefaultMarginUsage},
		{-0.5, DefaultMarginUsage},
		{0.05, MinMarginUsage},
		{0.1, 0.1},
		{0.45, 0.45},
		{0.9, 0.9},
		{1.0, 1.0},
		{1.5, MaxMarginUsage},
	}
	for _, tc := range cases {
		if got := NormalizeMarginUsageLimit(tc.in); got != tc.want {
			t.Errorf("NormalizeMarginUsageLimit(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}

	// ClampLimits (the save-path normalizer) applies the same mapping.
	cfg := GetDefaultStrategyConfig("en")
	cfg.RiskControl.MaxMarginUsage = 0
	cfg.ClampLimits()
	if cfg.RiskControl.MaxMarginUsage != DefaultMarginUsage {
		t.Errorf("ClampLimits(0) = %v, want %v", cfg.RiskControl.MaxMarginUsage, DefaultMarginUsage)
	}
}
