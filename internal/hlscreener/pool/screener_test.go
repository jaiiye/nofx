package pool

import (
	"context"
	"math"
	"strconv"
	"testing"
	"time"

	"nofx/internal/hlscreener/hyperliquid"
)

// stubSource implements PriceSource for unit tests. Each test builds the
// market it wants and reads back.
type stubSource struct {
	mids      map[string]string
	candles   map[string][]hyperliquid.Candle // coin -> 1h candles
	oi        map[string]*hyperliquid.OpenInterest
	fundings  map[string][]hyperliquid.Funding
	calls     int
}

func (s *stubSource) AllMids(_ context.Context) (map[string]string, error) {
	s.calls++
	return s.mids, nil
}
func (s *stubSource) Candles(_ context.Context, coin, _ string, _ int64) ([]hyperliquid.Candle, error) {
	return s.candles[coin], nil
}
func (s *stubSource) OpenInterest(_ context.Context, coin string) (*hyperliquid.OpenInterest, error) {
	return s.oi[coin], nil
}
func (s *stubSource) FundingHistory(_ context.Context, coin string, _ int64) ([]hyperliquid.Funding, error) {
	return s.fundings[coin], nil
}

// buildCandles returns 24 hourly candles representing a coin with the
// requested price trajectory. ATR% is roughly controlled by `atrPct`.
//   trendPct  : total 24h price change in %
//   atrPct    : average intrabar range as % of price
//   basePrice : the starting price
func buildCandles(basePrice, trendPct, atrPct float64) []hyperliquid.Candle {
	out := make([]hyperliquid.Candle, 24)
	step := trendPct / 24.0
	for i := 0; i < 24; i++ {
		px := basePrice * (1 + step*float64(i)/100)
		half := px * atrPct / 100 / 2
		out[i] = hyperliquid.Candle{
			T: time.Now().Add(-time.Duration(24-i) * time.Hour).UnixMilli(),
			O: f2s(px), C: f2s(px), H: f2s(px + half), L: f2s(px - half),
			V: f2s(1_000_000),
		}
	}
	return out
}

// f2s formats a float as the string Candle fields expect.
func f2s(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// mustF parses a Candle string field back to float (test helper).
func mustF(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

// findCoin returns the Result for a given coin (test helper).
func findCoin(rs []Result, coin string) (Result, bool) {
	for _, r := range rs {
		if r.Coin == coin {
			return r, true
		}
	}
	return Result{}, false
}

func TestScreener_LiquidityGate(t *testing.T) {
	tests := []struct {
		name    string
		vol24h  float64 // USD
		oiUSD   float64
		wantPass bool
	}{
		{"both above threshold", 60_000_000, 20_000_000, true},
		{"vol below", 30_000_000, 50_000_000, false},
		{"oi below", 200_000_000, 5_000_000, false},
		{"both below", 1_000_000, 100_000, false},
		{"right at threshold", 50_000_000, 10_000_000, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			coin := "TST"
			// Build candles whose sum(v*c) over 24 bars equals tc.vol24h.
			// vol24h = 24 * v * c, so v = vol24h / (24 * c).
			cs := buildCandles(100, 0, 3)
			perBarV := tc.vol24h / (24 * 100)
			for i := range cs {
				cs[i].V = f2s(perBarV)
			}
			src := &stubSource{
				mids:     map[string]string{coin: "100"},
				candles:  map[string][]hyperliquid.Candle{coin: cs},
				oi:       map[string]*hyperliquid.OpenInterest{coin: {Coin: coin, OpenInterest: tc.oiUSD / 100}},
				fundings: map[string][]hyperliquid.Funding{coin: {{Coin: coin, FundingRate: f2s(0.0001), Time: 0}}},
			}
			s := New(src, nil, DefaultThresholds())
			rs, _, err := s.Run(context.Background(), []string{coin})
			if err != nil {
				t.Fatal(err)
			}
			r, ok := findCoin(rs, coin)
			if !ok {
				t.Fatal("coin missing")
			}
			if r.Layers["liquidity"].Pass != tc.wantPass {
				t.Errorf("liquidity pass = %v, want %v (vol=%.0f oi=%.0f metrics=%+v)",
					r.Layers["liquidity"].Pass, tc.wantPass, tc.vol24h, tc.oiUSD, r.Layers["liquidity"].Metrics)
			}
		})
	}
}

func TestScreener_VolatilityGate(t *testing.T) {
	tests := []struct {
		name     string
		atrPct   float64
		wantPass bool
	}{
		{"dead coin", 0.1, false},
		{"low vol but ok", 0.8, true},
		{"ideal", 3.0, true},
		{"high but ok", 7.5, true},
		{"wild", 12.0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			coin := "TST"
			src := &stubSource{
				mids:     map[string]string{coin: "100"},
				candles:  map[string][]hyperliquid.Candle{coin: makeLiquidCandles(100, tc.atrPct)},
				oi:       map[string]*hyperliquid.OpenInterest{coin: {Coin: coin, OpenInterest: 200_000}},
				fundings: map[string][]hyperliquid.Funding{coin: {{Coin: coin, FundingRate: f2s(0.0001), Time: 0}}},
			}
			s := New(src, nil, DefaultThresholds())
			rs, _, err := s.Run(context.Background(), []string{coin})
			if err != nil {
				t.Fatal(err)
			}
			r, _ := findCoin(rs, coin)
			if r.Layers["volatility"].Pass != tc.wantPass {
				t.Errorf("volatility pass = %v, want %v (atr_pct=%.2f)",
					r.Layers["volatility"].Pass, tc.wantPass, r.Layers["volatility"].Metrics["atr_pct"])
			}
		})
	}
}

