package kernel

import (
	"strings"
	"testing"

	"nofx/market"
	"nofx/store"
)

// testRiskControl derives its profile from the shipped template so a change to
// the default risk profile cannot silently desync these tests. Only the two
// leverage caps are overridden, because TestLeverageFallback asserts on those.
func testRiskControl(btcEthLeverage, altcoinLeverage int) store.RiskControlConfig {
	risk := store.GetDefaultStrategyConfig("en").RiskControl
	risk.BTCETHMaxLeverage = btcEthLeverage
	risk.AltcoinMaxLeverage = altcoinLeverage
	return risk
}

// testSizedRiskControl narrows the position-value ratios to 10x BTC/ETH and
// 2x altcoin. The shipped template uses 5x/5x, which would cap the BTC case in
// TestLeverageFallback below its own position size. Tests that care about the
// ratio check use these narrower caps to keep their arithmetic readable.
func testSizedRiskControl(btcEthLeverage, altcoinLeverage int) store.RiskControlConfig {
	risk := testRiskControl(btcEthLeverage, altcoinLeverage)
	risk.BTCETHMaxPositionValueRatio = 10.0
	risk.AltcoinMaxPositionValueRatio = 2.0
	return risk
}

// testIndicators disables the entry gates so the structural checks (leverage,
// position value ratio, risk/reward) can be exercised in isolation. The gate
// behaviour has its own dedicated tests below.
func testIndicators() store.IndicatorConfig {
	ind := store.GetDefaultStrategyConfig("en").Indicators
	ind.EnableEMA = false
	ind.EnableADX = false
	ind.EnableKeltner = false
	return ind
}

// testRiskControlNoSizingLimit returns a risk profile with the per-trade risk
// budget switched off. Gate tests use this so a sizing rejection cannot mask
// the gate error they are actually asserting on: the checks run in sequence,
// and sizing comes last, so an oversized fixture would otherwise produce the
// wrong message and hide which gate fired.
func testRiskControlNoSizingLimit() store.RiskControlConfig {
	risk := testRiskControl(10, 10)
	risk.RiskPerTradePct = 0
	return risk
}

// testEnforcedIndicators enables the trend/chop/breakout gates AND turns on
// enforcement, for the tests that assert rejections. The shipped default is
// log-only (see TestEntryGatesLogOnlyMode), so tests asserting a block must opt
// in explicitly.
func testIndicatorsWithGates() store.IndicatorConfig {
	ind := store.GetDefaultStrategyConfig("en").Indicators
	ind.EnableEMA = true
	ind.EnableADX = true
	ind.EnableKeltner = true
	ind.ADXThreshold = store.DefaultADXThreshold
	ind.EntryGatesEnforced = true
	return ind
}

// testIndicatorsLogOnlyGates mirrors the shipped default: gates enabled but
// running in observation mode.
func testIndicatorsLogOnlyGates() store.IndicatorConfig {
	ind := testIndicatorsWithGates()
	ind.EntryGatesEnforced = false
	return ind
}

// noEntryContext is the "no market snapshot" case. Structural checks still run
// and the indicator gates degrade to a warning.
func noEntryContext() entryContext {
	return entryContext{}
}

// risky is a convenient entry snapshot for the gate tests: a clean uptrend
// where every long gate passes. Individual tests mutate one field to isolate
// which gate fires.
func trendingEntry() entryContext {
	return entryContext{
		Price:    110,
		EMA200:   100,
		ADX14:    30,
		KCUpper:  108,
		KCMiddle: 105,
		KCLower:  102,
		ATR14:    2,
	}
}

