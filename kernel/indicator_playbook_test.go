package kernel

import (
	"strings"
	"testing"

	"nofx/store"
)

func boolPtr(b bool) *bool { return &b }

// The playbook must never reference a disabled indicator: the model would
// otherwise try to "confirm with ADX" while receiving no ADX data at all.
func TestIndicatorPlaybookOnlyMentionsEnabledIndicators(t *testing.T) {
	cfg := store.GetDefaultStrategyConfig("en")
	cfg.Indicators.EnableADX = true
	cfg.Indicators.EnableSupertrend = true
	cfg.Indicators.EnableBOLL = true
	cfg.Indicators.EnableMACD = true
	cfg.Indicators.EnableEMA = false
	cfg.Indicators.EnableRSI = false
	cfg.Indicators.EnableATR = false
	cfg.Indicators.EnableVolume = false
	cfg.Indicators.EnableOI = false
	cfg.Indicators.EnableFundingRate = false
	cfg.Indicators.EnableMicrostructure = boolPtr(false)
	engine := NewStrategyEngine(&cfg)

	prompt := engine.BuildSystemPrompt(1000, "balanced")
	if !strings.Contains(prompt, "Indicator Confluence Rules") {
		t.Fatalf("playbook missing from prompt")
	}
	for _, want := range []string{
		"ADX gauges regime",
		"Supertrend direction",
		"Bollinger Bands time entries",
		"MACD crossing",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("rule for an enabled indicator missing: %q", want)
		}
	}
	for _, bad := range []string{
		"Price above the EMA set",
		"RSI extremes",
		"Size stops from ATR",
		"volume expansion",
		"Rising OI",
		"Extreme funding",
		"Microstructure:",
	} {
		if strings.Contains(prompt, bad) {
			t.Errorf("rule for a DISABLED indicator present: %q", bad)
		}
	}

	// The available-indicator list must agree with the enable flags too.
	for _, bad := range []string{"- EMA indicators", "- RSI indicators", "- ATR indicators", "- Volume data", "- Open Interest"} {
		if strings.Contains(prompt, bad) {
			t.Errorf("disabled indicator listed as available: %q", bad)
		}
	}
	if !strings.Contains(prompt, "ADX (trend strength)") {
		t.Errorf("enabled ADX not listed as available")
	}
	if !strings.Contains(prompt, "Supertrend (trailing band") {
		t.Errorf("enabled Supertrend not listed as available")
	}
}

func TestIndicatorPlaybookAbsentWithNoIndicators(t *testing.T) {
	cfg := store.GetDefaultStrategyConfig("en")
	ind := &cfg.Indicators
	ind.EnableADX = false
	ind.EnableSupertrend = false
	ind.EnableEMA = false
	ind.EnableMACD = false
	ind.EnableRSI = false
	ind.EnableATR = false
	ind.EnableBOLL = false
	ind.EnableVolume = false
	ind.EnableOI = false
	ind.EnableFundingRate = false
	ind.EnableQuantData = false
	ind.EnableMicrostructure = boolPtr(false)
	engine := NewStrategyEngine(&cfg)

	if prompt := engine.BuildSystemPrompt(1000, "balanced"); strings.Contains(prompt, "Indicator Confluence Rules") {
		t.Fatalf("playbook must not appear when no indicator is enabled")
	}
}

func TestIndicatorPlaybookLocalized(t *testing.T) {
	cfg := store.GetDefaultStrategyConfig("zh")
	cfg.Indicators.EnableADX = true
	engine := NewStrategyEngine(&cfg)

	prompt := engine.BuildSystemPrompt(1000, "balanced")
	if !strings.Contains(prompt, "指标协同规则") || !strings.Contains(prompt, "ADX 判断市场状态") {
		t.Fatalf("zh playbook not localized")
	}
}
