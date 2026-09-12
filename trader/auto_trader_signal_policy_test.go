package trader

import (
	"errors"
	"testing"

	"nofx/kernel"
	"nofx/provider/hyperliquid"
	"nofx/store"
	tradertypes "nofx/trader/types"
)

type emergencyCloseTestTrader struct {
	tradertypes.Trader
	closeCalls    int
	invalidations int
	cancelCalls   int
	positions     []map[string]interface{}
	openOrders    []tradertypes.OpenOrder
	cancelErr     error
}

func (t *emergencyCloseTestTrader) CloseLong(string, float64) (map[string]interface{}, error) {
	t.closeCalls++
	return map[string]interface{}{"status": "submitted"}, nil
}

func (t *emergencyCloseTestTrader) CloseShort(string, float64) (map[string]interface{}, error) {
	t.closeCalls++
	return map[string]interface{}{"status": "submitted"}, nil
}

func (t *emergencyCloseTestTrader) InvalidatePositionCache() { t.invalidations++ }
func (t *emergencyCloseTestTrader) GetPositions() ([]map[string]interface{}, error) {
	return t.positions, nil
}
func (t *emergencyCloseTestTrader) CancelAllOrders(string) error {
	t.cancelCalls++
	return t.cancelErr
}
func (t *emergencyCloseTestTrader) GetOpenOrders(string) ([]tradertypes.OpenOrder, error) {
	return t.openOrders, nil
}

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

// testSignalStrength reports a score above the entry floor for every symbol
// the bias map knows about, so legacy tests exercise the hold/close paths
// rather than the decay trims or the entry gate.
func testSignalStrength(values map[string]string) func(string) (float64, bool) {
	return func(symbol string) (float64, bool) {
		_, ok := values[universeBaseKey(symbol)]
		return signalOpenScore, ok
	}
}

// testSignalPolicy runs the pure state machine with a throwaway AutoTrader and
// matching bias/strength maps, keeping the tests focused on outcomes.
func testSignalPolicy(decisions []kernel.Decision, positions []kernel.PositionInfo, values map[string]string) ([]kernel.Decision, []kernel.Decision) {
	at := &AutoTrader{signalAbsentCycles: make(map[string]int)}
	return applyVergexSignalPolicy(decisions, positions, testSignalBias(values), testSignalStrength(values), at)
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

	got, blocked := testSignalPolicy(decisions, positions, map[string]string{
		"NVDA": "bullish",
		"BTC":  "bearish",
	})
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

	got, blocked := testSignalPolicy(decisions, positions, map[string]string{"NVDA": "bullish"})
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

	got, blocked := testSignalPolicy(decisions, positions, map[string]string{
		"NVDA": "bearish",
		"ETH":  "neutral",
	})
	if len(got) != 3 {
		t.Fatalf("every held position must yield a decision, got %+v", got)
	}
	if got[0].Action != "close_long" {
		t.Fatalf("a reversed direction must close the position (no same-cycle flip), got %+v", got[0])
	}
	if got[2].Action != "close_long" {
		t.Fatalf("a neutral direction must close the position, got %+v", got[2])
	}
	// An absent symbol is only trimmed on the first board cycle, not closed —
	// a pure ranking drop-out must be confirmed before it becomes a full exit.
	if got[1].Action != "reduce_short" || got[1].ReducePct <= 0 {
		t.Fatalf("a board-absent symbol must be trimmed while confirming, got %+v", got[1])
	}
	if len(blocked) != 2 {
		t.Fatalf("a reversed position must close without flipping in the same cycle, blocked=%+v", blocked)
	}
}

func TestVergexSignalPolicyClosesAfterAbsenceGracePeriod(t *testing.T) {
	positions := []kernel.PositionInfo{{Symbol: "BTC", Side: "short"}}

	at := &AutoTrader{signalAbsentCycles: make(map[string]int)}
	run := func() []kernel.Decision {
		got, _ := applyVergexSignalPolicy(nil, positions, testSignalBias(map[string]string{}), testSignalStrength(map[string]string{}), at)
		return got
	}

	// Each call is one board cycle with the symbol absent.
	for cycle := 1; cycle <= signalAbsentGraceCycles; cycle++ {
		got := run()
		if len(got) != 1 || !isReduceAction(got[0].Action) {
			t.Fatalf("cycle %d: absence within the grace period must trim, not close, got %+v", cycle, got)
		}
	}
	got := run()
	if len(got) != 1 || got[0].Action != "close_short" {
		t.Fatalf("absence beyond the grace period must close the position, got %+v", got)
	}
}