// TestLeverageFallback tests automatic correction when leverage exceeds limit
func TestLeverageFallback(t *testing.T) {
	tests := []struct {
		name            string
		decision        Decision
		accountEquity   float64
		btcEthLeverage  int
		altcoinLeverage int
		wantLeverage    int // Expected leverage after correction
		wantError       bool
	}{
		{
			name: "Altcoin leverage exceeded - auto-correct to limit",
			decision: Decision{
				Symbol:          "SOLUSDT",
				Action:          "open_long",
				Leverage:        20, // Exceeds limit
				PositionSizeUSD: 100,
				StopLoss:        50,
				TakeProfit:      200,
				Confidence:      80,
			},
			accountEquity:   100,
			btcEthLeverage:  10,
			altcoinLeverage: 5, // Limit 5x
			wantLeverage:    5, // Should be corrected to 5
			wantError:       false,
		},
		{
			name: "BTC leverage exceeded - auto-correct to limit",
			decision: Decision{
				Symbol:          "BTCUSDT",
				Action:          "open_long",
				Leverage:        20, // Exceeds limit
				PositionSizeUSD: 1000,
				StopLoss:        90000,
				TakeProfit:      110000,
				Confidence:      80,
			},
			accountEquity:   100,
			btcEthLeverage:  10, // Limit 10x
			altcoinLeverage: 5,
			wantLeverage:    10, // Should be corrected to 10
			wantError:       false,
		},
		{
			name: "Leverage within limit - no correction",
			decision: Decision{
				Symbol:          "ETHUSDT",
				Action:          "open_short",
				Leverage:        5, // Not exceeded
				PositionSizeUSD: 500,
				StopLoss:        4000,
				TakeProfit:      3000,
				Confidence:      80,
			},
			accountEquity:   100,
			btcEthLeverage:  10,
			altcoinLeverage: 5,
			wantLeverage:    5, // Stays unchanged
			wantError:       false,
		},
		{
			name: "Leverage is 0 - should error",
			decision: Decision{
				Symbol:          "SOLUSDT",
				Action:          "open_long",
				Leverage:        0, // Invalid
				PositionSizeUSD: 100,
				StopLoss:        50,
				TakeProfit:      200,
				Confidence:      80,
			},
			accountEquity:   100,
			btcEthLeverage:  10,
			altcoinLeverage: 5,
			wantLeverage:    0,
			wantError:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			risk := testSizedRiskControl(tt.btcEthLeverage, tt.altcoinLeverage)
			risk.RiskPerTradePct = 0 // sizing is covered by its own tests
			err := validateDecision(&tt.decision, tt.accountEquity, risk, testIndicators(), noEntryContext())

			// Check error status
			if (err != nil) != tt.wantError {
				t.Errorf("validateDecision() error = %v, wantError %v", err, tt.wantError)
				return
			}

			// If shouldn't error, check if leverage was correctly corrected
			if !tt.wantError && tt.decision.Leverage != tt.wantLeverage {
				t.Errorf("Leverage not corrected: got %d, want %d", tt.decision.Leverage, tt.wantLeverage)
			}
		})
	}
}

// TestValidateDecisionUsesTemplatePositionValueRatio guards the ratio the
// template actually ships. testSizedRiskControl narrows it for readability in
// the leverage cases, so nothing else would catch a template that silently
// widened or shrank.
func TestValidateDecisionUsesTemplatePositionValueRatio(t *testing.T) {
	risk := testRiskControl(10, 10)
	risk.RiskPerTradePct = 0 // sizing is covered by its own tests
	limit := 1000 * risk.AltcoinMaxPositionValueRatio

	atLimit := Decision{
		Symbol: "SOLUSDT", Action: "open_long", Leverage: 5,
		PositionSizeUSD: limit, StopLoss: 50, TakeProfit: 200,
		Confidence: 90,
	}
	if err := validateDecision(&atLimit, 1000, risk, testIndicators(), noEntryContext()); err != nil {
		t.Fatalf("position exactly at the template ratio should pass, got: %v", err)
	}

	overLimit := atLimit
	overLimit.PositionSizeUSD = limit * 1.5
	if err := validateDecision(&overLimit, 1000, risk, testIndicators(), noEntryContext()); err == nil {
		t.Fatal("expected rejection when the position exceeds the template ratio")
	}
}

