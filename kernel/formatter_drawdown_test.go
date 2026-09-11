package kernel

import "testing"

func TestPositionPeakPriceMovePctIsLeverageAware(t *testing.T) {
	pos := &PositionInfo{Side: "long", Leverage: 10, UnrealizedPnLPct: 15, PeakPnLPct: 30}
	if got := positionPeakPriceMovePct(pos); got != 3.0 {
		t.Fatalf("30%% margin peak at 10x is a 3%% price move, got %.4f", got)
	}
	if got := positionPriceMovePct(pos); got != 1.5 {
		t.Fatalf("15%% margin PnL at 10x is a 1.5%% price move, got %.4f", got)
	}
}

func TestPositionTakeProfitHintDue(t *testing.T) {
	cases := []struct {
		name                    string
		peak, current, drawdown float64
		want                    bool
	}{
		{"peak below arming floor", 2.5, 0.5, -2.0, false},
		{"no profit left", 10, -1, -11, false},
		{"retrace inside the band", 10, 8, -2.0, false},
		{"retrace beyond the ceiling", 10, 5, -5.0, true},
		{"exactly at the ceiling", 10, 6, -4.0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := positionTakeProfitHintDue(tc.peak, tc.current, tc.drawdown); got != tc.want {
				t.Fatalf("peak=%.2f current=%.2f drawdown=%.2f: want %v got %v",
					tc.peak, tc.current, tc.drawdown, tc.want, got)
			}
		})
	}
}

func TestFormatCurrentPositionsUsesPriceBasis(t *testing.T) {
	// 10x leverage, peak +30% margin (+3% price), now +15% margin (+1.5% price):
	// a 50% price giveback must trip the hint. Under the old margin basis the
	// same numbers read as a 50% retrace too, but a smaller peak (say +8% margin
	// at 10x, i.e. +0.8% price) used to fire on noise — it must not any more.
	noisy := Context{Positions: []PositionInfo{{
		Symbol: "BTCUSDT", Side: "long", Leverage: 10,
		UnrealizedPnLPct: 5, PeakPnLPct: 8,
	}}}
	if out := formatCurrentPositionsEN(&noisy); containsHint(out) {
		t.Fatalf("a sub-1%% price move must not raise a take-profit hint, got:\n%s", out)
	}

	faded := Context{Positions: []PositionInfo{{
		Symbol: "BTCUSDT", Side: "long", Leverage: 10,
		UnrealizedPnLPct: 15, PeakPnLPct: 30,
	}}}
	if out := formatCurrentPositionsEN(&faded); !containsHint(out) {
		t.Fatalf("a 50%% price giveback must raise a take-profit hint, got:\n%s", out)
	}
}

func containsHint(out string) bool {
	for i := 0; i+len("Take Profit") <= len(out); i++ {
		if out[i:i+len("Take Profit")] == "Take Profit" {
			return true
		}
	}
	return false
}
