package kernel

import (
	"strings"
	"testing"
)

// The chain of thought must survive both shapes the model produces: a populated
// <reasoning> block, and a blank one that must not swallow the fallbacks.
func TestExtractCoTTrace(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "reasoning tag",
			in:   "<reasoning>\nBTC is below EMA50.\n</reasoning>\n<decision>[{\"symbol\":\"BTCUSDT\"}]</decision>",
			want: "BTC is below EMA50.",
		},
		{
			name: "blank reasoning tag falls through to the text before <decision>",
			in:   "<reasoning>\n\n</reasoning>\nI would rather wait for confirmation.\n<decision>[{\"symbol\":\"BTCUSDT\"}]</decision>",
			want: "I would rather wait for confirmation.",
		},
		{
			name: "text before the JSON array",
			in:   "No structure yet, watching.\n[{\"symbol\":\"BTCUSDT\"}]",
			want: "No structure yet, watching.",
		},
		{
			name: "bare JSON keeps the whole response",
			in:   `[{"symbol":"BTCUSDT","action":"wait"}]`,
			want: `[{"symbol":"BTCUSDT","action":"wait"}]`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractCoTTrace(c.in)
			if got == "" {
				t.Fatalf("extracted an empty chain of thought from %q", c.in)
			}
			if !strings.Contains(got, strings.TrimSpace(c.want)) {
				t.Fatalf("extracted %q, want it to contain %q", got, c.want)
			}
		})
	}
}
