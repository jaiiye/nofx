package trader

import (
	"strings"
	"testing"
	"time"

	"nofx/provider/hyperliquid"
	"nofx/store"
)

// The gate must block only when it actually knows the venue is cold: with a live
// feed, silence is evidence; with no feed and no book there is no evidence, and
// trading must not stop because a data source is missing.
func TestColdStockEntry(t *testing.T) {
	cases := []struct {
		name     string
		isStock  bool
		activity stockActivity
		quiet    int
		blocked  bool
		wantPart string
	}{
		{
			name:     "non-stock is never gated",
			isStock:  false,
			activity: stockActivity{feedActive: true, lastPrintAge: time.Hour},
			quiet:    30,
			blocked:  false,
		},
		{
			name:     "live feed with recent prints allows the entry",
			isStock:  true,
			activity: stockActivity{feedActive: true, lastPrintAge: 5 * time.Minute},
			quiet:    30,
			blocked:  false,
		},
		{
			name:     "live feed with no prints at all blocks",
			isStock:  true,
			activity: stockActivity{feedActive: true},
			quiet:    30,
			blocked:  true,
			wantPart: "no trades seen",
		},
		{
			name:     "live feed silent past the window blocks",
			isStock:  true,
			activity: stockActivity{feedActive: true, lastPrintAge: 45 * time.Minute},
			quiet:    30,
			blocked:  true,
			wantPart: "no trades for",
		},
		{
			name:     "silence inside the window allows",
			isStock:  true,
			activity: stockActivity{feedActive: true, lastPrintAge: 29 * time.Minute},
			quiet:    30,
			blocked:  false,
		},
		{
			name:     "unset window falls back to the default",
			isStock:  true,
			activity: stockActivity{feedActive: true, lastPrintAge: 45 * time.Minute},
			quiet:    0,
			blocked:  true,
			wantPart: "no trades for",
		},
		{
			name:     "no feed and no book fails open",
			isStock:  true,
			activity: stockActivity{},
			quiet:    30,
			blocked:  false,
		},
		{
			name:     "no feed but a thin book blocks",
			isStock:  true,
			activity: stockActivity{hasBook: true, liquidityScore: 10, spreadBps: 12},
			quiet:    30,
			blocked:  true,
			wantPart: "thin",
		},
		{
			name:     "no feed but a wide spread blocks",
			isStock:  true,
			activity: stockActivity{hasBook: true, liquidityScore: 60, spreadBps: 80},
			quiet:    30,
			blocked:  true,
			wantPart: "spread",
		},
		{
			name:     "no feed with a healthy book allows",
			isStock:  true,
			activity: stockActivity{hasBook: true, liquidityScore: 80, spreadBps: 5},
			quiet:    30,
			blocked:  false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			blocked, reason := coldStockEntry(c.isStock, c.activity, c.quiet)
			if blocked != c.blocked {
				t.Fatalf("blocked = %v (reason %q), want %v", blocked, reason, c.blocked)
			}
			if c.blocked && c.wantPart != "" && !strings.Contains(reason, c.wantPart) {
				t.Fatalf("reason %q does not explain the block (%q)", reason, c.wantPart)
			}
			if !c.blocked && reason != "" {
				t.Fatalf("allowed entry must not carry a reason, got %q", reason)
			}
		})
	}
}

// Only stock perps are gated. Commodities, indices, FX and pre-IPO assets have
// their own sessions, and an unknown/bare symbol is left alone (gating it would
// block crypto symbols that merely look like a ticker).
func TestIsStockAsset(t *testing.T) {
	cases := map[string]bool{
		"xyz:TSLA":    true,
		"xyz:NVDA":    true,
		"xyz:AAPL":    true,
		"AAPL-USDC":   true,
		"xyz:SILVER":  false, // commodity
		"xyz:GOLD":    false, // commodity
		"xyz:SPX":     false, // index
		"xyz:EUR":     false, // fx
		"xyz:OPENAI":  false, // pre-IPO
		"BTCUSDT":     false,
		"ETHUSDC":     false,
		"SOL":         false,
		"TSLA":        false, // bare base: cannot be classified, so not gated
		"GOLD-USDC":   false, // commodity on the same dex
		"xyz:NEWTICK": true,  // unknown bases classify as stock by design
	}
	for symbol, want := range cases {
		if got := hyperliquid.IsStockAsset(symbol); got != want {
			t.Errorf("IsStockAsset(%q) = %v, want %v", symbol, got, want)
		}
	}
}

// The execution path reads both values through one helper, so a zero stored
// window cannot mean "gate everything instantly".
func TestStockEntryGateSettingsNormalize(t *testing.T) {
	var rc store.RiskControlConfig
	if enabled, quiet := rc.StockEntryGate(); !enabled || quiet != store.DefaultStockQuietMinutes {
		t.Fatalf("defaults = %v/%d, want true/%d", enabled, quiet, store.DefaultStockQuietMinutes)
	}

	off := false
	rc = store.RiskControlConfig{BlockColdStockEntries: &off, StockQuietMinutes: 1}
	if enabled, quiet := rc.StockEntryGate(); enabled || quiet != store.MinStockQuietMinutes {
		t.Fatalf("disabled gate = %v/%d, want false/%d", enabled, quiet, store.MinStockQuietMinutes)
	}

	on := true
	rc = store.RiskControlConfig{BlockColdStockEntries: &on, StockQuietMinutes: 999}
	if enabled, quiet := rc.StockEntryGate(); !enabled || quiet != store.MaxStockQuietMinutes {
		t.Fatalf("clamped window = %v/%d, want true/%d", enabled, quiet, store.MaxStockQuietMinutes)
	}
}
