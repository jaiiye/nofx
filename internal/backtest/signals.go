package backtest

import (
	"encoding/json"
	"math"
	"time"

	"nofx/store"
)

// signal is one open decision, normalized from whichever source the sizing mode
// reads: the AI's own JSON (carries position_size_usd, includes opens the risk
// gate blocked) or the execution records (carry the filled quantity only).
type signal struct {
	DecidedAt       time.Time
	Symbol          string
	Action          string
	StopLoss        float64
	TakeProfit      float64
	Confidence      int
	PositionSizeUSD float64
	Quantity        float64 // recorded fill; 0 when the live order never filled
}

// aiDecision mirrors the fields the replay needs from the stored decision JSON.
type aiDecision struct {
	Symbol          string  `json:"symbol"`
	Action          string  `json:"action"`
	PositionSizeUSD float64 `json:"position_size_usd"`
	StopLoss        float64 `json:"stop_loss"`
	TakeProfit      float64 `json:"take_profit"`
	Confidence      int     `json:"confidence"`
}

// collectSignals returns a record's open signals for the configured sizing mode.
//
// The execution list is the signal set: it contains every open that survived the
// decision engine and reached the executor — filled orders as well as ones the
// risk gate blocked (quantity 0). Opens that `validateDecisions` rejected never
// reach it, and they are deliberately excluded: they are a different
// counterfactual (what if our own validation had let them through), not a
// blocked trade.
//
// Recorded mode takes its quantity from the fill; decision and risk modes take
// the AI's position_size_usd from decision_json instead, which is what lets the
// gate-blocked signals be replayed.
func collectSignals(rec *store.DecisionRecord, mode string) []signal {
	aiByKey := make(map[string][]aiDecision)
	if mode != SizingRecorded && rec.DecisionJSON != "" {
		var ai []aiDecision
		if err := json.Unmarshal([]byte(rec.DecisionJSON), &ai); err == nil {
			for _, d := range ai {
				if isOpen(d.Action) {
					key := d.Symbol + "|" + d.Action
					aiByKey[key] = append(aiByKey[key], d)
				}
			}
		}
	}
	takeAI := func(symbol, action string) (aiDecision, bool) {
		key := symbol + "|" + action
		q := aiByKey[key]
		if len(q) == 0 {
			return aiDecision{}, false
		}
		aiByKey[key] = q[1:]
		return q[0], true
	}

	out := make([]signal, 0, len(rec.Decisions))
	for _, d := range rec.Decisions {
		if !isOpen(d.Action) {
			continue
		}
		s := signal{
			DecidedAt:  rec.Timestamp,
			Symbol:     d.Symbol,
			Action:     d.Action,
			StopLoss:   d.StopLoss,
			TakeProfit: d.TakeProfit,
			Confidence: d.Confidence,
			Quantity:   d.Quantity,
		}
		if mode != SizingRecorded {
			if ai, ok := takeAI(d.Symbol, d.Action); ok {
				s.PositionSizeUSD = ai.PositionSizeUSD
				// The execution record wins when it carries levels; the AI copy
				// fills the gaps for signals that never got a live order.
				if s.StopLoss <= 0 {
					s.StopLoss = ai.StopLoss
				}
				if s.TakeProfit <= 0 {
					s.TakeProfit = ai.TakeProfit
				}
				if s.Confidence == 0 {
					s.Confidence = ai.Confidence
				}
			}
		}
		out = append(out, s)
	}
	return out
}

// quantityFor sizes one signal under the configured mode. Zero means "cannot be
// sized", and the caller counts the signal as skipped. `stop` is the effective
// stop (after any sweep override): risk mode must divide by the stop it will
// actually trade, otherwise a stop grid would compare exits at one risk level
// with sizes computed from another.
func quantityFor(s signal, entry, stop float64, cfg Config) float64 {
	switch cfg.SizingMode {
	case SizingDecision:
		if s.PositionSizeUSD <= 0 || entry <= 0 {
			return 0
		}
		return s.PositionSizeUSD / entry
	case SizingRisk:
		stopDistance := math.Abs(entry - stop)
		equity := cfg.equityAt(s.DecidedAt)
		if stopDistance <= 0 || equity <= 0 {
			return 0
		}
		return equity * cfg.RiskPct / 100 / stopDistance
	default:
		return s.Quantity
	}
}
