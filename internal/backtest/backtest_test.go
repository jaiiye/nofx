package backtest

import (
	"math"
	"testing"
	"time"

	"nofx/market"
	"nofx/store"
	"nofx/trader/connector"
)

type bar struct{ o, h, l, c float64 }

func hourlyKlines(base time.Time, bars []bar) []market.Kline {
	out := make([]market.Kline, len(bars))
	for i, b := range bars {
		t := base.Add(time.Duration(i) * time.Hour).UnixMilli()
		out[i] = market.Kline{OpenTime: t, Open: b.o, High: b.h, Low: b.l, Close: b.c, CloseTime: t + 3599999}
	}
	return out
}

func staticSource(kl []market.Kline) KlineSource {
	return func(symbol, tf string, start, end time.Time) ([]market.Kline, error) { return kl, nil }
}

func record(at time.Time, actions ...store.DecisionAction) *store.DecisionRecord {
	return &store.DecisionRecord{Timestamp: at.UTC(), Decisions: actions}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestEvaluateTakeProfit(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	kl := hourlyKlines(base, []bar{
		{o: 99, h: 100, l: 98, c: 99},   // idx0 (before decision)
		{o: 100, h: 101, l: 99, c: 100}, // idx1 entry
		{o: 100, h: 112, l: 98, c: 110}, // idx2 hits TP=110
	})
	recs := []*store.DecisionRecord{record(base, store.DecisionAction{
		Action: "open_long", Symbol: "BTCUSDT", Quantity: 2, StopLoss: 95, TakeProfit: 110, Confidence: 85,
	})}

	cfg := Config{Timeframe: "1h", Fees: connector.StaticFeeModel{Taker: 0.001, Asset: "USDT"}, MaxHoldCandles: 10}
	res, err := Evaluate(recs, cfg, staticSource(kl))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(res.Trades) != 1 {
		t.Fatalf("trades = %d, want 1", len(res.Trades))
	}
	tr := res.Trades[0]
	if tr.ExitReason != "take_profit" || !near(tr.EntryPrice, 100) || !near(tr.ExitPrice, 110) {
		t.Fatalf("unexpected trade: %+v", tr)
	}
	if !near(tr.GrossPnL, 20) { // (110-100)*2
		t.Fatalf("gross = %v, want 20", tr.GrossPnL)
	}
	if !near(tr.Fee, 0.42) { // (200+220)*0.001
		t.Fatalf("fee = %v, want 0.42", tr.Fee)
	}
	if !near(tr.NetPnL, 19.58) {
		t.Fatalf("net = %v, want 19.58", tr.NetPnL)
	}
	if res.Metrics.Wins != 1 || !near(res.Metrics.WinRate, 100) {
		t.Fatalf("metrics = %+v", res.Metrics)
	}
}

func TestEvaluateStopLossFirstWhenCandleSpansBoth(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	kl := hourlyKlines(base, []bar{
		{o: 100, h: 100, l: 100, c: 100},
		{o: 100, h: 112, l: 90, c: 100}, // spans both TP=110 and SL=95 -> SL wins
	})
	recs := []*store.DecisionRecord{record(base, store.DecisionAction{
		Action: "open_long", Symbol: "ETHUSDT", Quantity: 1, StopLoss: 95, TakeProfit: 110,
	})}
	res, _ := Evaluate(recs, Config{Timeframe: "1h", Fees: connector.StaticFeeModel{}}, staticSource(kl))
	if res.Trades[0].ExitReason != "stop_loss" {
		t.Fatalf("exit reason = %q, want stop_loss (conservative intrabar)", res.Trades[0].ExitReason)
	}
}

func TestEvaluateShortAndTimeout(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	kl := hourlyKlines(base, []bar{
		{o: 100, h: 100, l: 100, c: 100},
		{o: 100, h: 101, l: 99, c: 100},
		{o: 100, h: 101, l: 99, c: 98},
	})
	recs := []*store.DecisionRecord{record(base, store.DecisionAction{
		Action: "open_short", Symbol: "BTCUSDT", Quantity: 3, // no SL/TP -> timeout exit
	})}
	res, _ := Evaluate(recs, Config{Timeframe: "1h", MaxHoldCandles: 2, Fees: connector.StaticFeeModel{}}, staticSource(kl))
	tr := res.Trades[0]
	if tr.ExitReason != "timeout" {
		t.Fatalf("exit reason = %q, want timeout", tr.ExitReason)
	}
	// entry at idx1 open=100, exit at idx2 close=98, short -> +2*3 = +6 gross.
	if !near(tr.GrossPnL, 6) {
		t.Fatalf("short gross = %v, want 6", tr.GrossPnL)
	}
}

func TestEvaluateSkipsZeroQuantity(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	kl := hourlyKlines(base, []bar{{o: 100, h: 100, l: 100, c: 100}, {o: 100, h: 100, l: 100, c: 100}})
	recs := []*store.DecisionRecord{record(base, store.DecisionAction{Action: "open_long", Symbol: "BTCUSDT", Quantity: 0})}
	res, _ := Evaluate(recs, Config{Timeframe: "1h", Fees: connector.StaticFeeModel{}}, staticSource(kl))
	if res.Opens != 1 || res.Skipped != 1 || len(res.Trades) != 0 {
		t.Fatalf("opens=%d skipped=%d trades=%d, want 1/1/0", res.Opens, res.Skipped, len(res.Trades))
	}
}

func TestEvaluateAttributionAndSignificance(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	kl := hourlyKlines(base, []bar{
		{o: 100, h: 100, l: 100, c: 100},
		{o: 100, h: 120, l: 100, c: 115}, // TP=110 hit
		{o: 100, h: 100, l: 80, c: 85},   // SL=90 hit
	})
	recs := []*store.DecisionRecord{record(base,
		store.DecisionAction{Action: "open_long", Symbol: "BTCUSDT", Quantity: 1, StopLoss: 90, TakeProfit: 110, Confidence: 95},
		store.DecisionAction{Action: "open_long", Symbol: "ETHUSDT", Quantity: 1, StopLoss: 90, TakeProfit: 110, Confidence: 65},
	)}
	res, err := Evaluate(recs, Config{Timeframe: "1h", Fees: connector.StaticFeeModel{}, Samples: 200, Seed: 1}, staticSource(kl))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(res.BySymbol) != 2 {
		t.Fatalf("bySymbol = %d, want 2", len(res.BySymbol))
	}
	if len(res.ByReason) != 1 || res.ByReason[0].Reason != "take_profit" {
		t.Fatalf("byReason = %+v", res.ByReason)
	}
	if len(res.ByConfidence) != 2 { // buckets 90-100 and 60-69
		t.Fatalf("byConfidence = %+v, want 2 buckets", res.ByConfidence)
	}
	if res.Significance.Samples != 200 {
		t.Fatalf("significance samples = %d, want 200", res.Significance.Samples)
	}
}