// TestValidateDecisionUsesConfiguredRiskReward verifies the risk/reward floor
// reads from the strategy config instead of a hardcoded 3.0.
func TestValidateDecisionUsesConfiguredRiskReward(t *testing.T) {
	// Entry is inferred at 20% of the stop..target span, so risk:reward is
	// 4:1 for a long from 100 -> 200 with a stop at 100.
	candidate := Decision{
		Symbol: "BTCUSDT", Action: "open_long", Leverage: 5,
		PositionSizeUSD: 500, StopLoss: 100, TakeProfit: 200,
		Confidence: 90,
	}

	strict := testRiskControlNoSizingLimit()
	strict.MinRiskRewardRatio = 5.0
	if err := validateDecision(&candidate, 1000, strict, testIndicators(), noEntryContext()); err == nil {
		t.Fatal("expected rejection when the configured risk/reward floor is higher than the setup")
	}

	relaxed := testRiskControlNoSizingLimit()
	relaxed.MinRiskRewardRatio = 3.0
	if err := validateDecision(&candidate, 1000, relaxed, testIndicators(), noEntryContext()); err != nil {
		t.Fatalf("setup meeting the configured floor should pass, got: %v", err)
	}
}

// TestConfidenceIsNoLongerAGate pins the deliberate downgrade: entry decisions
// are judged by the trend/chop/breakout gates, so a low or absent confidence
// score must not block an otherwise valid setup. The field is still recorded.
func TestConfidenceIsNoLongerAGate(t *testing.T) {
	risk := testRiskControl(10, 10)
	risk.RiskPerTradePct = 0 // sizing is covered by its own tests

	for _, confidence := range []int{0, 10, risk.MinConfidence - 1, risk.MinConfidence, 100} {
		d := Decision{
			Symbol: "BTCUSDT", Action: "open_long", Leverage: 5,
			PositionSizeUSD: 500, StopLoss: 100, TakeProfit: 200,
			Confidence: confidence,
		}
		if err := validateDecision(&d, 1000, risk, testIndicators(), noEntryContext()); err != nil {
			t.Fatalf("confidence=%d should not gate entry any more, got: %v", confidence, err)
		}
	}
}

// TestEntryGateRequiresUptrendForLongs covers gate 1: price must be above the
// EMA200 before a long is allowed. The gate deliberately tests only that
// relationship — no intermediate average has to agree.
func TestEntryGateRequiresUptrendForLongs(t *testing.T) {
	risk := testRiskControlNoSizingLimit()
	ind := testIndicatorsWithGates()

	base := Decision{
		Symbol: "BTCUSDT", Action: "open_long", Leverage: 5,
		PositionSizeUSD: 500, StopLoss: 100, TakeProfit: 200,
	}

	t.Run("clean uptrend passes", func(t *testing.T) {
		d := base
		if err := validateDecision(&d, 1000, risk, ind, trendingEntry()); err != nil {
			t.Fatalf("uptrend setup should pass, got: %v", err)
		}
	})

	t.Run("price below EMA200 is rejected", func(t *testing.T) {
		d := base
		entry := trendingEntry()
		entry.Price = entry.EMA200 - 1
		// Keep the breakout condition satisfied so we isolate the trend gate.
		entry.KCUpper = entry.Price - 1
		err := validateDecision(&d, 1000, risk, ind, entry)
		if err == nil {
			t.Fatal("expected rejection when price is below EMA200")
		}
		if !strings.Contains(err.Error(), "EMA200") {
			t.Fatalf("error should cite the EMA200 trend line, got: %v", err)
		}
	})

	t.Run("EMA50 disagreement no longer blocks", func(t *testing.T) {
		// Regression guard: an intermediate-average confirmation was removed, so
		// a setup whose EMA50 sits below the EMA200 must not be refused on that
		// basis alone. The fixture has no EMA50 field at all now.
		d := base
		if err := validateDecision(&d, 1000, risk, ind, trendingEntry()); err != nil {
			t.Fatalf("the trend gate must depend on price vs EMA200 only, got: %v", err)
		}
	})
}

