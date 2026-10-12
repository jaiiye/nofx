package api

import "testing"

func TestResolvePromptVariant(t *testing.T) {
	tests := []struct {
		name       string
		explicit   string
		configured string
		want       string
	}{
		{"explicit wins over configured", "aggressive", "scalping", "aggressive"},
		{"explicit is normalized", " AGGRESSIVE ", "scalping", "aggressive"},
		{"explicit balanced is honoured", "balanced", "aggressive", "balanced"},
		{"invalid explicit falls back to baseline", "bogus", "aggressive", "balanced"},
		{"no explicit uses configured", "", "scalping", "scalping"},
		{"configured is normalized", "", " Conservative ", "conservative"},
		{"nothing configured uses baseline", "", "", "balanced"},
		{"invalid configured uses baseline", "", "turbo", "balanced"},
	}

	for _, tc := range tests {
		if got := resolvePromptVariant(tc.explicit, tc.configured); got != tc.want {
			t.Errorf("%s: resolvePromptVariant(%q, %q) = %q, want %q",
				tc.name, tc.explicit, tc.configured, got, tc.want)
		}
	}
}