func TestScreener_EventGate(t *testing.T) {
	tests := []struct {
		name    string
		funding float64 // 8h rate, e.g. 0.0001 = 0.01%
		wantPass bool
	}{
		{"flat funding", 0.0001, true},
		{"mild long bias", 0.0005, true},
		{"right at boundary", 0.001, false}, // 0.1%/8h is the upper edge; strict < means no
		{"just under boundary", 0.0009, true},
		{"over threshold", 0.002, false}, // 0.2%/8h
		{"strong short bias", -0.003, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			coin := "TST"
			src := &stubSource{
				mids:     map[string]string{coin: "100"},
				candles:  map[string][]hyperliquid.Candle{coin: makeLiquidCandles(100, 3)},
				oi:       map[string]*hyperliquid.OpenInterest{coin: {Coin: coin, OpenInterest: 200_000}},
				fundings: map[string][]hyperliquid.Funding{coin: {{Coin: coin, FundingRate: f2s(tc.funding), Time: 0}}},
			}
			s := New(src, nil, DefaultThresholds())
			rs, _, _ := s.Run(context.Background(), []string{coin})
			r, _ := findCoin(rs, coin)
			if r.Layers["event"].Pass != tc.wantPass {
				t.Errorf("event pass = %v, want %v (funding=%.4f)",
					r.Layers["event"].Pass, tc.wantPass, tc.funding)
			}
		})
	}
}

func TestScreener_VolPriceAgreement(t *testing.T) {
	tests := []struct {
		name     string
		trendPct float64
		// We cannot control CVD via this stub (no store), so
		// CVDPanel returns [] and cvd24h=0. That means agreement
		// reduces to |price_chg| < 0.5% (the "flat" branch).
		wantPass bool
	}{
		{"flat day, no cvd", 0.0, true},
		{"small move", 0.3, true},
		{"big move, no cvd = disagreement", 5.0, false},
		{"huge down", -10.0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			coin := "TST"
			src := &stubSource{
				mids:     map[string]string{coin: "100"},
				candles:  map[string][]hyperliquid.Candle{coin: makeLiquidCandles(100, 3)},
				oi:       map[string]*hyperliquid.OpenInterest{coin: {Coin: coin, OpenInterest: 200_000}},
				fundings: map[string][]hyperliquid.Funding{coin: {{Coin: coin, FundingRate: f2s(0), Time: 0}}},
			}
			// override prices for the trend case
			cs := src.candles[coin]
			startPrice := 100.0
			for i := range cs {
				o := startPrice * (1 + tc.trendPct*float64(i)/24.0/100)
				cs[i].O = f2s(o)
				cs[i].C = f2s(o)
				cs[i].H = f2s(o * 1.015)
				cs[i].L = f2s(o * 0.985)
			}
			src.candles[coin] = cs
			s := New(src, nil, DefaultThresholds())
			rs, _, _ := s.Run(context.Background(), []string{coin})
			r, _ := findCoin(rs, coin)
			if r.Layers["volprice"].Pass != tc.wantPass {
				t.Errorf("volprice pass = %v, want %v (trend=%.2f%%)",
					r.Layers["volprice"].Pass, tc.wantPass, tc.trendPct)
			}
		})
	}
}

