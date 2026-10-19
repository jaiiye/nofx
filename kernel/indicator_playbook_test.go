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
	// Positioning is a data feature of its own, not an indicator: switch it off
	// so this test measures indicator gating only.
	cfg.Indicators.EnablePositioning = boolPtr(false)
	engine := NewStrategyEngine(&cfg)

	prompt := engine.BuildSystemPrompt(1000, "balanced")
	if !strings.Contains(prompt, "Indicator Confluence Rules") {
		t.Fatalf("playbook missing from prompt")
	}
	for _, want := range []string{
		"ADX measures trend STRENGTH",
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
	// Positioning has its own switch; the playbook only disappears when nothing
	// at all is enabled.
	ind.EnablePositioning = boolPtr(false)
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
	if !strings.Contains(prompt, "指标协同规则") || !strings.Contains(prompt, "ADX 衡量趋势") {
		t.Fatalf("zh playbook not localized")
	}
}

// A live run stalled on this rule: the playbook read as a veto ("< 20 = range
// where breakout entries are low quality") and the model echoed it back —
// "无一满足趋势市（>25）…突破质量差" — then held for hours across every cycle.
// ADX must stay a strength reading the model weighs, not a gate it hides behind.
func TestIndicatorPlaybookADXIsNotAVeto(t *testing.T) {
	cfg := store.GetDefaultStrategyConfig("en")
	cfg.Indicators.EnableADX = true
	prompt := NewStrategyEngine(&cfg).BuildSystemPrompt(1000, "balanced")

	if !strings.Contains(prompt, "not a permission gate") {
		t.Error("ADX rule must state it is not a permission gate")
	}
	if !strings.Contains(prompt, "is not a reason to stay flat") {
		t.Error("ADX rule must state that a low reading alone does not forbid entering")
	}
	for _, veto := range []string{"low quality", "not a reason to enter"} {
		if strings.Contains(prompt, veto) {
			t.Errorf("ADX rule reads as an entry veto: %q", veto)
		}
	}

	// The confluence principle must not demand an extreme on every indicator.
	if !strings.Contains(prompt, "does not require every indicator to sit at an extreme") {
		t.Error("confluence principle must not demand an extreme from every indicator")
	}

	zhCfg := store.GetDefaultStrategyConfig("zh")
	zhCfg.Indicators.EnableADX = true
	zh := NewStrategyEngine(&zhCfg).BuildSystemPrompt(1000, "balanced")
	if !strings.Contains(zh, "不是开仓许可") || !strings.Contains(zh, "不构成观望理由") {
		t.Error("zh ADX rule must not read as a veto")
	}
	if strings.Contains(zh, "入场质量差") {
		t.Error("zh ADX rule still reads as an entry veto")
	}
}

// Exit discipline tells the model when closing is allowed at all; without it
// the model improvised exits (81% of a live account's exits were
// discretionary, taken before either level was touched).
func TestExitDisciplineSection(t *testing.T) {
	cfg := store.GetDefaultStrategyConfig("en")
	engine := NewStrategyEngine(&cfg)

	prompt := engine.BuildSystemPrompt(1000, "balanced")
	if !strings.Contains(prompt, "Exit Discipline") {
		t.Fatal("exit discipline section missing")
	}
	for _, want := range []string{"Stop-loss or take-profit is hit", "Supertrend flips", "Profit protection"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("exit condition missing: %q", want)
		}
	}
	// 0 = off (the default): the note appears only when the operator sets a value.
	if strings.Contains(prompt, "CODE ENFORCED**: discretionary closes") {
		t.Error("min-hold note must not appear while the rule is off")
	}

	cfg.RiskControl.MinHoldMinutes = 60
	engine = NewStrategyEngine(&cfg)
	if prompt := engine.BuildSystemPrompt(1000, "balanced"); !strings.Contains(prompt, "before 60 minutes") {
		t.Error("min-hold note missing when the rule is set")
	}

	// Localized.
	zhCfg := store.GetDefaultStrategyConfig("zh")
	if prompt := NewStrategyEngine(&zhCfg).BuildSystemPrompt(1000, "balanced"); !strings.Contains(prompt, "出场纪律") {
		t.Error("zh exit discipline missing")
	}
}

// Named entry archetypes and the extension guard close the space the model used
// to improvise in: a live run opened a long with the higher timeframe against it
// and justified it as "a 1h vs 4h tension, so I size modestly".
func TestIndicatorPlaybookEntryArchetypesAndExtension(t *testing.T) {
	cfg := store.GetDefaultStrategyConfig("en")
	cfg.Indicators.EnableEMA = true
	cfg.Indicators.EnableATR = true
	cfg.Indicators.EnableBOLL = true
	cfg.Indicators.Klines.EnableMultiTimeframe = true
	cfg.Indicators.Klines.PrimaryTimeframe = "1h"
	cfg.Indicators.Klines.LongerTimeframe = "4h"
	prompt := NewStrategyEngine(&cfg).BuildSystemPrompt(1000, "balanced")

	for _, want := range []string{
		"Entry archetypes",
		"Pullback continuation (long)",
		"High-base continuation (long)",
		"Failed-bounce short",
		"Breakdown-retest short",
		"1.5x ATR away from EMA50",
		"4h trend up, 1h structure intact",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("missing %q", want)
		}
	}

	// Both blocks are phrased in terms of EMA, so EMA off must silence them —
	// the playbook may never reference data the model does not receive.
	off := store.GetDefaultStrategyConfig("en")
	off.Indicators.EnableEMA = false
	off.Indicators.EnableATR = true
	offPrompt := NewStrategyEngine(&off).BuildSystemPrompt(1000, "balanced")
	for _, bad := range []string{"Entry archetypes", "Pullback continuation", "1.5x ATR away from EMA50"} {
		if strings.Contains(offPrompt, bad) {
			t.Errorf("emitted without EMA: %q", bad)
		}
	}

	// Localized.
	zhCfg := store.GetDefaultStrategyConfig("zh")
	zhCfg.Indicators.EnableEMA = true
	zhCfg.Indicators.EnableATR = true
	zhCfg.Indicators.Klines.EnableMultiTimeframe = true
	zhCfg.Indicators.Klines.PrimaryTimeframe = "1h"
	zhCfg.Indicators.Klines.LongerTimeframe = "4h"
	zh := NewStrategyEngine(&zhCfg).BuildSystemPrompt(1000, "balanced")
	for _, want := range []string{"入场范式", "回踩延续（多）", "破位回测（空）", "1.5 倍 ATR"} {
		if !strings.Contains(zh, want) {
			t.Errorf("zh missing %q", want)
		}
	}
}

// A no-trade cycle must name the level it is waiting on, otherwise the record
// says nothing and the next cycle starts from zero.
func TestOutputFormatRequiresWaitTriggerLevel(t *testing.T) {
	for _, lang := range []string{"en", "zh"} {
		cfg := store.GetDefaultStrategyConfig(lang)
		prompt := NewStrategyEngine(&cfg).BuildSystemPrompt(1000, "balanced")
		if !strings.Contains(prompt, "trigger_level") {
			t.Errorf("%s: trigger_level not defined in the output format", lang)
		}
	}
}
