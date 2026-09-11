package trader

import (
	"testing"

	"nofx/kernel"
)

func TestDrawdownCloseArmsOnPriceBasisOnly(t *testing.T) {
	cases := []struct {
		name        string
		pricePnLPct float64
		drawdownPct float64
		shouldClose bool
	}{
		// +0.5% price move (what +5% margin at 10x used to arm on) must NOT
		// arm the monitor, no matter how large the relative drawdown is.
		{"tiny price gain big drawdown", 0.5, 60.0, false},
		// Armed only from a real +5% price move, and still needs the 40% giveback.
		{"real gain small drawdown", 6.0, 20.0, false},
		{"real gain big drawdown", 6.0, 45.0, true},
		{"at threshold not armed", 5.0, 45.0, false},
		{"loss never triggers", -3.0, 80.0, false},
	}
	for _, c := range cases {
		if got := shouldDrawdownClose(c.pricePnLPct, c.drawdownPct); got != c.shouldClose {
			t.Fatalf("%s: shouldDrawdownClose(%.1f, %.1f) = %v, want %v",
				c.name, c.pricePnLPct, c.drawdownPct, got, c.shouldClose)
		}
	}
}

func TestPruneStalePeakPnLDropsClosedPositions(t *testing.T) {
	at := &AutoTrader{peakPnLCache: map[string]float64{
		"BTC_long":  50.0,
		"ETH_short": 30.0,
		"SOL_long":  10.0,
	}}

	at.pruneStalePeakPnL(map[string]bool{"BTC_long": true})

	if _, ok := at.peakPnLCache["BTC_long"]; !ok {
		t.Fatal("an open position's peak entry must be kept")
	}
	for _, key := range []string{"ETH_short", "SOL_long"} {
		if _, ok := at.peakPnLCache[key]; ok {
			t.Fatalf("a closed position's stale peak entry %s must be pruned", key)
		}
	}
}

func TestSortDecisionsByPriorityReducesBeforeOpens(t *testing.T) {
	decisions := []kernel.Decision{
		{Symbol: "BTC", Action: "open_long"},
		{Symbol: "ETH", Action: "reduce_long"},
		{Symbol: "SOL", Action: "hold"},
		{Symbol: "XYZ", Action: "close_short"},
	}
	sorted := sortDecisionsByPriority(decisions)

	// Close-type actions (close + reduce) must run before opens and holds,
	// regardless of their mutual order within the group.
	closeType := map[string]bool{"close_long": true, "close_short": true, "reduce_long": true, "reduce_short": true}
	for i := 0; i < 2; i++ {
		if !closeType[sorted[i].Action] {
			t.Fatalf("first two must be close-type actions, got %+v", sorted)
		}
	}
	if sorted[2].Action != "open_long" || sorted[3].Action != "hold" {
		t.Fatalf("opens and holds must come after close-type actions, got %+v", sorted)
	}
}
