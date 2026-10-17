package market

import (
	"context"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"nofx/logger"
	"nofx/provider/hyperliquid"
)

// Hyperliquid publishes no account-level long/short ratio, so positioning has
// to be derived from what the venue does expose (verified against the full info
// API surface): the hourly funding rate it charges on the long/short imbalance,
// the per-asset open interest and premium in `metaAndAssetCtxs`, and the
// aggressor side of every trade on the public trades feed. This file holds the
// derivations; nothing here performs I/O so all of it is unit-testable.
//
// Everything is omission-first: a value that cannot be derived from enough
// samples is reported as unavailable and never rendered as zero, because a
// fabricated zero reads to the model as "no positioning" rather than "unknown".

const (
	// fundingHistoryWindow is the baseline period for the funding percentile.
	fundingHistoryWindow = 7 * 24 * time.Hour
	// fundingHistoryTTL caches the baseline per coin. Funding is charged hourly,
	// so refreshing every 30 minutes is plenty and keeps the request count at
	// roughly one per coin per half hour.
	fundingHistoryTTL = 30 * time.Minute
	// fundingMinSamples is the minimum number of hourly observations required
	// before a percentile is meaningful (~3 days of hourly funding).
	fundingMinSamples = 72
	// oiRetention bounds the in-memory open-interest history.
	oiRetention = 7 * 24 * time.Hour
	// flowRetention bounds the taker-flow window per symbol.
	flowRetention = 6 * time.Hour
	// flowMaxPrintsPerSymbol caps memory for very active markets: a single
	// symbol can print thousands of trades per hour.
	flowMaxPrintsPerSymbol = 20000
	// flowMinSamples is the minimum number of prints before a buy/sell ratio is
	// reported (~30 prints in the window).
	flowMinSamples = 30
)

// Positioning is the Hyperliquid-native positioning reading for one symbol.
// Fields are only meaningful when their OK flag (or parent) is set.
type Positioning struct {
	Funding    FundingReading
	OIUSD      float64
	OI         []OIReading
	PremiumPct float64
	TakerFlow  *TakerFlowReading
}

// FundingReading is the current funding rate with its baseline position.
type FundingReading struct {
	RateHourlyPct float64
	AnnualPct     float64
	// Percentile is 0-100: where the current hourly funding sits in the coin's
	// own recent history. High means longs are paying a lot relative to normal.
	Percentile   float64
	PercentileOK bool
	Samples      int
}

// OIReading is open interest relative to its own recent history, together with
// the price move over the same window.
type OIReading struct {
	Window         time.Duration
	OIChangePct    float64
	PriceChangePct float64
	// Quadrant is a language-neutral enum: "new_longs", "new_shorts",
	// "short_covering", "long_capitulation" or "" when unknown.
	Quadrant string
}

// TakerFlowReading is the aggressor-side flow ratio: how much notional was
// bought by market takers versus sold.
type TakerFlowReading struct {
	Window  time.Duration
	BuyUSD  float64
	SellUSD float64
	Ratio   float64 // BuyUSD / SellUSD; 0 when SellUSD is 0
	Samples int
}

// Quadrant enums for OI x price.
const (
	QuadrantNewLongs         = "new_longs"
	QuadrantNewShorts        = "new_shorts"
	QuadrantShortCovering    = "short_covering"
	QuadrantLongCapitulation = "long_capitulation"
)

// AnnualizedFundingPct converts an hourly funding rate (fraction) to an
// annualized percentage, the form traders compare across venues.
func AnnualizedFundingPct(hourly float64) float64 {
	return hourly * 24 * 365 * 100
}

// FundingPercentile returns where `current` sits within `points` (0-100, the
// share of observations at or below it). ok is false when fewer than
// minSamples observations are available: a percentile over a handful of points
// would look authoritative while being noise.
func FundingPercentile(points []hyperliquid.FundingPoint, current float64, minSamples int) (pct float64, samples int, ok bool) {
	if len(points) < minSamples {
		return 0, len(points), false
	}

	rates := make([]float64, 0, len(points))
	for _, p := range points {
		if math.IsNaN(p.Rate) || math.IsInf(p.Rate, 0) {
			continue
		}
		rates = append(rates, p.Rate)
	}
	if len(rates) < minSamples {
		return 0, len(rates), false
	}

	sort.Float64s(rates)
	below := sort.SearchFloat64s(rates, current)
	// Count exact matches too (SearchFloat64s returns the first index >= current).
	atOrBelow := below
	for atOrBelow < len(rates) && rates[atOrBelow] == current {
		atOrBelow++
	}
	return float64(atOrBelow) / float64(len(rates)) * 100, len(rates), true
}

