package backtest

import (
	"testing"
	"time"

	"nofx/store"
	"nofx/trader/connector"
)

func decisionRecord(at time.Time, decisionJSON string, fills ...store.DecisionAction) *store.DecisionRecord {
	return &store.DecisionRecord{Timestamp: at.UTC(), DecisionJSON: decisionJSON, Decisions: fills}
}

// A signal the risk gate blocked still expresses a view; decision sizing
// replays it from position_size_usd, while recorded sizing (correctly) skips it.
func TestDecisionSizingIncludesBlockedSignals(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	kl := hourlyKlines(base, []bar{
		{o: 100, h: 100, l: 100, c: 100},
		{o: 100, h: 111, l: 99, c: 110}, // TP=110
	})
	rec := decisionRecord(base,
		`[{"symbol":"BTCUSDT","action":"open_long","position_size_usd":300,"stop_loss":95,"take_profit":110,"confidence":80}]`,
		store.DecisionAction{Action: "open_long", Symbol: "BTCUSDT", Quantity: 0, StopLoss: 95, TakeProfit: 110, Confidence: 80},
	)
	src := staticSource(kl)

	res, err := Evaluate([]*store.DecisionRecord{rec}, Config{
		Timeframe: "1h", Fees: connector.StaticFeeModel{}, SizingMode: SizingDecision,
	}, src)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(res.Trades) != 1 {
		t.Fatalf("decision sizing must replay the blocked signal, trades=%d skipped=%d", len(res.Trades), res.Skipped)
	}
	tr := res.Trades[0]
	if !near(tr.Quantity, 3) { // 300 USD / entry 100
		t.Fatalf("quantity = %v, want 3", tr.Quantity)
	}
	if !near(tr.GrossPnL, 30) { // (110-100)*3
		t.Fatalf("gross = %v, want 30", tr.GrossPnL)
	}

	recorded, err := Evaluate([]*store.DecisionRecord{rec}, Config{
		Timeframe: "1h", Fees: connector.StaticFeeModel{}, SizingMode: SizingRecorded,
	}, src)
	if err != nil {
		t.Fatalf("Evaluate recorded: %v", err)
	}
	if recorded.Opens != 1 || recorded.Skipped != 1 || len(recorded.Trades) != 0 {
		t.Fatalf("recorded sizing must skip quantity 0: opens=%d skipped=%d trades=%d", recorded.Opens, recorded.Skipped, len(recorded.Trades))
	}
}

// Risk sizing ignores the AI's notional and divides a fixed equity risk budget
// by the stop distance.
func TestRiskSizingUsesEquityAndStop(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	kl := hourlyKlines(base, []bar{
		{o: 100, h: 100, l: 100, c: 100},
		{o: 100, h: 105, l: 99, c: 104}, // TP=104
	})
	rec := decisionRecord(base,
		`[{"symbol":"BTCUSDT","action":"open_long","position_size_usd":9999,"stop_loss":98,"take_profit":104,"confidence":80}]`,
		// Risk sizing ignores the fill quantity, but the execution entry is what
		// marks the signal as having reached the executor.
		store.DecisionAction{Action: "open_long", Symbol: "BTCUSDT", Quantity: 0, StopLoss: 98, TakeProfit: 104, Confidence: 80},
	)
	res, err := Evaluate([]*store.DecisionRecord{rec}, Config{
		Timeframe:    "1h",
		Fees:         connector.StaticFeeModel{},
		SizingMode:   SizingRisk,
		RiskPct:      1, // 1% of 1000 = 10 USD risk, stop distance 2 → 5 units
		EquitySource: func(time.Time) float64 { return 1000 },
	}, staticSource(kl))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(res.Trades) != 1 {
		t.Fatalf("trades = %d, want 1", len(res.Trades))
	}
	tr := res.Trades[0]
	if !near(tr.Quantity, 5) || !near(tr.Notional, 500) {
		t.Fatalf("risk sizing = qty %v notional %v, want 5 / 500", tr.Quantity, tr.Notional)
	}
	if !near(tr.GrossPnL, 20) { // (104-100)*5
		t.Fatalf("gross = %v, want 20", tr.GrossPnL)
	}

	// Without equity the mode cannot size anything, and that is an error rather
	// than a silent zero-trade replay.
	if _, err := Evaluate([]*store.DecisionRecord{rec}, Config{
		Timeframe: "1h", Fees: connector.StaticFeeModel{}, SizingMode: SizingRisk,
	}, staticSource(kl)); err == nil {
		t.Fatal("risk sizing without equity must fail loudly")
	}
}