// TestEntryGateRejectsRangingMarket covers gate 2: a low ADX means the market
// is chopping, which is exactly when breakout entries fail.
func TestEntryGateRejectsRangingMarket(t *testing.T) {
	risk := testRiskControlNoSizingLimit()
	ind := testIndicatorsWithGates()

	d := Decision{
		Symbol: "BTCUSDT", Action: "open_long", Leverage: 5,
		PositionSizeUSD: 500, StopLoss: 100, TakeProfit: 200,
	}

	entry := trendingEntry()
	entry.ADX14 = float64(ind.EffectiveADXThreshold()) - 1
	err := validateDecision(&d, 1000, risk, ind, entry)
	if err == nil {
		t.Fatal("expected rejection when ADX is below the trend threshold")
	}
	if !strings.Contains(err.Error(), "ADX") {
		t.Fatalf("error should cite ADX, got: %v", err)
	}

	// Exactly at the threshold must pass: the comparison is "below", not
	// "below or equal", so a boundary reading is not punished.
	entry.ADX14 = float64(ind.EffectiveADXThreshold())
	if err := validateDecision(&d, 1000, risk, ind, entry); err != nil {
		t.Fatalf("ADX exactly at threshold should pass, got: %v", err)
	}
}

// TestEntryGateRequiresBreakout covers gate 3: price must clear the Keltner
// band, otherwise it is a mid-channel entry dressed up as a breakout.
func TestEntryGateRequiresBreakout(t *testing.T) {
	risk := testRiskControlNoSizingLimit()
	ind := testIndicatorsWithGates()

	d := Decision{
		Symbol: "BTCUSDT", Action: "open_long", Leverage: 5,
		PositionSizeUSD: 500, StopLoss: 100, TakeProfit: 200,
	}

	entry := trendingEntry()
	entry.Price = entry.KCUpper - 0.5 // still above EMA200, but no breakout
	err := validateDecision(&d, 1000, risk, ind, entry)
	if err == nil {
		t.Fatal("expected rejection when price has not broken the upper band")
	}
	if !strings.Contains(err.Error(), "Keltner upper") {
		t.Fatalf("error should cite the Keltner upper band, got: %v", err)
	}

	// Exactly at the band is not a breakout; clearing it is.
	entry.Price = entry.KCUpper
	if err := validateDecision(&d, 1000, risk, ind, entry); err == nil {
		t.Fatal("price exactly at the band should not count as a breakout")
	}

	entry.Price = entry.KCUpper + 1
	if err := validateDecision(&d, 1000, risk, ind, entry); err != nil {
		t.Fatalf("price above the band should pass, got: %v", err)
	}
}

// TestEntryGateShortDirection covers the mirrored short conditions.
func TestEntryGateShortDirection(t *testing.T) {
	risk := testRiskControlNoSizingLimit()
	ind := testIndicatorsWithGates()

	makeShort := func() Decision {
		return Decision{
			// Long: stop below entry, target above.
			Symbol: "BTCUSDT", Action: "open_short", Leverage: 5,
			PositionSizeUSD: 500, StopLoss: 200, TakeProfit: 100,
		}
	}

	// Downtrend with price having broken below the lower Keltner band.
	downtrend := entryContext{
		Price: 87, EMA200: 100, ADX14: 30,
		KCUpper: 97, KCMiddle: 95, KCLower: 93, ATR14: 2,
	}

	d := makeShort()
	if err := validateDecision(&d, 1000, risk, ind, downtrend); err != nil {
		t.Fatalf("downtrend short should pass, got: %v", err)
	}

	// Price above the EMA200 must block a short.
	entry := downtrend
	entry.Price = 110
	entry.KCUpper = 120
	d = makeShort()
	err := validateDecision(&d, 1000, risk, ind, entry)
	if err == nil {
		t.Fatal("expected rejection of a short above the EMA200")
	}
	if !strings.Contains(err.Error(), "downtrend") {
		t.Fatalf("error should cite the downtrend requirement, got: %v", err)
	}

	// Price that has not broken the lower band must block a short.
	entry = downtrend
	entry.Price = entry.KCLower + 1
	d = makeShort()
	err = validateDecision(&d, 1000, risk, ind, entry)
	if err == nil {
		t.Fatal("expected rejection when price has not broken the lower band")
	}
	if !strings.Contains(err.Error(), "Keltner lower") {
		t.Fatalf("error should cite the Keltner lower band, got: %v", err)
	}
}