func TestVergexSignalPolicyTrimsOnDecayingSignal(t *testing.T) {
	positions := []kernel.PositionInfo{{Symbol: "xyz:NVDA", Side: "long"}}

	strength := func(score float64) func(string) (float64, bool) {
		return func(string) (float64, bool) { return score, true }
	}

	cases := []struct {
		name       string
		score      float64
		wantAction string
	}{
		{"strong holds", signalStrongScore + 0.1, "hold"},
		{"medium trims a third", (signalStrongScore + signalWeakScore) / 2, "reduce_long"},
		{"weak trims two thirds", signalWeakScore - 0.1, "reduce_long"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			at := &AutoTrader{signalAbsentCycles: make(map[string]int)}
			got, _ := applyVergexSignalPolicy(nil, positions,
				testSignalBias(map[string]string{"NVDA": "bullish"}), strength(tc.score), at)
			if len(got) != 1 || got[0].Action != tc.wantAction {
				t.Fatalf("score %.2f: want %s, got %+v", tc.score, tc.wantAction, got)
			}
			if tc.wantAction == "reduce_long" && got[0].ReducePct <= 0 {
				t.Fatalf("score %.2f: a trim must carry a reduce fraction, got %+v", tc.score, got[0])
			}
		})
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

	got, blocked := testSignalPolicy(decisions, nil, biases)
	if len(got) != 2 || got[0].Symbol != "xyz:NVDA" || got[1].Symbol != "BTC" {
		t.Fatalf("only direction-matched entries should pass, got %+v", got)
	}
	if len(blocked) != 2 {
		t.Fatalf("expected mismatched and absent entries to be blocked, got %+v", blocked)
	}
}

func TestVergexSignalPolicyRequiresStrongSignalToOpen(t *testing.T) {
	// A faded-but-still-matching board must not rebuild a position the exit side
	// just closed, and a merely-intact signal must not open one at all: entries
	// are gated above the hold floor.
	decisions := []kernel.Decision{{Symbol: "xyz:NVDA", Action: "open_long"}}
	bias := testSignalBias(map[string]string{"NVDA": "bullish"})

	cases := []struct {
		name    string
		score   float64
		allowed bool
	}{
		{"opens at the entry floor", signalOpenScore, true},
		{"just below the entry floor is blocked", signalOpenScore - 0.01, false},
		{"intact for a hold but too weak to open", signalStrongScore, false},
		{"medium is blocked", (signalStrongScore + signalWeakScore) / 2, false},
		{"weak is blocked", signalWeakScore - 0.1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			at := &AutoTrader{signalAbsentCycles: make(map[string]int)}
			strength := func(string) (float64, bool) { return tc.score, true }
			got, blocked := applyVergexSignalPolicy(decisions, nil, bias, strength, at)
			if tc.allowed && (len(got) != 1 || len(blocked) != 0) {
				t.Fatalf("score %.2f: expected the entry to pass, got %+v blocked %+v", tc.score, got, blocked)
			}
			if !tc.allowed && (len(got) != 0 || len(blocked) != 1) {
				t.Fatalf("score %.2f: expected the entry to be blocked, got %+v blocked %+v", tc.score, got, blocked)
			}
		})
	}
}

func TestVergexSignalPolicyIgnoresPriceGiveback(t *testing.T) {
	// A position that has given back most of its peak is still held while the
	// board signal is strong: price-drawdown exits are the drawdown monitor's
	// job (auto_trader_risk.go, per-minute and peak-cache-clearing), not the
	// signal state machine's. This prevents the double-exit and stale-peak
	// re-trigger loop the two overlapping mechanisms used to cause.
	positions := []kernel.PositionInfo{{
		Symbol: "xyz:NVDA", Side: "long", Leverage: 1,
		UnrealizedPnLPct: 5.0, PeakPnLPct: 10.0,
	}}
	at := &AutoTrader{signalAbsentCycles: make(map[string]int)}
	bias := testSignalBias(map[string]string{"NVDA": "bullish"})
	strong := func(string) (float64, bool) { return signalStrongScore + 0.5, true }

	got, _ := applyVergexSignalPolicy(nil, positions, bias, strong, at)
	if len(got) != 1 || got[0].Action != "hold" {
		t.Fatalf("price giveback alone must not exit a strong-signal position, got %+v", got)
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

func TestLegacyVergexFieldsDoNotChangeFixedExitMode(t *testing.T) {
	at := testVergexSignalTrader()
	at.config.StrategyConfig.CoinSource.SourceType = "claw402"
	at.config.StrategyConfig.CoinSource.VergexLimit = 5
	at.config.StrategyConfig.CoinSource.VergexMarketType = "all"
	if at.usesSignalManagedExit() {
		t.Fatal("legacy Vergex fields must not turn a claw402 strategy into signal-managed mode")
	}
}

func TestValidateProtectionPrices(t *testing.T) {
	tests := []struct {
		name, action           string
		market, sl, tp         float64
		signalManaged, wantErr bool
	}{
		{"fixed long valid", "open_long", 100, 95, 120, false, false},
		{"signal long valid", "open_long", 100, 95, 0, true, false},
		{"long stop above market", "open_long", 100, 101, 120, false, true},
		{"long target below market", "open_long", 100, 95, 99, false, true},
		{"fixed short valid", "open_short", 100, 105, 80, false, false},
		{"signal short valid", "open_short", 100, 105, 0, true, false},
		{"short stop below market", "open_short", 100, 99, 80, false, true},
		{"short target above market", "open_short", 100, 105, 101, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateProtectionPrices(tt.action, tt.market, tt.sl, tt.tp, tt.signalManaged)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error=%v wantErr=%v", err, tt.wantErr)
			}
		})
	}
}

