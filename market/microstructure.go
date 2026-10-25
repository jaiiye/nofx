package market

import "math"

// MarketMicrostructure holds the decision-time order book metrics fed to the
// AI prompt: execution-quality and flow-tilt signals orthogonal to the
// price-history (k-lines) and flow-history (quant data) views.
//
// Deliberately snapshot-only: no cross-cycle state, no large-order detection
// (resting book orders are not real flow). Crypto symbols read the Binance
// book; xyz dex assets route to the Hyperliquid L2 snapshot (see orderbook.go).
type MarketMicrostructure struct {
	SpreadPct        float64
	SpreadBps        float64
	Imbalance        float64 // (bidNotional-askNotional)/total over the top levels, [-1, 1]
	ImbalanceSide    string  // BUY / SELL / BALANCED
	BidDepthUSD      float64 // notional across the top depth levels
	AskDepthUSD      float64
	SupportLevels    []float64 // up to 3 largest bid levels below mid
	ResistanceLevels []float64 // up to 3 largest ask levels above mid
	LiquidityScore   int       // 0-100 composite (spread + depth + tilt penalties)
	VWAP             float64   // from the primary-timeframe klines
	VWAPDeviationPct float64   // (mid - vwap)/vwap * 100; 0 when no klines
}

const (
	microDepthLevels = 10 // levels summed for depth/imbalance
	microVWAPBars    = 30 // trailing klines used for VWAP
)

// AnalyzeMicrostructure derives the book metrics using the Binance reference
// thresholds. Venue-aware callers should use AnalyzeMicrostructureForVenue.
func AnalyzeMicrostructure(book *OrderBookDepth, klines []KlineBar) *MarketMicrostructure {
	return AnalyzeMicrostructureForVenue(VenueBinance, book, klines)
}

// AnalyzeMicrostructureForVenue derives the book metrics from a snapshot,
// scoring liquidity against the execution venue's own book profile. klines are
// the primary-timeframe bars already fetched for the symbol (VWAP source); nil
// klines simply leave the VWAP fields at zero. Returns nil when the book is
// unusable.
func AnalyzeMicrostructureForVenue(venue Venue, book *OrderBookDepth, klines []KlineBar) *MarketMicrostructure {
	if book == nil || len(book.Bids) == 0 || len(book.Asks) == 0 {
		return nil
	}
	mid := book.Mid()
	if mid <= 0 {
		return nil
	}

	m := &MarketMicrostructure{}

	spread := book.Asks[0].Price - book.Bids[0].Price
	m.SpreadPct = spread / mid * 100
	m.SpreadBps = m.SpreadPct * 100

	var bidNotional, askNotional float64
	for i := 0; i < microDepthLevels && i < len(book.Bids); i++ {
		bidNotional += book.Bids[i].Price * book.Bids[i].Size
	}
	for i := 0; i < microDepthLevels && i < len(book.Asks); i++ {
		askNotional += book.Asks[i].Price * book.Asks[i].Size
	}
	m.BidDepthUSD = bidNotional
	m.AskDepthUSD = askNotional

	if total := bidNotional + askNotional; total > 0 {
		m.Imbalance = (bidNotional - askNotional) / total
	}
	m.ImbalanceSide = "BALANCED"
	if m.Imbalance > 0.15 {
		m.ImbalanceSide = "BUY"
	} else if m.Imbalance < -0.15 {
		m.ImbalanceSide = "SELL"
	}

	m.SupportLevels = topLevelsBySize(book.Bids, mid, false, 3)
	m.ResistanceLevels = topLevelsBySize(book.Asks, mid, true, 3)

	m.LiquidityScore = microLiquidityScoreForVenue(m, venue)

	vwap := vwapFromKlines(klines, microVWAPBars)
	if vwap > 0 {
		m.VWAP = vwap
		m.VWAPDeviationPct = (mid - vwap) / vwap * 100
	}

	return m
}