// OIQuadrant classifies open-interest and price changes over the same window.
// This is the standard positioning read: it separates new money entering from
// positions being closed, which a bare open-interest number cannot do.
func OIQuadrant(oiChangePct, priceChangePct float64) string {
	switch {
	case oiChangePct > 0 && priceChangePct > 0:
		return QuadrantNewLongs
	case oiChangePct > 0 && priceChangePct < 0:
		return QuadrantNewShorts
	case oiChangePct < 0 && priceChangePct > 0:
		return QuadrantShortCovering
	case oiChangePct < 0 && priceChangePct < 0:
		return QuadrantLongCapitulation
	default:
		return ""
	}
}

// TakerFlowRatio returns buy/sell notional ratio, or ok=false when the window
// holds too few prints to mean anything.
func TakerFlowRatio(buyUSD, sellUSD float64, samples, minSamples int) (ratio float64, ok bool) {
	if samples < minSamples {
		return 0, false
	}
	if sellUSD <= 0 {
		if buyUSD <= 0 {
			return 0, false
		}
		return math.Inf(1), true
	}
	return buyUSD / sellUSD, true
}

// ---------------------------------------------------------------------------
// In-memory histories
// ---------------------------------------------------------------------------

// OISample is one open-interest observation.
type OISample struct {
	TimeMs int64
	OIUSD  float64
	Price  float64
}

// OISeries keeps a per-symbol open-interest history in memory. The analysis
// cycle samples it, so the resolution equals the decision cadence (one hour at
// a 60-minute scan). History is process-local: after a restart the earlier
// windows report unavailable until enough samples accumulate, rather than
// silently comparing against nothing.
type OISeries struct {
	mu           sync.Mutex
	bySymbol     map[string][]OISample
	retention    time.Duration
	lastRecorded map[string]int64
}

func NewOISeries() *OISeries {
	return &OISeries{
		bySymbol:     make(map[string][]OISample),
		retention:    oiRetention,
		lastRecorded: make(map[string]int64),
	}
}

// Record appends one observation. Observations closer together than half the
// shortest reported window are skipped, so a burst of data assembly within one
// cycle cannot crowd out the real cadence of the series.
func (s *OISeries) Record(symbol string, tsMs int64, oiUSD, price float64) {
	if symbol == "" || oiUSD <= 0 || price <= 0 || tsMs <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// Two windows are reported (1h, 4h); 30 minutes is the shortest useful
	// spacing, so anything denser is noise for both.
	const minSpacingMs = int64(30 * time.Minute / time.Millisecond)
	if last := s.lastRecorded[symbol]; last > 0 && tsMs-last < minSpacingMs {
		return
	}
	s.lastRecorded[symbol] = tsMs

	series := append(s.bySymbol[symbol], OISample{TimeMs: tsMs, OIUSD: oiUSD, Price: price})
	cutoff := tsMs - int64(s.retention/time.Millisecond)
	drop := 0
	for drop < len(series) && series[drop].TimeMs < cutoff {
		drop++
	}
	if drop > 0 {
		series = series[drop:]
	}
	s.bySymbol[symbol] = series
}