func TestEmergencyCloseUsesFreshPositionsAndVerifiesOrderCleanup(t *testing.T) {
	fake := &emergencyCloseTestTrader{}
	at := &AutoTrader{trader: fake}
	if err := at.emergencyClosePositionAndVerify("BTCUSDT", "long", 1); err != nil {
		t.Fatalf("emergency close failed: %v", err)
	}
	if fake.closeCalls != 1 || fake.invalidations != 1 || fake.cancelCalls != 1 {
		t.Fatalf("calls close=%d invalidate=%d cancel=%d, want 1 each", fake.closeCalls, fake.invalidations, fake.cancelCalls)
	}
}

func TestEmergencyCloseFailsIfProtectionOrdersRemain(t *testing.T) {
	fake := &emergencyCloseTestTrader{openOrders: []tradertypes.OpenOrder{{Symbol: "BTCUSDT"}}}
	at := &AutoTrader{trader: fake}
	if err := at.emergencyClosePositionAndVerify("BTCUSDT", "long", 1); err == nil {
		t.Fatal("expected remaining order to fail closed")
	}
}

func TestEmergencyCloseFailsIfOrderCleanupFails(t *testing.T) {
	fake := &emergencyCloseTestTrader{cancelErr: errors.New("cancel rejected")}
	at := &AutoTrader{trader: fake}
	if err := at.emergencyClosePositionAndVerify("BTCUSDT", "long", 1); err == nil {
		t.Fatal("expected cleanup error to fail closed")
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

func TestTrimQuantity(t *testing.T) {
	cases := []struct {
		name             string
		held, pct, price float64
		wantQty          float64
		wantFullClose    bool
	}{
		{"full close by zero pct", 1.0, 0, 100, 0, true},
		{"full close by pct >= 1", 1.0, 1.0, 100, 0, true},
		{"plain trim", 1.0, 1.0 / 3.0, 100, 1.0 / 3.0, false},
		{"dust remainder closes all", 0.1, 0.5, 100, 0, true}, // remaining 5 USDT < 12
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			qty, fullClose := trimQuantity(tc.held, tc.pct, tc.price)
			if qty != tc.wantQty || fullClose != tc.wantFullClose {
				t.Fatalf("held=%.4f pct=%.4f price=%.2f: got (%.6f, %v), want (%.6f, %v)",
					tc.held, tc.pct, tc.price, qty, fullClose, tc.wantQty, tc.wantFullClose)
			}
		})
	}
}

func TestPlanClose(t *testing.T) {
	// A plain close records the held quantity while the exchange gets close-all.
	plan, err := planClose("close_long", "BTC", 1.0, 0, 100)
	if err != nil || plan.exchangeQty != 0 || plan.recordQty != 1.0 || plan.dustClose {
		t.Fatalf("plain close: got %+v, err %v", plan, err)
	}

	// A normal trim closes only its fraction.
	plan, err = planClose("reduce_long", "BTC", 1.0, 1.0/3.0, 100)
	if err != nil || plan.exchangeQty != 1.0/3.0 || plan.recordQty != 1.0/3.0 || plan.dustClose {
		t.Fatalf("plain trim: got %+v, err %v", plan, err)
	}

	// A trim stranding dust escalates to a full close and says so.
	plan, err = planClose("reduce_long", "BTC", 0.1, 0.5, 100)
	if err != nil || plan.exchangeQty != 0 || plan.recordQty != 0.1 || !plan.dustClose {
		t.Fatalf("dust trim: got %+v, err %v", plan, err)
	}

	// A reduce with nothing held is an error, not a silent close-all.
	if _, err = planClose("reduce_long", "BTC", 0, 0.5, 100); err == nil {
		t.Fatal("reduce with nothing held must error")
	}
}

