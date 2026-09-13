package kernel

import (
	"strings"
	"testing"

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
			err := validateDecision(&tt.decision, tt.accountEquity, risk)

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

// TestValidateDecisionEnforcesMinConfidence verifies the configured confidence
// floor is enforced in code, not only asserted in the prompt.
func TestValidateDecisionEnforcesMinConfidence(t *testing.T) {
	risk := testRiskControl(10, 10)

	below := Decision{
		Symbol: "BTCUSDT", Action: "open_long", Leverage: 5,
		PositionSizeUSD: 500, StopLoss: 90000, TakeProfit: 110000,
		Confidence: risk.MinConfidence - 1,
	}
	err := validateDecision(&below, 1000, risk)
	if err == nil {
		t.Fatal("expected rejection when confidence is below the configured minimum")
	}
	if !strings.Contains(err.Error(), "confidence") {
		t.Fatalf("error should mention confidence, got: %v", err)
	}

	atFloor := below
	atFloor.Confidence = risk.MinConfidence
	if err := validateDecision(&atFloor, 1000, risk); err != nil {
		t.Fatalf("confidence equal to the minimum should pass, got: %v", err)
	}

	// A zero floor disables the check rather than rejecting everything.
	disabled := risk
	disabled.MinConfidence = 0
	noFloor := below
	noFloor.Confidence = 0
	if err := validateDecision(&noFloor, 1000, disabled); err != nil {
		t.Fatalf("zero MinConfidence should disable the check, got: %v", err)
	}
}

// TestValidateDecisionSeparatesMissingConfidenceFromLowConfidence pins the
// distinction between "the model omitted the field" and "the model scored the
// setup below the bar". Both arrive as a low number, but they need different
// operator responses, so the error text must tell them apart.
func TestValidateDecisionSeparatesMissingConfidenceFromLowConfidence(t *testing.T) {
	risk := testRiskControl(10, 10)

	missing := Decision{
		Symbol: "BTCUSDT", Action: "open_long", Leverage: 5,
		PositionSizeUSD: 500, StopLoss: 90000, TakeProfit: 110000,
		Confidence: 0, // field absent in the model's JSON
	}
	err := validateDecision(&missing, 1000, risk)
	if err == nil {
		t.Fatal("expected rejection when the confidence field is missing")
	}
	if !strings.Contains(err.Error(), "without a confidence score") {
		t.Fatalf("missing confidence should be reported as a schema violation, got: %v", err)
	}

	low := missing
	low.Confidence = risk.MinConfidence - 1
	err = validateDecision(&low, 1000, risk)
	if err == nil {
		t.Fatal("expected rejection when the confidence score is below the minimum")
	}
	if !strings.Contains(err.Error(), "below minimum") {
		t.Fatalf("low confidence should be reported as below minimum, got: %v", err)
	}
	if strings.Contains(err.Error(), "without a confidence score") {
		t.Fatalf("a scored decision must not be reported as missing the field, got: %v", err)
	}
}

// TestValidateDecisionUsesTemplatePositionValueRatio guards the ratio the
// template actually ships. testSizedRiskControl narrows it for readability in
// the leverage cases, so nothing else would catch a template that silently
// widened or shrank.
func TestValidateDecisionUsesTemplatePositionValueRatio(t *testing.T) {
	risk := testRiskControl(10, 10)
	limit := 1000 * risk.AltcoinMaxPositionValueRatio

	atLimit := Decision{
		Symbol: "SOLUSDT", Action: "open_long", Leverage: 5,
		PositionSizeUSD: limit, StopLoss: 50, TakeProfit: 200,
		Confidence: 90,
	}
	if err := validateDecision(&atLimit, 1000, risk); err != nil {
		t.Fatalf("position exactly at the template ratio should pass, got: %v", err)
	}

	overLimit := atLimit
	overLimit.PositionSizeUSD = limit * 1.5
	if err := validateDecision(&overLimit, 1000, risk); err == nil {
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

	strict := testRiskControl(10, 10)
	strict.MinRiskRewardRatio = 5.0
	if err := validateDecision(&candidate, 1000, strict); err == nil {
		t.Fatal("expected rejection when the configured risk/reward floor is higher than the setup")
	}

	relaxed := testRiskControl(10, 10)
	relaxed.MinRiskRewardRatio = 3.0
	if err := validateDecision(&candidate, 1000, relaxed); err != nil {
		t.Fatalf("setup meeting the configured floor should pass, got: %v", err)
	}
}