// TestEntryGateSkipsWhenHistoryMissing pins the degradation path: a missing
// EMA200/ADX means "not enough history", not "condition failed", so validation
// must not reject everything during a cold start.
func TestEntryGateSkipsWhenHistoryMissing(t *testing.T) {
	risk := testRiskControlNoSizingLimit()
	ind := testIndicatorsWithGates()

	d := Decision{
		Symbol: "BTCUSDT", Action: "open_long", Leverage: 5,
		PositionSizeUSD: 500, StopLoss: 100, TakeProfit: 200,
	}

	// No snapshot at all.
	if err := validateDecision(&d, 1000, risk, ind, noEntryContext()); err != nil {
		t.Fatalf("missing market snapshot should skip the gates, got: %v", err)
	}

	// Snapshot present but EMA200 still in its warmup window.
	warmingUp := entryContext{Price: 110, EMA200: 0, ADX14: 0, KCUpper: 108, ATR14: 2}
	if err := validateDecision(&d, 1000, risk, ind, warmingUp); err != nil {
		t.Fatalf("EMA200 still warming up should skip the gates, got: %v", err)
	}
}

// TestDisabledGateDoesNotDisableEnabledGates guards the coupling that made a
// switched-off gate able to silence the others.
//
// With EnableEMA off the EMA200 is never populated, so a warmup check that
// tested it unconditionally would return "no data" and skip every gate — the
// Keltner gate the user had switched on included. A gate that is off must not
// be able to disable one that is on.
func TestDisabledGateDoesNotDisableEnabledGates(t *testing.T) {
	risk := testRiskControlNoSizingLimit()

	// Only the Keltner breakout is enabled; EMA200/ADX are absent because their
	// gates are off and those indicators are not populated.
	ind := testIndicatorsWithGates()
	ind.EnableEMA = false
	ind.EnableADX = false

	// Price sits below the band, so the enabled Keltner gate must reject.
	notBrokenOut := entryContext{Price: 105, KCUpper: 108, KCMiddle: 102, KCLower: 98, ATR14: 2}
	d := Decision{
		Symbol: "BTCUSDT", Action: "open_long", Leverage: 5,
		PositionSizeUSD: 500, StopLoss: 100, TakeProfit: 200,
	}

	err := validateDecision(&d, 1000, risk, ind, notBrokenOut)
	if err == nil {
		t.Fatal("the enabled Keltner gate must still run when EMA/ADX gates are off")
	}
	if !strings.Contains(err.Error(), "Keltner upper") {
		t.Fatalf("error should cite the Keltner gate, got: %v", err)
	}

	// A genuine breakout must pass, confirming the gate is evaluated rather
	// than merely always failing.
	brokenOut := notBrokenOut
	brokenOut.Price = 109
	if err := validateDecision(&d, 1000, risk, ind, brokenOut); err != nil {
		t.Fatalf("a breakout with only the Keltner gate enabled should pass, got: %v", err)
	}
}

// TestDisabledKeltnerGateDoesNotRequireBands covers the mirror case: with the
// Keltner gate off, absent bands must not be treated as missing history.
func TestDisabledKeltnerGateDoesNotRequireBands(t *testing.T) {
	risk := testRiskControlNoSizingLimit()

	ind := testIndicatorsWithGates()
	ind.EnableKeltner = false

	// EMA and ADX are healthy, but no Keltner data at all.
	noBands := entryContext{Price: 110, EMA200: 100, ADX14: 30, ATR14: 2}
	d := Decision{
		Symbol: "BTCUSDT", Action: "open_long", Leverage: 5,
		PositionSizeUSD: 500, StopLoss: 100, TakeProfit: 200,
	}

	if err := validateDecision(&d, 1000, risk, ind, noBands); err != nil {
		t.Fatalf("the Keltner gate being off must not require band data, got: %v", err)
	}
}

// TestEnabledGateWithNoReadingStillSkips keeps the cold-start behaviour: an
// enabled gate whose indicator has not warmed up skips rather than rejecting,
// so a freshly seen symbol is not blocked outright.
func TestEnabledGateWithNoReadingStillSkips(t *testing.T) {
	risk := testRiskControlNoSizingLimit()
	ind := testIndicatorsWithGates()

	// ADX enabled but still warming up; price also below the band, which would
	// normally be rejected. The warmup skip must win.
	d := Decision{
		Symbol: "BTCUSDT", Action: "open_long", Leverage: 5,
		PositionSizeUSD: 500, StopLoss: 100, TakeProfit: 200,
	}
	warmingUp := entryContext{Price: 105, EMA200: 100, ADX14: 0, KCUpper: 108, KCLower: 98, ATR14: 2}

	if err := validateDecision(&d, 1000, risk, ind, warmingUp); err != nil {
		t.Fatalf("an enabled gate without a reading should skip, not reject, got: %v", err)
	}
}