// Change reports the open-interest and price change over the window, using the
// oldest sample inside the window as the baseline. ok is false when the span
// covered is less than half the window (fresh process, sparse samples), so a
// "1h change" is never reported from a few minutes of data.
func (s *OISeries) Change(symbol string, window time.Duration, nowMs int64) (oiChangePct, priceChangePct float64, ok bool) {
	if window <= 0 || nowMs <= 0 {
		return 0, 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	series := s.bySymbol[symbol]
	if len(series) < 2 {
		return 0, 0, false
	}

	windowMs := int64(window / time.Millisecond)
	cutoff := nowMs - windowMs

	baselineIdx := -1
	for i, sample := range series {
		if sample.TimeMs >= cutoff {
			baselineIdx = i
			break
		}
	}
	if baselineIdx < 0 || baselineIdx >= len(series)-1 {
		return 0, 0, false
	}

	baseline := series[baselineIdx]
	latest := series[len(series)-1]
	spanMs := latest.TimeMs - baseline.TimeMs
	if spanMs < windowMs/2 {
		return 0, 0, false
	}
	if baseline.OIUSD <= 0 || baseline.Price <= 0 {
		return 0, 0, false
	}
	return (latest.OIUSD/baseline.OIUSD - 1) * 100,
		(latest.Price/baseline.Price - 1) * 100, true
}

// FlowPrint is one executed trade attributed to its aggressor side.
type FlowPrint struct {
	TimeMs      int64
	NotionalUSD float64
	IsBuy       bool
}

// FlowWindow accumulates aggressor-side flow per symbol from the trades feed.
// Callbacks arrive concurrently, hence the mutex.
type FlowWindow struct {
	mu         sync.Mutex
	bySymbol   map[string][]FlowPrint
	retention  time.Duration
	minSamples int
}

func NewFlowWindow(minSamples int) *FlowWindow {
	if minSamples <= 0 {
		minSamples = flowMinSamples
	}
	return &FlowWindow{
		bySymbol:   make(map[string][]FlowPrint),
		retention:  flowRetention,
		minSamples: minSamples,
	}
}

// AddTaker records a trade print. notionalUSD is price x size.
func (f *FlowWindow) AddTaker(symbol string, tsMs int64, isBuy bool, notionalUSD float64) {
	if symbol == "" || tsMs <= 0 || notionalUSD <= 0 {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	prints := append(f.bySymbol[symbol], FlowPrint{TimeMs: tsMs, NotionalUSD: notionalUSD, IsBuy: isBuy})
	cutoff := tsMs - int64(f.retention/time.Millisecond)
	drop := 0
	for drop < len(prints) && prints[drop].TimeMs < cutoff {
		drop++
	}
	if drop > 0 {
		prints = prints[drop:]
	}
	if len(prints) > flowMaxPrintsPerSymbol {
		prints = prints[len(prints)-flowMaxPrintsPerSymbol:]
	}
	f.bySymbol[symbol] = prints
}

// Reading sums the aggressor-side flow inside the window. ok is false when too
// few prints landed in the window (a quiet symbol, or a feed that just
// started): the caller omits the line instead of reporting a ratio built from
// almost no data.
func (f *FlowWindow) Reading(symbol string, window time.Duration, nowMs int64) (TakerFlowReading, bool) {
	if window <= 0 || nowMs <= 0 {
		return TakerFlowReading{}, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	cutoff := nowMs - int64(window/time.Millisecond)
	buy, sell := 0.0, 0.0
	samples := 0
	for _, p := range f.bySymbol[symbol] {
		if p.TimeMs < cutoff {
			continue
		}
		samples++
		if p.IsBuy {
			buy += p.NotionalUSD
		} else {
			sell += p.NotionalUSD
		}
	}

	ratio, ok := TakerFlowRatio(buy, sell, samples, f.minSamples)
	if !ok {
		return TakerFlowReading{}, false
	}
	return TakerFlowReading{Window: window, BuyUSD: buy, SellUSD: sell, Ratio: ratio, Samples: samples}, true
}

// LastPrintAge reports how long ago the most recent print for a symbol arrived,
// used for feed health reporting (0 when there is no print at all). The newest
// print is found by timestamp rather than by slice position: the feed normally
// arrives in order, but a reconnect or an out-of-order burst must not make a
// live feed look stale.
func (f *FlowWindow) LastPrintAge(symbol string, nowMs int64) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()

	prints := f.bySymbol[symbol]
	if len(prints) == 0 {
		return 0
	}
	newest := prints[0].TimeMs
	for _, p := range prints[1:] {
		if p.TimeMs > newest {
			newest = p.TimeMs
		}
	}
	if nowMs <= newest {
		return 0
	}
	return time.Duration(nowMs-newest) * time.Millisecond
}

// ---------------------------------------------------------------------------
// Process-level state and assembly
// ---------------------------------------------------------------------------

// oiWindows are the lookbacks reported to the model. They match the timeframes
// of the short-horizon strategy (hours to a few days).
var oiWindows = []time.Duration{time.Hour, 4 * time.Hour}

var hlPositioning = struct {
	oi   *OISeries
	flow *FlowWindow
}{
	oi:   NewOISeries(),
	flow: NewFlowWindow(flowMinSamples),
}

// HLFlowWindow exposes the shared taker-flow accumulator to the trades watcher
// that feeds it and to the tests.
func HLFlowWindow() *FlowWindow { return hlPositioning.flow }

// HLOISeries exposes the shared open-interest history.
func HLOISeries() *OISeries { return hlPositioning.oi }

var fundingHistoryCache = struct {
	mu      sync.Mutex
	byCoin  map[string][]hyperliquid.FundingPoint
	fetched map[string]time.Time
	// attempts caches failures too, so an unavailable venue is not retried on
	// every symbol on every cycle.
	attempts map[string]bool
}{
	byCoin:   make(map[string][]hyperliquid.FundingPoint),
	fetched:  make(map[string]time.Time),
	attempts: make(map[string]bool),
}

// fundingHistory returns the cached hourly funding baseline for a venue coin
// name, fetching it at most once per TTL. Failures are cached for the same TTL:
// a venue outage must not turn into one request per symbol per cycle.
func fundingHistory(coin string) []hyperliquid.FundingPoint {
	if coin == "" {
		return nil
	}
	now := time.Now()

	fundingHistoryCache.mu.Lock()
	if fetchedAt, ok := fundingHistoryCache.fetched[coin]; ok && now.Sub(fetchedAt) < fundingHistoryTTL {
		points := fundingHistoryCache.byCoin[coin]
		fundingHistoryCache.mu.Unlock()
		return points
	}
	fundingHistoryCache.fetched[coin] = now
	fundingHistoryCache.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := hyperliquid.NewClient()
	points, err := client.GetFundingHistory(ctx, coin, now.Add(-fundingHistoryWindow).UnixMilli())
	if err != nil {
		fundingHistoryCache.mu.Lock()
		fundingHistoryCache.byCoin[coin] = nil
		if _, warned := fundingHistoryCache.attempts[coin]; !warned {
			fundingHistoryCache.attempts[coin] = true
			logger.Infof("⚠️ Hyperliquid funding history unavailable for %s (percentile omitted): %v", coin, err)
		}
		fundingHistoryCache.mu.Unlock()
		return nil
	}

	fundingHistoryCache.mu.Lock()
	fundingHistoryCache.byCoin[coin] = points
	fundingHistoryCache.mu.Unlock()
	return points
}

// HyperliquidPositioning assembles the positioning reading for a symbol from
// the venue context plus the in-memory histories. ok is false when the venue
// does not list the symbol or nothing can be derived yet.
func HyperliquidPositioning(symbol string) (*Positioning, bool) {
	ctx, ok := hyperliquidAssetCtx(symbol)
	if !ok {
		return nil, false
	}

	normalized := Normalize(symbol)
	nowMs := time.Now().UnixMilli()

	// The context is also the sampling point for the open-interest history, so
	// the series advances once per analysis cycle.
	oiUSD := ctx.OpenInterest * ctx.MarkPx
	if oiUSD <= 0 {
		oiUSD = ctx.OpenInterest * ctx.MidPx
	}
	hlPositioning.oi.Record(normalized, nowMs, oiUSD, ctx.MarkPx)

	reading := &Positioning{
		Funding: FundingReading{
			RateHourlyPct: ctx.Funding * 100,
			AnnualPct:     AnnualizedFundingPct(ctx.Funding),
		},
		OIUSD:      oiUSD,
		PremiumPct: ctx.Premium * 100,
	}

	points := fundingHistory(venueCoinName(symbol))
	percentile, samples, percentileOK := FundingPercentile(points, ctx.Funding, fundingMinSamples)
	reading.Funding.Percentile = percentile
	reading.Funding.PercentileOK = percentileOK
	reading.Funding.Samples = samples

	for _, window := range oiWindows {
		oiChange, priceChange, ok := hlPositioning.oi.Change(normalized, window, nowMs)
		if !ok {
			continue
		}
		reading.OI = append(reading.OI, OIReading{
			Window:         window,
			OIChangePct:    oiChange,
			PriceChangePct: priceChange,
			Quadrant:       OIQuadrant(oiChange, priceChange),
		})
	}

	if flow, ok := hlPositioning.flow.Reading(normalized, time.Hour, nowMs); ok {
		reading.TakerFlow = &flow
	}

	// Nothing derivable yet (fresh process, venue context without OI): report
	// unavailable rather than an empty line.
	if reading.OIUSD <= 0 && !reading.Funding.PercentileOK && len(reading.OI) == 0 && reading.TakerFlow == nil {
		return nil, false
	}
	return reading, true
}

// venueCoinName returns the name the venue uses for a symbol, which is what the
// funding-history request expects: crypto symbols are stored as "BTCUSDT" while
// the venue calls the coin "BTC", and HIP-3 assets already carry the venue's
// "xyz:" prefix.
func venueCoinName(symbol string) string {
	trimmed := strings.TrimSpace(symbol)
	if trimmed == "" {
		return ""
	}
	upper := strings.ToUpper(trimmed)
	if strings.Contains(upper, "XYZ:") {
		return trimmed
	}
	for _, quote := range []string{"USDT", "USDC", "USD"} {
		if idx := strings.LastIndex(upper, quote); idx > 0 && idx+len(quote) == len(upper) {
			return trimmed[:idx]
		}
	}
	return trimmed
}
