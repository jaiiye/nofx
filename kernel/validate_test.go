package kernel

import (
	"strings"
	"testing"
)

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
			// Use default position value ratios for testing (10x for BTC/ETH, 1.5x for altcoins)
			err := validateDecision(&tt.decision, tt.accountEquity, tt.btcEthLeverage, tt.altcoinLeverage, 10.0, 1.5, nil, 3.0)

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


// contains checks if string contains substring (helper function)
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && stringContains(s, substr)))
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// The risk/reward gate used to derive the entry as SL + 20% of the SL→TP span,
// which made the ratio a tautological 4.0:1 — every pair passed. It now
// measures against the real market price, so a target closer to price than the
// stop is rejected (live case: xyz:SILVER entry 57.45, SL 56.40, TP 57.70).
func TestValidateDecisionRiskRewardUsesMarketPrice(t *testing.T) {
	priceFor := func(string) float64 { return 57.45 }

	bad := Decision{
		Symbol: "xyz:SILVER", Action: "open_long", Leverage: 5, PositionSizeUSD: 75,
		StopLoss: 56.40, TakeProfit: 57.70, // 0.24:1 at the real entry
	}
	err := validateDecision(&bad, 153, 5, 5, 0.6, 0.6, priceFor, 3.0)
	if err == nil {
		t.Fatal("target nearer than the stop must be rejected")
	}
	if !strings.Contains(err.Error(), "risk/reward ratio too low") {
		t.Fatalf("unexpected error: %v", err)
	}

	// The same SL/TP pair is fine when the entry really is near the stop.
	near := Decision{
		Symbol: "xyz:SILVER", Action: "open_long", Leverage: 5, PositionSizeUSD: 75,
		StopLoss: 56.40, TakeProfit: 60.60, // risk 1.05, reward 2.10 → 2.0:1? no: entry 57.45 → 2.0:1
	}
	if err := validateDecision(&near, 153, 5, 5, 0.6, 0.6, priceFor, 2.0); err != nil {
		t.Fatalf("2.0:1 must pass a 2.0 minimum, got: %v", err)
	}
	if err := validateDecision(&near, 153, 5, 5, 0.6, 0.6, priceFor, 3.0); err == nil {
		t.Fatal("2.0:1 must fail a 3.0 minimum")
	}

	// Configured threshold is respected (not the old hardcoded 3.0).
	if err := validateDecision(&near, 153, 5, 5, 0.6, 0.6, priceFor, 1.5); err != nil {
		t.Fatalf("2.0:1 must pass a 1.5 minimum, got: %v", err)
	}

	// No market price → check skipped rather than blocking trading.
	if err := validateDecision(&bad, 153, 5, 5, 0.6, 0.6, func(string) float64 { return 0 }, 3.0); err != nil {
		t.Fatalf("missing price must skip the check, got: %v", err)
	}
}

func TestNormalizeMinRewardRatio(t *testing.T) {
	if got := normalizeMinRewardRatio(0); got != 3.0 {
		t.Fatalf("0 → %.1f, want 3.0", got)
	}
	if got := normalizeMinRewardRatio(-2); got != 3.0 {
		t.Fatalf("negative → %.1f, want 3.0", got)
	}
	if got := normalizeMinRewardRatio(4.5); got != 4.5 {
		t.Fatalf("explicit → %.1f, want 4.5", got)
	}
}