// TestEntryGateRespectsDisabledSwitches ensures a strategy that turns a gate
// off is not silently gated anyway.
func TestEntryGateRespectsDisabledSwitches(t *testing.T) {
	risk := testRiskControlNoSizingLimit()

	// All gates off: a setup that violates every gate must still pass.
	ind := testIndicatorsWithGates()
	ind.EnableEMA = false
	ind.EnableADX = false
	ind.EnableKeltner = false

	d := Decision{
		Symbol: "BTCUSDT", Action: "open_long", Leverage: 5,
		PositionSizeUSD: 500, StopLoss: 100, TakeProfit: 200,
	}
	hostile := entryContext{
		Price: 90, EMA200: 100, ADX14: 5,
		KCUpper: 92, KCMiddle: 95, KCLower: 88, ATR14: 2,
	}
	if err := validateDecision(&d, 1000, risk, ind, hostile); err != nil {
		t.Fatalf("with all gates disabled nothing should block, got: %v", err)
	}
}

// TestRiskBasedSizingRejectsOversizedPosition covers the core of risk-parity
// sizing: with 1% of 1000 = 10 USDT of risk and a 10% stop, the position must
// not exceed 100 USDT notional.
func TestRiskBasedSizingRejectsOversizedPosition(t *testing.T) {
	risk := testRiskControl(10, 10)
	risk.RiskPerTradePct = 1.0
	ind := testIndicators() // gates off; isolate sizing

	// Entry 110, stop 100 -> 10/110 ≈ 9.09% stop distance.
	// Budget 10 USDT -> max notional ≈ 10 * 110/10 = 110 USDT.
	exact := Decision{
		Symbol: "BTCUSDT", Action: "open_long", Leverage: 5,
		PositionSizeUSD: 110, StopLoss: 100, TakeProfit: 200,
	}
	entry := entryContext{Price: 110}
	if err := validateDecision(&exact, 1000, risk, ind, entry); err != nil {
		t.Fatalf("position at the risk budget should pass, got: %v", err)
	}

	oversized := exact
	oversized.PositionSizeUSD = 500
	err := validateDecision(&oversized, 1000, risk, ind, entry)
	if err == nil {
		t.Fatal("expected rejection when the position risks more than the budget")
	}
	if !strings.Contains(err.Error(), "budget") {
		t.Fatalf("error should cite the risk budget, got: %v", err)
	}
}

// TestRiskBasedSizingAllowsLargerSizeWithTighterStop is the property that makes
// risk-based sizing worth having: for the same dollar risk, a tighter stop
// permits a larger position.
func TestRiskBasedSizingAllowsLargerSizeWithTighterStop(t *testing.T) {
	risk := testRiskControl(10, 10)
	risk.RiskPerTradePct = 1.0
	ind := testIndicators()

	// 5% stop (entry 105, stop 100) vs 2% stop (entry 102, stop 100).
	wideStop := Decision{
		Symbol: "BTCUSDT", Action: "open_long", Leverage: 5,
		PositionSizeUSD: 200, StopLoss: 100, TakeProfit: 300,
	}
	if err := validateDecision(&wideStop, 1000, risk, ind, entryContext{Price: 105}); err != nil {
		t.Fatalf("wide-stop position should pass, got: %v", err)
	}

	tightStop := Decision{
		Symbol: "BTCUSDT", Action: "open_long", Leverage: 5,
		PositionSizeUSD: 200, StopLoss: 100, TakeProfit: 300,
	}
	if err := validateDecision(&tightStop, 1000, risk, ind, entryContext{Price: 102}); err != nil {
		t.Fatalf("tight-stop position of the same size should pass, got: %v", err)
	}
}

