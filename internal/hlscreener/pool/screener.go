// Package pool implements the 5-layer candidate-pool screener.
//
// Filters (apply in order, all must pass for in_pool=true):
//  1. Liquidity:     24h vol > $50M, OI > $10M
//  2. Volatility:    24h ATR% in [0.5%, 8%]      — dead or wild both rejected
//  3. Structure:     EMA20 slope > 0 OR < 0       — must have direction
//  4. Volume/Price:  CVD direction agrees with price action
//  5. Event:         |funding| < 0.1% / 8h         — avoid one-sided crowding
//
// Score is 0-100, weighted sum of the layer scores that passed.
package pool

import (
	"context"
	"fmt"
	"log"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"

	"nofx/internal/hlscreener/hyperliquid"
	"nofx/internal/hlscreener/store"
)

type Screener struct {
	src   PriceSource
	store StoreReader
	th    Thresholds
}

// PriceSource is the minimal interface the screener needs. The production
// `*hyperliquid.Client` satisfies it; tests provide a stub.
type PriceSource interface {
	AllMids(ctx context.Context) (map[string]string, error)
	Candles(ctx context.Context, coin, interval string, startTimeMs int64) ([]hyperliquid.Candle, error)
	OpenInterest(ctx context.Context, coin string) (*hyperliquid.OpenInterest, error)
	FundingHistory(ctx context.Context, coin string, startTimeMs int64) ([]hyperliquid.Funding, error)
}

// StoreReader is the subset of *store.Store that Screener needs.
// Tests provide an in-memory implementation; production uses
// *store.Store. Methods that take concrete store types
// (CVDBar, Candidate) are passed as *struct or via adapters.
type StoreReader interface {
	CVDPanel(ctx context.Context, coin string, fromMs, toMs int64) ([]store.CVDPoint, error)
	OIHistory(ctx context.Context, coin string, limit int) ([]store.OISnapshot, error)
	ReplaceActivePool(ctx context.Context, candidates []store.Candidate) error
}

type Thresholds struct {
	Min24hVolUSD  float64
	MinOIUSD      float64
	MaxATRPct     float64
	MinATRPct     float64
	MaxFundingPct float64
}

func DefaultThresholds() Thresholds {
	return Thresholds{
		Min24hVolUSD:  50_000_000,
		MinOIUSD:      10_000_000,
		MaxATRPct:     8.0,
		MinATRPct:     0.5,
		MaxFundingPct: 0.1,
	}
}

func New(src PriceSource, st StoreReader, th Thresholds) *Screener {
	if th.Min24hVolUSD == 0 {
		th = DefaultThresholds()
	}
	return &Screener{src: src, store: st, th: th}
}

type Result struct {
	Coin   string
	Score  float64
	InPool bool
	Layers map[string]LayerResult
}

type LayerResult struct {
	Pass    bool
	Score   float64
	Reason  string
	Metrics map[string]float64
}

// Run executes the 5-layer filter on every coin in `coins` (e.g. top 20).
// Returns the survivors in score-descending order, plus a per-layer stats map.
func (s *Screener) Run(ctx context.Context, coins []string) (survivors []Result, layerStats map[string]int, err error) {
	layerStats = map[string]int{
		"input":      len(coins),
		"liquidity":  0,
		"volatility": 0,
		"structure":  0,
		"volprice":   0,
		"event":      0,
		"survivors":  0,
	}
	results := make([]Result, 0, len(coins))
	resultsMu := sync.Mutex{}

	// Pre-fetch shared data once: AllMids + AssetContexts cover
	// every coin's (mark, OI, funding) in 2 RPCs. evaluateShared
	// uses these to skip per-coin OpenInterest/FundingHistory calls,
	// cutting per-coin RPCs from 3 to 1.
	mids, err := s.src.AllMids(ctx)
	if err != nil {
		return nil, layerStats, err
	}
	var sharedCtxs *hyperliquid.AssetContexts
	if ac, ok := s.src.(interface {
		AssetContexts(ctx context.Context) (*hyperliquid.AssetContexts, error)
	}); ok {
		sharedCtxs, _ = ac.AssetContexts(ctx)
	}

	limiter := newRateLimiter(8)

	work := func(ctx context.Context, coin string) error {
		r, err := s.evaluateShared(ctx, coin, mids, sharedCtxs)
		if err != nil {
			log.Printf("[screener] %s: %v", coin, err)
			return nil // don't abort the whole run
		}
		resultsMu.Lock()
		results = append(results, r)
		if r.Layers["liquidity"].Pass {
			layerStats["liquidity"]++
		}
		if r.Layers["volatility"].Pass {
			layerStats["volatility"]++
		}
		if r.Layers["structure"].Pass {
			layerStats["structure"]++
		}
		if r.Layers["volprice"].Pass {
			layerStats["volprice"]++
		}
		if r.Layers["event"].Pass {
			layerStats["event"]++
		}
		if r.InPool {
			layerStats["survivors"]++
		}
		resultsMu.Unlock()
		return nil
	}

	_ = runParallel(ctx, coins, limiter, 16, work)

	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	return results, layerStats, nil
}

