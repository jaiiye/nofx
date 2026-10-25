package sweep

import (
	"testing"
	"time"

	"nofx/internal/backtest"
	"nofx/market"
	"nofx/store"
	"nofx/trader/connector"
)

func TestRunSweepsGridAndCachesKlines(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// 1h candles: entry at idx1 open=100; idx2 is a big move up then down.
	kl := make([]market.Kline, 4)
	prices := []struct{ o, h, l, c float64 }{
		{100, 100, 100, 100},
		{100, 100, 100, 100}, // entry bar
		{100, 115, 95, 100},  // spans a wide range
		{100, 100, 100, 100},
	}
	for i, p := range prices {
		ts := base.Add(time.Duration(i) * time.Hour).UnixMilli()
		kl[i] = market.Kline{OpenTime: ts, Open: p.o, High: p.h, Low: p.l, Close: p.c}
	}

	fetches := 0
	src := func(symbol, tf string, start, end time.Time) ([]market.Kline, error) {
		fetches++
		return kl, nil
	}
	records := []*store.DecisionRecord{{Timestamp: base, Decisions: []store.DecisionAction{
		{Action: "open_long", Symbol: "BTCUSDT", Quantity: 1, Confidence: 70},
	}}}

	res, err := Run(records, backtest.Config{
		Timeframe: "1h", MaxHoldCandles: 3, Samples: 50, Seed: 1,
		Fees: connector.StaticFeeModel{},
	}, src, Grid{StopPcts: []float64{2, 5}, TakePcts: []float64{3, 10}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Cells) != 4 {
		t.Fatalf("cells = %d, want 4", len(res.Cells))
	}
	// The wide ±move (high 115, low 95) lets both a 2% stop (98) and a 3%/10%
	// target be reached; wider stops should not lose by being stopped out on
	// that same bar, so the 5% stop cell should be at least as good.
	if res.Best.Metrics.NetPnL < res.Cells[0].Metrics.NetPnL {
		t.Fatalf("best should be >= first cell")
	}
	// Klines fetched once, reused across the 4 cells.
	if fetches != 1 {
		t.Fatalf("kline fetches = %d, want 1 (cached)", fetches)
	}
}

func TestRunRejectsEmptyGrid(t *testing.T) {
	if _, err := Run(nil, backtest.Config{}, nil, Grid{}); err == nil {
		t.Fatal("expected error for empty grid")
	}
}

func givebackKlines(base time.Time) []market.Kline {
	// Entry 100, peak 105, then a retrace through 103.5 (the 2%/30% trigger).
	bars := []struct{ o, h, l, c float64 }{
		{100, 100, 100, 100},
		{100, 105, 100, 104},
		{104, 104, 96, 97},
		{97, 98, 96, 97},
	}
	out := make([]market.Kline, len(bars))
	for i, b := range bars {
		ts := base.Add(time.Duration(i) * time.Hour).UnixMilli()
		out[i] = market.Kline{OpenTime: ts, Open: b.o, High: b.h, Low: b.l, Close: b.c, CloseTime: ts + 3599999}
	}
	return out
}

func givebackRecord(base time.Time) *store.DecisionRecord {
	return &store.DecisionRecord{
		Timestamp:    base,
		DecisionJSON: `[{"symbol":"BTCUSDT","action":"open_long","position_size_usd":100,"stop_loss":90,"take_profit":110,"confidence":80}]`,
	}
}

func givebackConfig(base time.Time) backtest.Config {
	return backtest.Config{
		Timeframe:  "1h",
		From:       base,
		To:         base.Add(24 * time.Hour),
		Fees:       connector.StaticFeeModel{},
		SizingMode: backtest.SizingDecision,
	}
}

func TestRunGivebackGrid(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	kl := givebackKlines(base)
	src := func(symbol, tf string, start, end time.Time) ([]market.Kline, error) { return kl, nil }

	res, err := Run([]*store.DecisionRecord{givebackRecord(base)}, givebackConfig(base), src, Grid{
		Mode:         backtest.ExitGiveback,
		StopPcts:     []float64{2, 5},
		ArmPcts:      []float64{1},
		GivebackPcts: []float64{30},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Mode != backtest.ExitGiveback {
		t.Fatalf("mode = %q", res.Mode)
	}
	if len(res.Cells) != 2 {
		t.Fatalf("cells = %d, want 2 (one per stop)", len(res.Cells))
	}
	for _, c := range res.Cells {
		if c.ArmPct != 1 || c.GivebackPct != 30 || c.TakePct != 0 {
			t.Fatalf("cell carries the wrong parameters: %+v", c)
		}
	}
}

func TestRunRejectsIncompleteGrids(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recs := []*store.DecisionRecord{givebackRecord(base)}
	src := func(symbol, tf string, start, end time.Time) ([]market.Kline, error) {
		return givebackKlines(base), nil
	}

	if _, err := Run(recs, givebackConfig(base), src, Grid{Mode: backtest.ExitGiveback, StopPcts: []float64{2}, ArmPcts: []float64{1}}); err == nil {
		t.Fatal("giveback mode without a giveback grid must fail")
	}
	if _, err := Run(recs, givebackConfig(base), src, Grid{StopPcts: []float64{2}}); err == nil {
		t.Fatal("bracket mode without a take grid must fail")
	}
	if _, err := Run(recs, givebackConfig(base), src, Grid{Mode: "trailing", StopPcts: []float64{2}}); err == nil {
		t.Fatal("unknown mode must fail")
	}
}