func TestScreener_ScoreOrdering(t *testing.T) {
	// Two coins with identical inputs except funding → should produce
	// the same score (deterministic).
	coinA, coinB := "AAA", "BBB"
	src := &stubSource{
		mids: map[string]string{coinA: "100", coinB: "100"},
		candles: map[string][]hyperliquid.Candle{
			coinA: makeLiquidCandles(100, 3),
			coinB: makeLiquidCandles(100, 3),
		},
		oi: map[string]*hyperliquid.OpenInterest{
			coinA: {Coin: coinA, OpenInterest: 200_000},
			coinB: {Coin: coinB, OpenInterest: 200_000},
		},
		fundings: map[string][]hyperliquid.Funding{
			coinA: {{Coin: coinA, FundingRate: f2s(0.0001), Time: 0}},
			coinB: {{Coin: coinB, FundingRate: f2s(0.0001), Time: 0}},
		},
	}

	s := New(src, nil, DefaultThresholds())
	rs, _, _ := s.Run(context.Background(), []string{coinA, coinB})
	if len(rs) != 2 {
		t.Fatalf("expected 2 results, got %d", len(rs))
	}
	if math.Abs(rs[0].Score-rs[1].Score) > 1e-6 {
		t.Errorf("identical inputs → scores should match: %.4f vs %.4f", rs[0].Score, rs[1].Score)
	}
}

func TestScreener_LayerStats(t *testing.T) {
	// 4 coins: 2 should pass everything (trending + liquid + reasonable vol),
	// 1 fails liquidity, 1 fails volatility. "Good" coins have a slight
	// trend (1% over 24h) so EMA slope is non-zero.
	coins := []string{"GOOD1", "GOOD2", "NOLIQ", "WILD"}
	src := &stubSource{
		mids: map[string]string{},
		candles: map[string][]hyperliquid.Candle{},
		oi: map[string]*hyperliquid.OpenInterest{},
		fundings: map[string][]hyperliquid.Funding{},
	}
	for _, c := range coins {
		src.mids[c] = "100"
		atr := 3.0
		if c == "WILD" {
			atr = 12.0
		}
		// GOOD coins get a 0.3% trend so structure layer passes and
		// volprice passes (|priceChg| < 0.5 is the "flat" branch when
		// no CVD is available — the screener has no CVD when store=nil).
		trend := 0.0
		if c == "GOOD1" || c == "GOOD2" {
			trend = 0.3
		}
		cs := makeLiquidCandles(100, atr)
		// apply trend in-place
		for i := range cs {
			factor := 1.0 + trend*float64(i)/24.0/100
			cs[i].O = f2s(mustF(cs[i].O) * factor)
			cs[i].C = f2s(mustF(cs[i].C) * factor)
			cs[i].H = f2s(mustF(cs[i].H) * factor)
			cs[i].L = f2s(mustF(cs[i].L) * factor)
		}
		src.candles[c] = cs
		src.oi[c] = &hyperliquid.OpenInterest{Coin: c, OpenInterest: 200_000}
		src.fundings[c] = []hyperliquid.Funding{{Coin: c, FundingRate: f2s(0.0001), Time: 0}}
	}
	// NOLIQ: tiny OI
	src.oi["NOLIQ"] = &hyperliquid.OpenInterest{Coin: "NOLIQ", OpenInterest: 100} // = $10k OI

	s := New(src, nil, DefaultThresholds())
	_, stats, err := s.Run(context.Background(), coins)
	if err != nil {
		t.Fatal(err)
	}
	if stats["input"] != 4 {
		t.Errorf("input = %d, want 4", stats["input"])
	}
	// GOOD1, GOOD2: pass all 5; NOLIQ fails liquidity (layer 1);
	// WILD fails volatility (layer 2)
	if stats["liquidity"] != 3 {
		t.Errorf("liquidity = %d, want 3 (GOOD1, GOOD2, WILD)", stats["liquidity"])
	}
	if stats["volatility"] != 3 {
		t.Errorf("volatility = %d, want 3 (GOOD1, GOOD2, NOLIQ)", stats["volatility"])
	}
	if stats["survivors"] != 2 {
		t.Errorf("survivors = %d, want 2 (GOOD1, GOOD2)", stats["survivors"])
	}
}

func TestScreener_MissingCoinInMids(t *testing.T) {
	src := &stubSource{
		mids:     map[string]string{}, // empty
		candles:  map[string][]hyperliquid.Candle{},
		oi:       map[string]*hyperliquid.OpenInterest{},
		fundings: map[string][]hyperliquid.Funding{},
	}
	s := New(src, nil, DefaultThresholds())
	rs, _, err := s.Run(context.Background(), []string{"GHOST"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 0 {
		t.Errorf("missing coin should be skipped, got %d results", len(rs))
	}
}

// makeLiquidCandles returns candles with exactly 24h volume = 60M USD
// (above the 50M threshold) and the given ATR%. Centralizes the math
// so individual tests don't recompute it.
func makeLiquidCandles(price, atrPct float64) []hyperliquid.Candle {
	cs := buildCandles(price, 0, atrPct)
	perBarV := 60_000_000.0 / (24 * price)
	for i := range cs {
		cs[i].V = f2s(perBarV)
	}
	return cs
}
