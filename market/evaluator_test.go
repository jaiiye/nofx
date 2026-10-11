package market

import "testing"

func sampleKlines(n int) []Kline {
	ks := make([]Kline, n)
	for i := range ks {
		p := 100 + float64(i)
		ks[i] = Kline{
			OpenTime: int64(i),
			Open:     p,
			High:     p + 1,
			Low:      p - 1,
			Close:    p + 0.5,
			Volume:   10,
		}
	}
	return ks
}

func TestEvaluatorsMatchCalculators(t *testing.T) {
	k := sampleKlines(80)

	if got, want := (EMA{Period: 20}).Evaluate(k).Value, calculateEMA(k, 20); got != want {
		t.Fatalf("EMA = %v, want %v", got, want)
	}
	if got, want := (MACD{}).Evaluate(k).Value, calculateMACD(k); got != want {
		t.Fatalf("MACD = %v, want %v", got, want)
	}
	if got, want := (RSI{Period: 14}).Evaluate(k).Value, calculateRSI(k, 14); got != want {
		t.Fatalf("RSI = %v, want %v", got, want)
	}
	if got, want := (ATR{Period: 14}).Evaluate(k).Value, calculateATR(k, 14); got != want {
		t.Fatalf("ATR = %v, want %v", got, want)
	}
}

func TestBollEvaluator(t *testing.T) {
	k := sampleKlines(40)
	note := (BOLL{Period: 20}).Evaluate(k)
	u, m, l := calculateBOLL(k, 20, 2.0)
	if note.Values["upper"] != u || note.Values["middle"] != m || note.Values["lower"] != l {
		t.Fatalf("BOLL values = %+v, want upper=%v middle=%v lower=%v", note.Values, u, m, l)
	}
}

func TestDonchianEvaluator(t *testing.T) {
	k := sampleKlines(40)
	note := (Donchian{Period: 20}).Evaluate(k)
	u, l := calculateDonchian(k, 20)
	if note.Values["upper"] != u || note.Values["lower"] != l {
		t.Fatalf("Donchian values = %+v, want upper=%v lower=%v", note.Values, u, l)
	}
}

func TestEvaluateAllPreservesOrder(t *testing.T) {
	k := sampleKlines(40)
	notes := EvaluateAll([]Evaluator{EMA{Period: 20}, RSI{Period: 14}}, k)
	if len(notes) != 2 {
		t.Fatalf("got %d notes, want 2", len(notes))
	}
	if notes[0].Name != "ema20" || notes[1].Name != "rsi14" {
		t.Fatalf("note order = %q,%q, want ema20,rsi14", notes[0].Name, notes[1].Name)
	}
}