func TestCorrelationGroupSplitsBroadCategories(t *testing.T) {
	cases := []struct {
		a, b string
		same bool
	}{
		// The pair that motivated the rule: two crude grades are one oil bet.
		{"xyz:CL", "xyz:BRENTOIL", true},
		// Broad category would have merged these; the fine group must not.
		{"xyz:CL", "xyz:GOLD", false},
		{"xyz:GOLD", "xyz:SILVER", true},
		{"xyz:COPPER", "xyz:GOLD", false},
		// Semiconductors cluster.
		{"xyz:NVDA", "xyz:SNDK", true},
		{"xyz:NVDA", "xyz:AAPL", false},
		// Crypto majors group; alts stay unconstrained.
		{"BTCUSDT", "ETHUSDT", true},
		{"SOLUSDT", "ZECUSDT", false},
		// Unrelated/unknown instruments carry no group.
		{"xyz:SNDK", "SOLUSDT", false},
	}
	for _, tc := range cases {
		ga, gb := hyperliquid.CorrelationGroup(tc.a), hyperliquid.CorrelationGroup(tc.b)
		got := ga != "" && ga == gb
		if got != tc.same {
			t.Fatalf("%s(%q) vs %s(%q): same=%v, want %v", tc.a, ga, tc.b, gb, got, tc.same)
		}
	}
}

func TestVergexSignalPolicyBlocksCorrelatedDoubleUp(t *testing.T) {
	// WTI is already held; a Brent open in the same cycle must be blocked.
	positions := []kernel.PositionInfo{{Symbol: "xyz:CL", Side: "long"}}
	decisions := []kernel.Decision{{Symbol: "xyz:BRENTOIL", Action: "open_long"}}
	at := &AutoTrader{signalAbsentCycles: make(map[string]int)}
	bias := testSignalBias(map[string]string{"CL": "bullish", "BRENTOIL": "bullish"})
	strong := func(string) (float64, bool) { return signalStrongScore + 0.5, true }

	got, blocked := applyVergexSignalPolicy(decisions, positions, bias, strong, at)
	if len(blocked) != 1 || blocked[0].Symbol != "xyz:BRENTOIL" {
		t.Fatalf("a correlated double-up must be blocked, got blocked %+v (allowed %+v)", blocked, got)
	}
	for _, d := range got {
		if d.Action == "open_long" {
			t.Fatalf("no correlated open may survive, got %+v", got)
		}
	}

	// An uncorrelated strong signal still opens normally.
	decisions = []kernel.Decision{{Symbol: "xyz:AAPL", Action: "open_long"}}
	bias = testSignalBias(map[string]string{"CL": "bullish", "AAPL": "bullish"})
	got, blocked = applyVergexSignalPolicy(decisions, positions, bias, strong, at)
	if len(blocked) != 0 {
		t.Fatalf("an uncorrelated signal must not be blocked, got %+v", blocked)
	}
	opened := false
	for _, d := range got {
		if d.Symbol == "xyz:AAPL" && d.Action == "open_long" {
			opened = true
		}
	}
	if !opened {
		t.Fatalf("an uncorrelated signal must open, got %+v", got)
	}
}

func TestVergexSignalPolicyBlocksCorrelatedPairWithinOneCycle(t *testing.T) {
	// Two correlated opens in the same cycle: the first wins, the second is
	// blocked, so the book never takes both at once.
	decisions := []kernel.Decision{
		{Symbol: "xyz:CL", Action: "open_long"},
		{Symbol: "xyz:BRENTOIL", Action: "open_long"},
	}
	at := &AutoTrader{signalAbsentCycles: make(map[string]int)}
	bias := testSignalBias(map[string]string{"CL": "bullish", "BRENTOIL": "bullish"})
	strong := func(string) (float64, bool) { return signalStrongScore + 0.5, true }

	got, blocked := applyVergexSignalPolicy(decisions, nil, bias, strong, at)
	if len(got) != 1 || got[0].Symbol != "xyz:CL" {
		t.Fatalf("only the first correlated open should pass, got %+v", got)
	}
	if len(blocked) != 1 || blocked[0].Symbol != "xyz:BRENTOIL" {
		t.Fatalf("the second correlated open must be blocked, got %+v", blocked)
	}
}