// topLevelsBySize returns up to `n` price levels on one side of the mid,
// ordered by resting size (a simple, explainable stand-in for liquidity
// clustering).
func topLevelsBySize(levels []OrderBookLevel, mid float64, above bool, n int) []float64 {
	candidates := make([]OrderBookLevel, 0, len(levels))
	for _, l := range levels {
		if above && l.Price > mid {
			candidates = append(candidates, l)
		}
		if !above && l.Price < mid {
			candidates = append(candidates, l)
		}
	}
	// selection sort by size descending, capped at n
	out := make([]float64, 0, n)
	for len(out) < n && len(candidates) > 0 {
		best := 0
		for i := 1; i < len(candidates); i++ {
			if candidates[i].Size > candidates[best].Size {
				best = i
			}
		}
		out = append(out, candidates[best].Price)
		candidates = append(candidates[:best], candidates[best+1:]...)
	}
	return out
}

// microThresholds describe when a book is wide or thin. They are venue-relative
// on purpose: Hyperliquid alt books are structurally thinner and wider than
// Binance majors, so Binance cutoffs would label a normal HL book as unusable.
type microThresholds struct {
	spreadSevereBps   float64
	spreadWideBps     float64
	spreadElevatedBps float64
	depthSevereLow    float64
	depthLow          float64
	depthModerate     float64
}

var (
	binanceMicroThresholds = microThresholds{
		spreadSevereBps: 50, spreadWideBps: 20, spreadElevatedBps: 10,
		depthSevereLow: 50_000, depthLow: 200_000, depthModerate: 1_000_000,
	}
	hyperliquidMicroThresholds = microThresholds{
		spreadSevereBps: 80, spreadWideBps: 40, spreadElevatedBps: 20,
		depthSevereLow: 20_000, depthLow: 80_000, depthModerate: 300_000,
	}
)

func microThresholdsFor(venue Venue) microThresholds {
	if venue == VenueHyperliquid {
		return hyperliquidMicroThresholds
	}
	return binanceMicroThresholds
}

// microLiquidityScore is the Binance-reference form kept for existing callers.
func microLiquidityScore(m *MarketMicrostructure) int {
	return microLiquidityScoreForVenue(m, VenueBinance)
}

// microLiquidityScoreForVenue composites the spread, depth and tilt penalties
// into a 0-100 score: 100 = tight spread, deep book, no extreme tilt.
func microLiquidityScoreForVenue(m *MarketMicrostructure, venue Venue) int {
	thresholds := microThresholdsFor(venue)
	score := 100
	switch {
	case m.SpreadBps > thresholds.spreadSevereBps:
		score -= 40
	case m.SpreadBps > thresholds.spreadWideBps:
		score -= 20
	case m.SpreadBps > thresholds.spreadElevatedBps:
		score -= 10
	}
	depth := m.BidDepthUSD + m.AskDepthUSD
	switch {
	case depth < thresholds.depthSevereLow:
		score -= 40
	case depth < thresholds.depthLow:
		score -= 20
	case depth < thresholds.depthModerate:
		score -= 10
	}
	if math.Abs(m.Imbalance) > 0.6 {
		score -= 10
	}
	if score < 0 {
		score = 0
	}
	return score
}

// vwapFromKlines computes the volume-weighted average price over the trailing
// `bars` bars (typical price = (H+L+C)/3). Returns 0 without usable bars.
func vwapFromKlines(klines []KlineBar, bars int) float64 {
	if len(klines) == 0 || bars <= 0 {
		return 0
	}
	if bars > len(klines) {
		bars = len(klines)
	}
	var pvSum, vSum float64
	for _, k := range klines[len(klines)-bars:] {
		if k.Volume <= 0 {
			continue
		}
		tp := (k.High + k.Low + k.Close) / 3
		pvSum += tp * k.Volume
		vSum += k.Volume
	}
	if vSum <= 0 {
		return 0
	}
	return pvSum / vSum
}
