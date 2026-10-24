package market

import (
	"testing"
	"time"
)

func addPrint(window *LiquidationWindow, at time.Time, side string, notional float64) {
	window.Add(LiquidationPrint{
		TimestampMs: at.UnixMilli(),
		Venue:       "binance",
		Symbol:      "BTCUSDT",
		VenueSymbol: "BTCUSDT",
		Side:        side,
		Price:       1,
		Qty:         notional,
		NotionalUSD: notional,
	})
}

func TestLiquidationWindowSums(t *testing.T) {
	now := time.Now()
	window := NewLiquidationWindow()

	addPrint(window, now.Add(-20*time.Minute), "long", 10)
	addPrint(window, now.Add(-10*time.Minute), "long", 100)
	addPrint(window, now.Add(-5*time.Minute), "short", 50)

	got := window.Reading("BTCUSDT", now)
	if got == nil {
		t.Fatal("expected a reading")
	}
	if got.TotalUSD != 160 || got.LongUSD != 110 || got.ShortUSD != 50 {
		t.Fatalf("sums = %+v", got)
	}
	if got.PrintCount != 3 {
		t.Fatalf("prints = %d, want 3", got.PrintCount)
	}
	if got.LargestUSD != 100 {
		t.Fatalf("largest = %v, want 100", got.LargestUSD)
	}
	if got.ShortWindowUSD != 150 {
		t.Fatalf("15m = %v, want 150 (the 20m print is outside it)", got.ShortWindowUSD)
	}
	if diff := got.LongSharePct - 68.75; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("long share = %v, want 68.75", got.LongSharePct)
	}
}

func TestLiquidationWindowOmitsEmptyWindow(t *testing.T) {
	now := time.Now()
	window := NewLiquidationWindow()
	addPrint(window, now.Add(-2*time.Hour), "long", 100)

	if got := window.Reading("BTCUSDT", now); got != nil {
		t.Fatalf("a quiet headline window must be omitted, got %+v", got)
	}
}

func TestLiquidationWindowBaselineAndCascade(t *testing.T) {
	now := time.Now()
	window := NewLiquidationWindow()

	// Twelve complete hourly windows of $100, then a 30x one-sided burst.
	for hour := 1; hour <= 12; hour++ {
		addPrint(window, now.Add(-time.Duration(hour)*time.Hour).Add(-time.Minute), "long", 100)
	}
	for i := 0; i < 30; i++ {
		addPrint(window, now.Add(-time.Duration(i)*time.Second), "long", 1000)
	}

	got := window.Reading("BTCUSDT", now)
	if got == nil {
		t.Fatal("expected a reading")
	}
	if !got.BaselineOK {
		t.Fatalf("expected a baseline from 12 complete windows: %+v", got)
	}
	if got.BaselineUSD != 100 {
		t.Fatalf("baseline = %v, want 100", got.BaselineUSD)
	}
	if got.ZScore < liquidationCascadeZ {
		t.Fatalf("z = %v, want >= %v", got.ZScore, liquidationCascadeZ)
	}
	if !got.Cascade || got.CascadeSide != "long" {
		t.Fatalf("cascade = %v side = %q, want a long cascade", got.Cascade, got.CascadeSide)
	}
}

func TestLiquidationWindowMixedSidesIsNotACascade(t *testing.T) {
	now := time.Now()
	window := NewLiquidationWindow()

	for hour := 1; hour <= 12; hour++ {
		addPrint(window, now.Add(-time.Duration(hour)*time.Hour).Add(-time.Minute), "long", 100)
	}
	for i := 0; i < 30; i++ {
		side := "long"
		if i%2 == 0 {
			side = "short"
		}
		addPrint(window, now.Add(-time.Duration(i)*time.Second), side, 1000)
	}

	got := window.Reading("BTCUSDT", now)
	if got == nil {
		t.Fatal("expected a reading")
	}
	if got.Cascade {
		t.Fatalf("a two-sided burst is liquidity, not a cascade: %+v", got)
	}
}

func TestLiquidationWindowNeedsEnoughBaseline(t *testing.T) {
	now := time.Now()
	window := NewLiquidationWindow()

	for hour := 1; hour <= 3; hour++ {
		addPrint(window, now.Add(-time.Duration(hour)*time.Hour).Add(-time.Minute), "long", 100)
	}
	addPrint(window, now.Add(-time.Minute), "long", 5000)

	got := window.Reading("BTCUSDT", now)
	if got == nil {
		t.Fatal("expected a reading")
	}
	if got.BaselineOK || got.ZScore != 0 {
		t.Fatalf("three windows are not a baseline: %+v", got)
	}
	if got.Cascade {
		t.Fatalf("no baseline must never produce a cascade: %+v", got)
	}
}

func TestLiquidationWindowPrunesExpiredPrints(t *testing.T) {
	now := time.Now()
	window := NewLiquidationWindow()
	addPrint(window, now.Add(-13*time.Hour), "long", 100)
	if window.Len() != 0 {
		t.Fatalf("prints outside the retention window must be dropped, got %d", window.Len())
	}
}