// In risk mode the sweep's stop override must drive the size as well: otherwise
// every stop cell would be sized from the recorded stop while exiting at the
// overridden one, and the grid would compare sizes that do not belong to it.
func TestRiskSizingUsesOverriddenStop(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	kl := hourlyKlines(base, []bar{
		{o: 100, h: 100, l: 100, c: 100},
		{o: 100, h: 106, l: 99, c: 105},
	})
	rec := decisionRecord(base,
		`[{"symbol":"BTCUSDT","action":"open_long","position_size_usd":999,"stop_loss":90,"take_profit":105,"confidence":80}]`,
		store.DecisionAction{Action: "open_long", Symbol: "BTCUSDT", Quantity: 0, StopLoss: 90, TakeProfit: 105, Confidence: 80},
	)
	res, err := Evaluate([]*store.DecisionRecord{rec}, Config{
		Timeframe:       "1h",
		Fees:            connector.StaticFeeModel{},
		SizingMode:      SizingRisk,
		RiskPct:         1,
		OverrideStopPct: 2, // effective stop 98 → distance 2, not the recorded 10
		OverrideTakePct: 5, // target 105
		EquitySource:    func(time.Time) float64 { return 1000 },
	}, staticSource(kl))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	tr := res.Trades[0]
	if !near(tr.Quantity, 5) { // 10 USD risk / 2 = 5 units; the recorded stop would give 1
		t.Fatalf("quantity = %v, want 5 (sized off the overridden stop)", tr.Quantity)
	}
}

// An open that exists only in decision_json was rejected by our own validation
// (`validateDecisions`) and never reached the executor. Decision sizing must not
// resurrect it: that is a different counterfactual from a risk-gate block, and
// mixing the two inflated a live replay from 113 signals to 186.
func TestDecisionSizingExcludesValidationRejectedOpens(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	kl := hourlyKlines(base, []bar{
		{o: 100, h: 100, l: 100, c: 100},
		{o: 100, h: 111, l: 99, c: 110},
	})
	rec := &store.DecisionRecord{
		Timestamp:    base,
		DecisionJSON: `[{"symbol":"BTCUSDT","action":"open_long","position_size_usd":300,"stop_loss":95,"take_profit":110,"confidence":80}]`,
		// No execution entry: validateDecisions dropped it.
	}
	res, err := Evaluate([]*store.DecisionRecord{rec}, Config{
		Timeframe: "1h", Fees: connector.StaticFeeModel{}, SizingMode: SizingDecision,
	}, staticSource(kl))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.Opens != 0 || len(res.Trades) != 0 {
		t.Fatalf("validation-rejected open leaked into the replay: opens=%d trades=%d", res.Opens, len(res.Trades))
	}
}

// Giveback exit: arm after a favorable move, then close when a share of the peak
// profit is given back. The peak is updated after the checks, so the trigger is
// evaluated against the prior candle's peak.
func TestGivebackExitArmsAndTrails(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	kl := hourlyKlines(base, []bar{
		{o: 100, h: 100, l: 100, c: 100},
		{o: 100, h: 105, l: 100, c: 104}, // peak 105 (+5%), not armed within this candle
		{o: 104, h: 104, l: 103.4, c: 103.6},
	})
	rec := record(base, store.DecisionAction{
		Action: "open_long", Symbol: "BTCUSDT", Quantity: 1, StopLoss: 90, TakeProfit: 200, Confidence: 80,
	})
	res, err := Evaluate([]*store.DecisionRecord{rec}, Config{
		Timeframe: "1h", Fees: connector.StaticFeeModel{}, MaxHoldCandles: 10,
		ExitMode: ExitGiveback, GivebackArmPct: 2, GivebackPct: 30,
	}, staticSource(kl))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	tr := res.Trades[0]
	// peak +5%, keep 70% of it → trigger 103.5; the third candle's low is 103.4.
	if tr.ExitReason != "giveback" {
		t.Fatalf("exit reason = %q, want giveback", tr.ExitReason)
	}
	if !near(tr.ExitPrice, 103.5) {
		t.Fatalf("exit = %v, want 103.5", tr.ExitPrice)
	}
}