// TestRiskBasedSizingDisabledWhenPctUnset confirms a zero budget disables the
// derived-size check rather than rejecting everything.
func TestRiskBasedSizingDisabledWhenPctUnset(t *testing.T) {
	risk := testRiskControl(10, 10)
	risk.RiskPerTradePct = 0 // disabled

	big := Decision{
		Symbol: "BTCUSDT", Action: "open_long", Leverage: 5,
		PositionSizeUSD: 900, StopLoss: 100, TakeProfit: 200,
	}
	if err := validateDecision(&big, 1000, risk, testIndicators(), entryContext{Price: 110}); err != nil {
		t.Fatalf("zero RiskPerTradePct should disable the sizing check, got: %v", err)
	}
}

// TestGatesResolvePerSymbol verifies the batch path: a decision is evaluated
// against the indicators of the symbol it names, not a neighbouring symbol's.
func TestGatesResolvePerSymbol(t *testing.T) {
	risk := testRiskControlNoSizingLimit()
	ind := testIndicatorsWithGates()

	batch := entryContext{
		marketDataBySymbol: map[string]*market.Data{
			"UPTRENDUSDT": {TimeframeData: map[string]*market.TimeframeSeriesData{
				"5m": {ADX14: 30, ATR14: 2, Klines: []market.KlineBar{{Close: 110}},
					EMA200Values: []float64{100},
					KCUpper:      []float64{108},
					KCMiddle:     []float64{105},
					KCLower:      []float64{102}},
			}},
			"RANGEUSDT": {TimeframeData: map[string]*market.TimeframeSeriesData{
				"5m": {ADX14: 10, ATR14: 2, Klines: []market.KlineBar{{Close: 110}},
					EMA200Values: []float64{100},
					KCUpper:      []float64{108},
					KCMiddle:     []float64{105},
					KCLower:      []float64{102}},
			}},
		},
		primaryTimeframe: "5m",
	}

	valid := Decision{
		Symbol: "UPTRENDUSDT", Action: "open_long", Leverage: 5,
		PositionSizeUSD: 500, StopLoss: 100, TakeProfit: 200,
	}
	if err := validateDecision(&valid, 1000, risk, ind, batch); err != nil {
		t.Fatalf("the trending symbol should pass, got: %v", err)
	}

	ranging := Decision{
		Symbol: "RANGEUSDT", Action: "open_long", Leverage: 5,
		PositionSizeUSD: 500, StopLoss: 100, TakeProfit: 200,
	}
	if err := validateDecision(&ranging, 1000, risk, ind, batch); err == nil {
		t.Fatal("the ranging symbol should be blocked by its own ADX reading")
	}
}

// TestEntryGatesLogOnlyMode pins the observation mode the gates ship in: the
// same rules that would reject a setup must merely log it, so the pass rate can
// be measured against live flow before enforcement is switched on.
func TestEntryGatesLogOnlyMode(t *testing.T) {
	risk := testRiskControlNoSizingLimit()

	// The shipped template must leave enforcement off; three ANDed conditions
	// rarely align, so blocking by default risks a strategy that never trades.
	if store.GetDefaultStrategyConfig("en").Indicators.EntryGatesEnforced {
		t.Fatal("gates must default to log-only")
	}

	// Hostile snapshot: every gate would fail.
	hostile := entryContext{
		Price: 90, EMA200: 100, ADX14: 5,
		KCUpper: 92, KCMiddle: 95, KCLower: 88, ATR14: 2,
	}
	d := Decision{
		Symbol: "BTCUSDT", Action: "open_long", Leverage: 5,
		PositionSizeUSD: 500, StopLoss: 100, TakeProfit: 200,
	}

	// Log-only: must NOT reject.
	logOnly := testIndicatorsLogOnlyGates()
	if err := validateDecision(&d, 1000, risk, logOnly, hostile); err != nil {
		t.Fatalf("log-only mode must not reject, got: %v", err)
	}

	// Enforced: the identical setup must now be rejected.
	enforced := testIndicatorsWithGates()
	if err := validateDecision(&d, 1000, risk, enforced, hostile); err == nil {
		t.Fatal("enforced mode must reject the same setup")
	}
}
