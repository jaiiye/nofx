package market

import (
	"testing"
	"time"
)

func TestDropUnclosedCandle(t *testing.T) {
	now := time.Now().UTC()

	// Two candles: one already closed (opens 10m ago, 5m timeframe -> closed
	// 5m ago) and one still forming (opens now -> closes 5m from now).
	klines := []Kline{
		{OpenTime: now.Add(-10 * time.Minute).UnixMilli()},
		{OpenTime: now.UnixMilli()},
	}

	got := dropUnclosedCandle(klines, "5m")
	if len(got) != 1 {
		t.Fatalf("got %d candles, want 1 (forming candle should be dropped)", len(got))
	}
	if got[0].OpenTime != klines[0].OpenTime {
		t.Fatalf("kept wrong candle: %d, want %d", got[0].OpenTime, klines[0].OpenTime)
	}
}

func TestDropUnclosedCandleAllClosed(t *testing.T) {
	now := time.Now().UTC()
	klines := []Kline{
		{OpenTime: now.Add(-20 * time.Minute).UnixMilli()},
		{OpenTime: now.Add(-15 * time.Minute).UnixMilli()},
	}

	got := dropUnclosedCandle(klines, "5m")
	if len(got) != 2 {
		t.Fatalf("got %d candles, want 2 (all closed)", len(got))
	}
}

func TestDropUnclosedCandleUnsupportedTimeframe(t *testing.T) {
	now := time.Now().UTC()
	klines := []Kline{{OpenTime: now.UnixMilli()}}

	got := dropUnclosedCandle(klines, "bogus")
	if len(got) != 1 {
		t.Fatalf("got %d candles, want 1 (unsupported timeframe leaves input untouched)", len(got))
	}

	if got := dropUnclosedCandle(nil, "5m"); got != nil {
		t.Fatalf("empty input should return nil")
	}
}
