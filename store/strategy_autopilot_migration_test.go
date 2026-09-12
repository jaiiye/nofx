package store

import "testing"

func autopilotConfig(maxPositions int, ratio float64) *StrategyConfig {
	cfg := GetDefaultStrategyConfig("en")
	cfg.CoinSource.SourceType = "vergex_signal"
	cfg.RiskControl.MaxPositions = maxPositions
	cfg.RiskControl.BTCETHMaxPositionValueRatio = ratio
	cfg.RiskControl.AltcoinMaxPositionValueRatio = ratio
	return &cfg
}

func TestMigrateLegacyAutopilotRiskDefaults(t *testing.T) {
	cases := []struct {
		name         string
		maxPositions int
		ratio        float64
		wantMigrate  bool
	}{
		{"legacy three-slot book", 3, 5.0, true},
		{"legacy four-slot book", 4, 5.0, true},
		{"legacy four-slot quarter-ratio book", 4, legacyAutopilotPositionRatio, true},
		{"already current", AutopilotDefaultMaxPositions, AutopilotMaxPositionValueRatio, false},
		{"user-chosen two-slot book is left alone", 2, 5.0, false},
		{"user-chosen one-slot book is left alone", 1, 5.0, false},
		{"user-chosen custom ratio is left alone", 2, 3.0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := autopilotConfig(tc.maxPositions, tc.ratio)
			got := MigrateLegacyAutopilotRiskDefaults(cfg)
			if got != tc.wantMigrate {
				t.Fatalf("migrated=%v, want %v (maxPos=%d ratio=%.2f)", got, tc.wantMigrate, tc.maxPositions, tc.ratio)
			}
			if tc.wantMigrate {
				if cfg.RiskControl.MaxPositions != AutopilotDefaultMaxPositions {
					t.Fatalf("max positions = %d, want %d", cfg.RiskControl.MaxPositions, AutopilotDefaultMaxPositions)
				}
				if cfg.RiskControl.BTCETHMaxPositionValueRatio != AutopilotMaxPositionValueRatio ||
					cfg.RiskControl.AltcoinMaxPositionValueRatio != AutopilotMaxPositionValueRatio {
					t.Fatalf("position ratios = %.2f/%.2f, want %.2f",
						cfg.RiskControl.BTCETHMaxPositionValueRatio, cfg.RiskControl.AltcoinMaxPositionValueRatio, AutopilotMaxPositionValueRatio)
				}
				// Migration must be idempotent: a second pass finds nothing to do.
				if MigrateLegacyAutopilotRiskDefaults(cfg) {
					t.Fatal("migration must not re-apply to an already current config")
				}
			}
		})
	}
}

func TestMigrateLegacyAutopilotRiskDefaultsIgnoresNonAutopilot(t *testing.T) {
	cfg := autopilotConfig(2, 5.0)
	cfg.CoinSource.SourceType = "static"
	if MigrateLegacyAutopilotRiskDefaults(cfg) {
		t.Fatal("non-Autopilot strategies must not be migrated")
	}
	if cfg.RiskControl.MaxPositions != 2 {
		t.Fatalf("non-Autopilot max positions changed to %d", cfg.RiskControl.MaxPositions)
	}
	if MigrateLegacyAutopilotRiskDefaults(nil) {
		t.Fatal("nil config must not panic or report a migration")
	}
}

func TestClampLimitsConstrainsAutopilotSlots(t *testing.T) {
	// Every strategy shares the same bound, so a slot count above it is pulled
	// down regardless of source type.
	cfg := autopilotConfig(MaxPositions+3, 5.0)
	cfg.ClampLimits()
	if cfg.RiskControl.MaxPositions != MaxPositions {
		t.Fatalf("Autopilot slots = %d, want %d", cfg.RiskControl.MaxPositions, MaxPositions)
	}
	// ...and a user choice at or below the bound is respected.
	cfg = autopilotConfig(MaxPositions, 5.0)
	cfg.ClampLimits()
	if cfg.RiskControl.MaxPositions != MaxPositions {
		t.Fatalf("a user-chosen %d-slot Autopilot book must survive clamping, got %d", MaxPositions, cfg.RiskControl.MaxPositions)
	}

	// ...while a non-Autopilot strategy keeps its higher slot count (up to MaxPositions).
	cfg = autopilotConfig(MaxPositions, 5.0)
	cfg.CoinSource.SourceType = "static"
	cfg.ClampLimits()
	if cfg.RiskControl.MaxPositions != MaxPositions {
		t.Fatalf("non-Autopilot slots = %d, want %d", cfg.RiskControl.MaxPositions, MaxPositions)
	}
}
