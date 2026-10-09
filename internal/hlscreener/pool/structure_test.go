package pool

import (
	"context"
	"testing"
	"time"

	"nofx/internal/hlscreener/hyperliquid"
	"nofx/internal/hlscreener/store"
)

// stubStore implements StoreReader for tests.
type stubStore struct {
	oiHist     map[string][]store.OISnapshot // coin → ascending time
	cvdPanel   map[string][]store.CVDPoint
	activePool []store.Candidate
}

func (s *stubStore) OIHistory(_ context.Context, coin string, limit int) ([]store.OISnapshot, error) {
	hist := s.oiHist[coin]
	if len(hist) > limit {
		hist = hist[len(hist)-limit:]
	}
	return hist, nil
}

func (s *stubStore) CVDPanel(_ context.Context, coin string, _, _ int64) ([]store.CVDPoint, error) {
	return s.cvdPanel[coin], nil
}

func (s *stubStore) ReplaceActivePool(_ context.Context, cands []store.Candidate) error {
	s.activePool = cands
	return nil
}

// buildCandlesWithTrend makes 24 hourly candles trending up or down.
func buildCandlesWithTrend(price, trendPct, atrPct float64) []hyperliquid.Candle {
	cs := make([]hyperliquid.Candle, 24)
	step := trendPct / 24.0
	for i := 0; i < 24; i++ {
		px := price * (1 + step*float64(i)/100)
		half := px * atrPct / 100 / 2
		cs[i] = hyperliquid.Candle{
			T: time.Now().Add(-time.Duration(24-i) * time.Hour).UnixMilli(),
			O: f2s(px), C: f2s(px), H: f2s(px + half), L: f2s(px - half),
			V: f2s(1_000_000),
		}
	}
	return cs
}

// TestStructure_PriceUpOIUp_Boost verifies that price trend + OI
// agreement strengthens the structure pass + score.
func TestStructure_PriceUpOIUp_Boost(t *testing.T) {
	coin := "TST"
	now := time.Now().UnixMilli()
	// Price trending up + OI rising (bullish confirmation)
	src := &stubSource{
		mids: map[string]string{coin: "100"},
		candles: map[string][]hyperliquid.Candle{
			coin: buildCandlesWithTrend(100, 1.0, 3.0), // 1% up trend
		},
		oi: map[string]*hyperliquid.OpenInterest{
			coin: {Coin: coin, OpenInterest: 200_000},
		},
		fundings: map[string][]hyperliquid.Funding{coin: {{Coin: coin, FundingRate: f2s(0.0001), Time: 0}}},
	}
	// OI rising from 100M to 110M over 12h
	oiHist := make([]store.OISnapshot, 12)
	for i := 0; i < 12; i++ {
		oiHist[i] = store.OISnapshot{
			Coin: coin, T: now - int64(12-i)*3600*1000,
			OIUSD: 100_000_000 + float64(i)*833_333, // +10% over 12h
		}
	}
	st := &stubStore{
		oiHist:   map[string][]store.OISnapshot{coin: oiHist},
		cvdPanel: map[string][]store.CVDPoint{coin: nil},
	}
	s := New(src, st, DefaultThresholds())
	rs, _, err := s.Run(context.Background(), []string{coin})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := findCoin(rs, coin)
	str := r.Layers["structure"]
	if !str.Pass {
		t.Errorf("structure should pass (price+OI agree): %+v", str)
	}
	// Score should be boosted above baseline (price-only score).
	// ema_slope ≈ 0.56 for a 1% trend over 24h, so price-only
	// score = min(1, 0.56/2) = 0.28. With OI confirmation: 0.28 * 1.2 = 0.336.
	priceOnlyScore := math_min(1.0, math_abs(0.56)/2.0)
	if str.Score <= priceOnlyScore {
		t.Errorf("structure score %.2f should be boosted above price-only %.2f",
			str.Score, priceOnlyScore)
	}
	// OI pct should be reported
	if oiPct := str.Metrics["oi_pct_24h"]; math_abs(oiPct-10.0) > 1.0 {
		t.Errorf("oi_pct_24h = %v, want ~10", oiPct)
	}
}

