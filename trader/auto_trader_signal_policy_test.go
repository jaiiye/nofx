package trader

import (
	"testing"

	"nofx/kernel"
	"nofx/store"
)

func testVergexSignalTrader() *AutoTrader {
	cfg := store.GetDefaultStrategyConfig("en")
	cfg.CoinSource.SourceType = "vergex_signal"
	return &AutoTrader{
		config:         AutoTraderConfig{StrategyConfig: &cfg},
		strategyEngine: kernel.NewStrategyEngine(&cfg),
	}
}

func testSignalBias(values map[string]string) func(string) (string, bool) {
	return func(symbol string) (string, bool) {
		bias, ok := values[universeBaseKey(symbol)]
		return bias, ok
	}
}

func TestVergexSignalPolicyHoldsWhileDirectionIsUnchanged(t *testing.T) {
	decisions := []kernel.Decision{
		{Symbol: "xyz:NVDA", Action: "close_long"},
		{Symbol: "BTC", Action: "close_short"},
	}
	positions := []kernel.PositionInfo{
		{Symbol: "xyz:NVDA", Side: "long"},
		{Symbol: "BTC", Side: "short"},
	}

	got, blocked := applyVergexSignalPolicy(decisions, positions, testSignalBias(map[string]string{
		"NVDA": "bullish",
		"BTC":  "bearish",
	}))
	if len(got) != 2 || got[0].Action != "hold" || got[1].Action != "hold" ||
		got[0].Confidence != 100 || got[1].Confidence != 100 {
		t.Fatalf("unchanged long and short signals must force hold, got %+v", got)
	}
	if len(blocked) != 2 || blocked[0].Action != "close_long" || blocked[1].Action != "close_short" {
		t.Fatalf("AI closes must be blocked while signals remain unchanged, got %+v", blocked)
	}
}

func TestVergexSignalPolicyDoesNotFlagMatchingAIHoldAsConflict(t *testing.T) {
	decisions := []kernel.Decision{{Symbol: "xyz:NVDA", Action: "hold"}}
	positions := []kernel.PositionInfo{{Symbol: "xyz:NVDA", Side: "long"}}

	got, blocked := applyVergexSignalPolicy(decisions, positions, testSignalBias(map[string]string{"NVDA": "bullish"}))
	if len(got) != 1 || got[0].Action != "hold" {
		t.Fatalf("unchanged bullish signal must hold, got %+v", got)
	}
	if len(blocked) != 0 {
		t.Fatalf("matching AI hold is not a signal conflict, got %+v", blocked)
	}
}

func TestVergexSignalPolicyClosesWhenDirectionChangesOrDisappears(t *testing.T) {
	decisions := []kernel.Decision{
		{Symbol: "xyz:NVDA", Action: "open_short"},
		{Symbol: "BTC", Action: "open_long"},
	}
	positions := []kernel.PositionInfo{
		{Symbol: "xyz:NVDA", Side: "long"},
		{Symbol: "BTC", Side: "short"},
		{Symbol: "ETH", Side: "long"},
	}

	got, blocked := applyVergexSignalPolicy(decisions, positions, testSignalBias(map[string]string{
		"NVDA": "bearish",
		"ETH":  "neutral",
	}))
	if len(got) != 3 || got[0].Action != "close_long" || got[1].Action != "close_short" || got[2].Action != "close_long" {
		t.Fatalf("changed or absent signals must close positions, got %+v", got)
	}
	if len(blocked) != 2 {
		t.Fatalf("a reversed position must close without flipping in the same cycle, blocked=%+v", blocked)
	}
}

func TestVergexSignalPolicyAllowsOnlyMatchingEntries(t *testing.T) {
	decisions := []kernel.Decision{
		{Symbol: "xyz:NVDA", Action: "open_long"},
		{Symbol: "BTC", Action: "open_short"},
		{Symbol: "ETH", Action: "open_long"},
		{Symbol: "SOL", Action: "open_short"},
	}
	biases := map[string]string{
		"NVDA": "bullish",
		"BTC":  "bearish",
		"ETH":  "bearish",
	}

	got, blocked := applyVergexSignalPolicy(decisions, nil, testSignalBias(biases))
	if len(got) != 2 || got[0].Symbol != "xyz:NVDA" || got[1].Symbol != "BTC" {
		t.Fatalf("only direction-matched entries should pass, got %+v", got)
	}
	if len(blocked) != 2 {
		t.Fatalf("expected mismatched and absent entries to be blocked, got %+v", blocked)
	}
}

func TestVergexSignalPolicyDoesNotTreatMissingSnapshotAsSignalExit(t *testing.T) {
	at := testVergexSignalTrader()
	decisions := []kernel.Decision{{Symbol: "xyz:NVDA", Action: "hold"}}
	ctx := &kernel.Context{Positions: []kernel.PositionInfo{{Symbol: "xyz:NVDA", Side: "long"}}}

	got := at.enforceVergexSignalPolicy(decisions, ctx)
	if len(got) != 1 || got[0].Action != "hold" {
		t.Fatalf("missing board snapshot must leave decisions untouched, got %+v", got)
	}
}

func TestVergexSignalPolicyUsesSignalManagedProfitExit(t *testing.T) {
	at := testVergexSignalTrader()
	if !at.usesSignalManagedExit() {
		t.Fatal("Vergex strategy must use signal-managed ordinary exits")
	}
	at.config.StrategyConfig.CoinSource.SourceType = "static"
	if at.usesSignalManagedExit() {
		t.Fatal("non-Vergex strategies must keep their configured take-profit behavior")
	}
}

func TestSignalTPCleanupRunsOnlyOncePerSymbol(t *testing.T) {
	at := testVergexSignalTrader()
	if !at.needsSignalTPCleanup("xyz:NVDA") {
		t.Fatal("first signal-managed hold must schedule a TP cleanup")
	}
	// Exchange-style symbols resolve to the same base, so clearing via either
	// form must suppress future cleanups for that symbol.
	at.markSignalTPCleared("NVDAUSDT")
	if at.needsSignalTPCleanup("xyz:NVDA") {
		t.Fatal("cleared symbol must skip further TP cleanups")
	}
	at.config.StrategyConfig.CoinSource.SourceType = "static"
	if at.needsSignalTPCleanup("SOL") {
		t.Fatal("non-Vergex strategies must not run TP cleanup")
	}
}