// evaluate calls src.AllMids/Candles/OpenInterest/FundingHistory per
// coin. Kept for backward compatibility; new code uses evaluateShared.
func (s *Screener) evaluate(ctx context.Context, coin string) (Result, error) {
	return s.evaluateShared(ctx, coin, nil, nil)
}

// evaluateShared is the same as evaluate but accepts pre-fetched
// mids + AssetContexts to avoid per-coin AllMids/OpenInterest calls.
// Either or both can be nil — the function falls back to per-coin
// calls as needed.
func (s *Screener) evaluateShared(ctx context.Context, coin string, sharedMids map[string]string, sharedCtxs *hyperliquid.AssetContexts) (Result, error) {
	r := Result{Coin: coin, Layers: map[string]LayerResult{}}

	mids := sharedMids
	if mids == nil {
		var err error
		mids, err = s.src.AllMids(ctx)
		if err != nil {
			return r, err
		}
	}
	midStr, ok := mids[coin]
	if !ok {
		return r, fmt.Errorf("not in mids: %s", coin)
	}
	mid := parseF(midStr)

	// 24h candle. Use CandlesRange with a tight 24h window — HL
	// only keeps ~5000 bars, so 7d lookback often returns empty for
	// low-volume coins.
	now := time.Now().UnixMilli()
	dayAgo := now - 24*3600*1000
	var (
		candles []hyperliquid.Candle
		err     error
	)
	if cr, ok := s.src.(interface {
		CandlesRange(ctx context.Context, coin, interval string, startTimeMs, endTimeMs int64) ([]hyperliquid.Candle, error)
	}); ok {
		candles, err = cr.CandlesRange(ctx, coin, "1h", dayAgo, now)
	} else {
		candles, err = s.src.Candles(ctx, coin, "1h", dayAgo)
	}
	if err != nil {
		return r, err
	}

	vol24h, atrPct := s.deriveVolATR(candles)

	// Try AssetContexts cache for OI + funding (single shared call
	// covers all 30 coins). Fall back to per-coin OpenInterest.
	var oiUSD, latestFunding float64
	usedShared := false
	if sharedCtxs != nil {
		for i, a := range sharedCtxs.Universe {
			if a.Name != coin {
				continue
			}
			actx := sharedCtxs.Contexts[i]
			oiUSD = actx.OI() * mid
			f, _ := strconv.ParseFloat(actx.Funding, 64)
			latestFunding = f * 100 // 8h-equivalent → %
			usedShared = true
			break
		}
	}
	if !usedShared {
		oi, err := s.src.OpenInterest(ctx, coin)
		if err != nil {
			return r, err
		}
		oiUSD = oi.OpenInterest * mid
		fundings, err := s.src.FundingHistory(ctx, coin, now-3*3600*1000)
		if err != nil {
			return r, err
		}
		latestFunding = 0.0
		if len(fundings) > 0 {
			latestFunding = fundings[len(fundings)-1].Rate() * 100 // to %
		}
	}

	// 24h CVD delta (nil store = no historical CVD; treat as flat)
	cvd24h := 0.0
	if s.store != nil {
		points, err := s.store.CVDPanel(ctx, coin, now-24*3600*1000, now)
		if err != nil {
			return r, err
		}
		if len(points) > 0 {
			cvd24h = points[len(points)-1].C - points[0].O
		}
	}

	// 24h price change
	priceChg := 0.0
	if len(candles) > 0 {
		first := candles[0].Open()
		last := candles[len(candles)-1].Close()
		if first > 0 {
			priceChg = (last - first) / first * 100
		}
	}

	// EMA20 slope (last 5 hourly deltas, normalized)
	emaSlope := s.ema20Slope(candles)

	// Layer 1: liquidity
	r.Layers["liquidity"] = LayerResult{
		Pass:   vol24h >= s.th.Min24hVolUSD && oiUSD >= s.th.MinOIUSD,
		Score:  scaleToBand(vol24h, s.th.Min24hVolUSD, s.th.Min24hVolUSD*10) * 0.5 +
			scaleToBand(oiUSD, s.th.MinOIUSD, s.th.MinOIUSD*10) * 0.5,
		Reason:  "vol + OI in USD",
		Metrics: map[string]float64{"vol_usd": vol24h, "oi_usd": oiUSD},
	}

	// Layer 2: volatility
	r.Layers["volatility"] = LayerResult{
		Pass:   atrPct >= s.th.MinATRPct && atrPct <= s.th.MaxATRPct,
		Score:  1 - math.Abs(atrPct-3)/3, // ideal ~3% ATR
		Reason:  "ATR% in band",
		Metrics: map[string]float64{"atr_pct": atrPct},
	}

	// Layer 3: structure. Combines price trend (EMA slope) with
	// OI trend (if available in store). A real trend is price
	// AND OI both moving in the same direction; divergent OI is
	// a weaker signal. With no OI data we fall back to EMA only.
	oiTrend := 0.0
	oiPct := 0.0
	if s.store != nil {
		hist, herr := s.store.OIHistory(ctx, coin, 12)
		if herr == nil && len(hist) >= 2 {
			oiTrend = hist[len(hist)-1].OIUSD - hist[0].OIUSD
			if hist[0].OIUSD > 0 {
				oiPct = oiTrend / hist[0].OIUSD * 100
			}
		}
	}
	// Score blends price direction + (if available) OI agreement.
	// Strong trend: price direction and OI direction agree.
	// Weak trend: price direction alone, no OI confirmation.
	// Counter-trend: price and OI diverge (down-grade).
	structScore := math.Min(1, math.Abs(emaSlope)/2.0)
	structPass := math.Abs(emaSlope) > 0.1
	if oiTrend != 0 {
		// OI confirmation boost: same direction as price
		priceUp := emaSlope > 0
		oiUp := oiTrend > 0
		if priceUp == oiUp {
			structScore = math.Min(1, structScore*1.2)
		} else {
			// Divergence: down-grade
			structScore = structScore * 0.5
			// Only fail if OI strongly disagrees
			if math.Abs(oiPct) > 5 {
				structPass = false
			}
		}
	}
	r.Layers["structure"] = LayerResult{
		Pass:   structPass,
		Score:  structScore,
		Reason: "price trend + OI confirmation",
		Metrics: map[string]float64{
			"ema_slope": emaSlope,
			"oi_pct_24h": oiPct,
		},
	}

	// Layer 4: volprice — CVD agrees with price direction
	agree := (priceChg > 0 && cvd24h > 0) || (priceChg < 0 && cvd24h < 0) || math.Abs(priceChg) < 0.5
	r.Layers["volprice"] = LayerResult{
		Pass:   agree,
		Score:  scaleToBand(math.Abs(cvd24h), 0, 10_000_000),
		Reason:  "CVD vs price agreement",
		Metrics: map[string]float64{"cvd_24h": cvd24h, "price_chg_pct": priceChg},
	}

	// Layer 5: event — funding not extreme
	r.Layers["event"] = LayerResult{
		Pass:   math.Abs(latestFunding) < s.th.MaxFundingPct,
		Score:  1 - math.Abs(latestFunding)/s.th.MaxFundingPct,
		Reason:  "funding within ±0.1%/8h",
		Metrics: map[string]float64{"funding_pct": latestFunding},
	}

	// In pool iff all pass
	allPass := true
	totalScore := 0.0
	for _, lr := range r.Layers {
		if !lr.Pass {
			allPass = false
		}
		totalScore += lr.Score
	}
	r.InPool = allPass
	r.Score = (totalScore / 5) * 100
	return r, nil
}

