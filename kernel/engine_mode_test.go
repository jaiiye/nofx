package kernel

import (
	"strings"
	"testing"

	"nofx/store"
)

func TestNormalizeModeVariant(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"aggressive", "aggressive"},
		{"Aggressive", "aggressive"},
		{"  CONSERVATIVE  ", "conservative"},
		{"scalping", "scalping"},
		{"Scalping", "scalping"},
		{"\tscalping\n", "scalping"},
		{"balanced", ""},
		{"", ""},
		{"   ", ""},
		{"turbo", ""},
		{"aggressive!", ""},
	}

	for _, tc := range tests {
		if got := NormalizeModeVariant(tc.in); got != tc.want {
			t.Errorf("NormalizeModeVariant(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestBuildSystemPromptAppliesModeVariant guards the regression where the
// trading mode never reached the prompt: the live loop passed a hardcoded
// value that writeModeVariant did not recognise, so no mode section was ever
// emitted.
func TestBuildSystemPromptAppliesModeVariant(t *testing.T) {
	cfg := store.GetDefaultStrategyConfig("en")
	engine := NewStrategyEngine(&cfg)

	applied := []struct {
		variant string
		marker  string
	}{
		{"aggressive", "Mode: Aggressive"},
		{"Aggressive", "Mode: Aggressive"},
		{"conservative", "Mode: Conservative"},
		{"scalping", "Mode: Scalping"},
	}
	for _, tc := range applied {
		if prompt := engine.BuildSystemPrompt(1000, tc.variant); !strings.Contains(prompt, tc.marker) {
			t.Errorf("variant %q: %q missing from system prompt", tc.variant, tc.marker)
		}
	}

	// Balanced and unrecognised values must not emit any mode section.
	for _, variant := range []string{"", "balanced", "BALANCED", "bogus"} {
		if prompt := engine.BuildSystemPrompt(1000, variant); strings.Contains(prompt, "## Mode:") {
			t.Errorf("variant %q should not emit a mode section", variant)
		}
	}
}