// TestStructure_PriceUpOIDown_Weaken verifies divergence down-grades.
func TestStructure_PriceUpOIDown_Weaken(t *testing.T) {
	coin := "TST"
	now := time.Now().UnixMilli()
	src := &stubSource{
		mids: map[string]string{coin: "100"},
		candles: map[string][]hyperliquid.Candle{
			coin: buildCandlesWithTrend(100, 1.0, 3.0),
		},
		oi: map[string]*hyperliquid.OpenInterest{coin: {Coin: coin, OpenInterest: 200_000}},
		fundings: map[string][]hyperliquid.Funding{coin: {{Coin: coin, FundingRate: f2s(0.0001), Time: 0}}},
	}
	// OI falling -4% (divergence, but under the -5% failure
	// threshold so it should still pass with downgraded score)
	oiHist := make([]store.OISnapshot, 12)
	for i := 0; i < 12; i++ {
		oiHist[i] = store.OISnapshot{
			Coin: coin, T: now - int64(12-i)*3600*1000,
			OIUSD: 100_000_000 - float64(i)*333_333, // -4% over 12h
		}
	}
	st := &stubStore{
		oiHist:   map[string][]store.OISnapshot{coin: oiHist},
		cvdPanel: map[string][]store.CVDPoint{coin: nil},
	}
	s := New(src, st, DefaultThresholds())
	rs, _, _ := s.Run(context.Background(), []string{coin})
	r, _ := findCoin(rs, coin)
	str := r.Layers["structure"]
	if !str.Pass {
		t.Errorf("structure should still pass (small divergence): %+v", str)
	}
	// ema_slope ≈ 0.56, so price-only = 0.28. With -8% OI divergence
	// (under the -5% threshold... wait, |oi_pct|=8 > 5, so it WILL
	// fail). Use a smaller divergence to test the downgrade case.
	priceOnlyScore := math_min(1.0, math_abs(0.56)/2.0)
	if str.Score >= priceOnlyScore {
		t.Errorf("divergence should reduce score; got %.2f (price-only %.2f)",
			str.Score, priceOnlyScore)
	}
}

// TestStructure_PriceUpOIDown_FailOnBigDivergence verifies extreme
// divergence fails the structure layer.
func TestStructure_PriceUpOIDown_FailOnBigDivergence(t *testing.T) {
	coin := "TST"
	now := time.Now().UnixMilli()
	src := &stubSource{
		mids: map[string]string{coin: "100"},
		candles: map[string][]hyperliquid.Candle{
			coin: buildCandlesWithTrend(100, 1.0, 3.0),
		},
		oi: map[string]*hyperliquid.OpenInterest{coin: {Coin: coin, OpenInterest: 200_000}},
		fundings: map[string][]hyperliquid.Funding{coin: {{Coin: coin, FundingRate: f2s(0.0001), Time: 0}}},
	}
	// OI falling -10% (extreme divergence > 5% threshold)
	oiHist := make([]store.OISnapshot, 12)
	for i := 0; i < 12; i++ {
		oiHist[i] = store.OISnapshot{
			Coin: coin, T: now - int64(12-i)*3600*1000,
			OIUSD: 100_000_000 - float64(i)*833_333,
		}
	}
	st := &stubStore{
		oiHist:   map[string][]store.OISnapshot{coin: oiHist},
		cvdPanel: map[string][]store.CVDPoint{coin: nil},
	}
	s := New(src, st, DefaultThresholds())
	rs, _, _ := s.Run(context.Background(), []string{coin})
	r, _ := findCoin(rs, coin)
	if r.Layers["structure"].Pass {
		t.Error("structure should fail with extreme OI divergence")
	}
}

// TestStructure_NoOIHistory_FallsBackToPriceOnly verifies the
// screener still works when no OI data is in the store.
func TestStructure_NoOIHistory_FallsBackToPriceOnly(t *testing.T) {
	coin := "TST"
	src := &stubSource{
		mids: map[string]string{coin: "100"},
		candles: map[string][]hyperliquid.Candle{
			coin: buildCandlesWithTrend(100, 1.0, 3.0),
		},
		oi: map[string]*hyperliquid.OpenInterest{coin: {Coin: coin, OpenInterest: 200_000}},
		fundings: map[string][]hyperliquid.Funding{coin: {{Coin: coin, FundingRate: f2s(0.0001), Time: 0}}},
	}
	st := &stubStore{
		oiHist:   map[string][]store.OISnapshot{}, // empty
		cvdPanel: map[string][]store.CVDPoint{coin: nil},
	}
	s := New(src, st, DefaultThresholds())
	rs, _, _ := s.Run(context.Background(), []string{coin})
	r, _ := findCoin(rs, coin)
	if !r.Layers["structure"].Pass {
		t.Errorf("structure should pass on price alone when no OI: %+v",
			r.Layers["structure"])
	}
	if oiPct := r.Layers["structure"].Metrics["oi_pct_24h"]; oiPct != 0 {
		t.Errorf("oi_pct should be 0 when no data, got %v", oiPct)
	}
}

// minimal math helpers (no need to import math)
func math_min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
func math_abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