// deriveVolATR returns 24h notional vol and ATR% from 1h candles.
func (s *Screener) deriveVolATR(candles []hyperliquid.Candle) (volUsd, atrPct float64) {
	if len(candles) == 0 {
		return
	}
	trs := 0.0
	for i, c := range candles {
		hi, lo := c.High(), c.Low()
		tr := hi - lo
		if i > 0 {
			prev := candles[i-1].Close()
			tr = math.Max(tr, math.Abs(hi-prev))
			tr = math.Max(tr, math.Abs(lo-prev))
		}
		trs += tr
		volUsd += c.Volume() * c.Close()
	}
	atr := trs / float64(len(candles))
	last := candles[len(candles)-1].Close()
	if last > 0 {
		atrPct = atr / last * 100
	}
	return
}

func (s *Screener) ema20Slope(candles []hyperliquid.Candle) float64 {
	if len(candles) < 20 {
		return 0
	}
	k := 2.0 / 21.0
	ema := candles[0].Close()
	for _, c := range candles[1:] {
		ema = c.Close()*k + ema*(1-k)
	}
	// slope: ema now vs ema 5 bars ago, normalized
	ema5 := candles[0].Close()
	ema = candles[0].Close()
	for i, c := range candles {
		if i < 5 {
			ema5 = c.Close()*k + ema5*(1-k)
		}
		ema = c.Close()*k + ema*(1-k)
	}
	last := candles[len(candles)-1].Close()
	if last == 0 {
		return 0
	}
	return (ema - ema5) / last * 100
}

func scaleToBand(v, lo, hi float64) float64 {
	if v <= lo {
		return 0
	}
	if v >= hi {
		return 1
	}
	return (v - lo) / (hi - lo)
}

func parseF(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

// PersistResults writes survivors into candidate_pool + candidate_active.
func (s *Screener) PersistResults(ctx context.Context, results []Result) error {
	now := time.Now().UnixMilli()
	expires := now + 4*3600*1000
	cands := make([]store.Candidate, 0, len(results))
	for _, r := range results {
		reasons := map[string]any{}
		for k, v := range r.Layers {
			reasons[k] = map[string]any{
				"pass":    v.Pass,
				"score":   v.Score,
				"metrics": v.Metrics,
				"reason":  v.Reason,
			}
		}
		cands = append(cands, store.Candidate{
			Coin:        r.Coin,
			Score:       r.Score,
			Reasons:     reasons,
			InPool:      r.InPool,
			GeneratedAt: now,
			ExpiresAt:   expires,
		})
	}
	return s.store.ReplaceActivePool(ctx, cands)
}