func TestGivebackDoesNotFireBelowArm(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	kl := hourlyKlines(base, []bar{
		{o: 100, h: 100, l: 100, c: 100},
		{o: 100, h: 101.5, l: 100, c: 101}, // +1.5% < arm 2%
		{o: 101, h: 101, l: 100.2, c: 100.4},
	})
	rec := record(base, store.DecisionAction{
		Action: "open_long", Symbol: "BTCUSDT", Quantity: 1, StopLoss: 90, TakeProfit: 200,
	})
	res, _ := Evaluate([]*store.DecisionRecord{rec}, Config{
		Timeframe: "1h", Fees: connector.StaticFeeModel{}, MaxHoldCandles: 2,
		ExitMode: ExitGiveback, GivebackArmPct: 2, GivebackPct: 30,
	}, staticSource(kl))
	if res.Trades[0].ExitReason != "timeout" {
		t.Fatalf("exit reason = %q, want timeout (trail never armed)", res.Trades[0].ExitReason)
	}
}

// A candle that spans both the stop and the trailing trigger is scored as the
// stop, matching the conservative bracket ordering.
func TestGivebackStopWinsWhenBothHit(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	kl := hourlyKlines(base, []bar{
		{o: 100, h: 100, l: 100, c: 100},
		{o: 100, h: 105, l: 100, c: 104}, // peak 105
		{o: 104, h: 104, l: 90, c: 95},   // spans both trigger 103.5 and stop 95
	})
	rec := record(base, store.DecisionAction{
		Action: "open_long", Symbol: "BTCUSDT", Quantity: 1, StopLoss: 95, TakeProfit: 200,
	})
	res, _ := Evaluate([]*store.DecisionRecord{rec}, Config{
		Timeframe: "1h", Fees: connector.StaticFeeModel{}, MaxHoldCandles: 10,
		ExitMode: ExitGiveback, GivebackArmPct: 2, GivebackPct: 30,
	}, staticSource(kl))
	if res.Trades[0].ExitReason != "stop_loss" {
		t.Fatalf("exit reason = %q, want stop_loss", res.Trades[0].ExitReason)
	}
}

// A short's trail mirrors the long: the peak is the lowest low and the trigger
// sits below entry.
func TestGivebackShort(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	kl := hourlyKlines(base, []bar{
		{o: 100, h: 100, l: 100, c: 100},
		{o: 100, h: 100, l: 95, c: 96},   // peak 95 (+5%)
		{o: 96, h: 96.6, l: 96, c: 96.4}, // trigger 96.5 → high 96.6 crosses
	})
	rec := record(base, store.DecisionAction{
		Action: "open_short", Symbol: "BTCUSDT", Quantity: 1, StopLoss: 110, TakeProfit: 50,
	})
	res, _ := Evaluate([]*store.DecisionRecord{rec}, Config{
		Timeframe: "1h", Fees: connector.StaticFeeModel{}, MaxHoldCandles: 10,
		ExitMode: ExitGiveback, GivebackArmPct: 2, GivebackPct: 30,
	}, staticSource(kl))
	tr := res.Trades[0]
	if tr.ExitReason != "giveback" || !near(tr.ExitPrice, 96.5) {
		t.Fatalf("short trail = %s @ %v, want giveback @ 96.5", tr.ExitReason, tr.ExitPrice)
	}
}

// Unknown modes are typos, not silent fallbacks.
func TestUnknownModesRejected(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	kl := hourlyKlines(base, []bar{{o: 100, h: 100, l: 100, c: 100}})
	rec := record(base, store.DecisionAction{Action: "open_long", Symbol: "BTCUSDT", Quantity: 1})

	if _, err := Evaluate([]*store.DecisionRecord{rec}, Config{Timeframe: "1h", SizingMode: "sized"}, staticSource(kl)); err == nil {
		t.Fatal("unknown sizing mode must be rejected")
	}
	if _, err := Evaluate([]*store.DecisionRecord{rec}, Config{Timeframe: "1h", ExitMode: "trailing"}, staticSource(kl)); err == nil {
		t.Fatal("unknown exit mode must be rejected")
	}
}
