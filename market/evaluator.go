package market

import "fmt"

// Note is a computed indicator value. Multi-value indicators (e.g. Bollinger
// Bands) populate Values; single-value ones use Value.
type Note struct {
	Name   string
	Value  float64
	Values map[string]float64
}

// Evaluator computes an indicator from a K-line series. It is the evaluation
// seam: the strategy layer reads indicators through Evaluator rather than
// calling the raw calculation helpers directly.
type Evaluator interface {
	Name() string
	Evaluate(klines []Kline) Note
}

// EMA evaluates an exponential moving average.
type EMA struct{ Period int }

func (e EMA) Name() string { return fmt.Sprintf("ema%d", e.Period) }
func (e EMA) Evaluate(klines []Kline) Note {
	return Note{Name: e.Name(), Value: calculateEMA(klines, e.Period)}
}

// MACD evaluates the MACD line (EMA12 - EMA26).
type MACD struct{}

func (MACD) Name() string { return "macd" }
func (MACD) Evaluate(klines []Kline) Note {
	return Note{Name: "macd", Value: calculateMACD(klines)}
}

// RSI evaluates the Wilder RSI.
type RSI struct{ Period int }

func (e RSI) Name() string { return fmt.Sprintf("rsi%d", e.Period) }
func (e RSI) Evaluate(klines []Kline) Note {
	return Note{Name: e.Name(), Value: calculateRSI(klines, e.Period)}
}

// ATR evaluates the average true range.
type ATR struct{ Period int }

func (e ATR) Name() string { return fmt.Sprintf("atr%d", e.Period) }
func (e ATR) Evaluate(klines []Kline) Note {
	return Note{Name: e.Name(), Value: calculateATR(klines, e.Period)}
}

// BOLL evaluates Bollinger Bands (upper/middle/lower). Mult defaults to 2.
type BOLL struct {
	Period int
	Mult   float64
}

func (b BOLL) Name() string { return fmt.Sprintf("boll%d", b.Period) }
func (b BOLL) Evaluate(klines []Kline) Note {
	mult := b.Mult
	if mult == 0 {
		mult = 2.0
	}
	upper, middle, lower := calculateBOLL(klines, b.Period, mult)
	return Note{
		Name: b.Name(),
		Values: map[string]float64{
			"upper":  upper,
			"middle": middle,
			"lower":  lower,
		},
	}
}

// Donchian evaluates a Donchian channel.
type Donchian struct{ Period int }

func (d Donchian) Name() string { return fmt.Sprintf("donchian%d", d.Period) }
func (d Donchian) Evaluate(klines []Kline) Note {
	upper, lower := calculateDonchian(klines, d.Period)
	return Note{
		Name:   d.Name(),
		Values: map[string]float64{"upper": upper, "lower": lower},
	}
}

// EvaluateAll runs a set of evaluators over the same series, preserving order.
func EvaluateAll(evaluators []Evaluator, klines []Kline) []Note {
	notes := make([]Note, 0, len(evaluators))
	for _, e := range evaluators {
		notes = append(notes, e.Evaluate(klines))
	}
	return notes
}
