package market

import (
	"context"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"nofx/logger"
	"nofx/provider/binance"
	"nofx/provider/bybit"
)

// Local market universe.
//
// One Bybit tickers request covers every linear contract with price, 24h
// turnover, open interest and 24h change. Polling it on a fixed cadence gives,
// without any vendor key: the screener universe, market-wide OI rankings, and
// price rankings. The 5-minute snapshots also provide the short-window deltas;
// a window the process has not observed yet is reported as unavailable rather
// than fabricated.
const (
	universePollInterval  = 5 * time.Minute
	universeHistoryWindow = 24 * time.Hour
	// universeSampleTolerance is how far a baseline sample may sit from the
	// requested window edge. Snapshots are every 5 minutes, so 12 minutes allows
	// for one missed poll without silently shortening the window.
	universeSampleTolerance = 12 * time.Minute
	// universeMinRankedSymbols keeps a two-symbol market from producing a
	// "ranking".
	universeMinRankedSymbols = 5
	// universeBootstrapSymbols is how many of the most liquid contracts have
	// their OI/price history seeded from Binance on startup. Without it, a fresh
	// process would have no 1h window and an oi_top strategy would sit out for an
	// hour after every restart.
	universeBootstrapSymbols = 120
	// universeWindowPairAttempts is how many recent observations are tried when
	// looking for a baseline. One is not enough with mixed hourly and 5-minute
	// cadences; more than a handful would just scan stale history.
	universeWindowPairAttempts = 4
)

// UniverseCoin is one contract's current state.
type UniverseCoin struct {
	Symbol       string
	VenueSymbol  string
	Price        float64
	Change24hPct float64
	Turnover24h  float64
	OIUSD        float64
	// Score is the screener rank: volume, open interest and volatility, each
	// z-scored across the universe. Positive is better.
	Score float64
	At    time.Time
}

type universeSample struct {
	At    time.Time
	Price float64
	OIUSD float64
}

// RankItem is one row of a market-wide ranking.
type RankItem struct {
	Rank        int
	Symbol      string
	Price       float64
	ChangePct   float64
	OIUSD       float64
	OIDeltaPct  float64
	Turnover24h float64
}

// OIRankingData is the market-wide open-interest ranking for one window.
type OIRankingData struct {
	Duration  string
	Top       []RankItem
	Low       []RankItem
	FetchedAt time.Time
}

// PriceRankingDuration holds gainers and losers for one window.
type PriceRankingDuration struct {
	Top []RankItem
	Low []RankItem
}

// PriceRankingData is the market-wide price ranking for several windows.
type PriceRankingData struct {
	Durations map[string]*PriceRankingDuration
	FetchedAt time.Time
}

// UniverseService polls the venue, keeps 24h of snapshots, and derives the
// screener/rankings from them.
type UniverseService struct {
	mu               sync.RWMutex
	pollMu           sync.Mutex
	client           *bybit.Client
	binance          *binance.Client
	coins            []UniverseCoin
	bySymbol         map[string]UniverseCoin
	history          map[string][]universeSample
	lastPoll         time.Time
	lastErr          string
	stopped          bool
	bootstrapRunning bool
	bootstrapDone    bool
}

var (
	universeMu      sync.RWMutex
	universeService *UniverseService
)

func NewUniverseService() *UniverseService {
	return &UniverseService{
		client:   bybit.NewClient(),
		binance:  binance.NewClient(),
		bySymbol: make(map[string]UniverseCoin),
		history:  make(map[string][]universeSample),
	}
}

// StartUniverse creates and starts the process-wide universe poller.
func StartUniverse(ctx context.Context) *UniverseService {
	service := NewUniverseService()
	SetUniverseService(service)
	service.Start(ctx)
	return service
}

func SetUniverseService(service *UniverseService) {
	universeMu.Lock()
	universeService = service
	universeMu.Unlock()
}

func CurrentUniverse() *UniverseService {
	universeMu.RLock()
	defer universeMu.RUnlock()
	return universeService
}

func (s *UniverseService) Start(ctx context.Context) {
	if s == nil {
		return
	}
	go func() {
		s.poll(ctx)
		ticker := time.NewTicker(universePollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.poll(ctx)
			}
		}
	}()
}

func (s *UniverseService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()
}

func (s *UniverseService) stoppedFlag() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stopped
}

// Ensure makes sure a reading exists before a caller consumes it. The first
// analysis cycle otherwise races the poller and would see an empty universe.
func (s *UniverseService) Ensure(ctx context.Context) {
	if s == nil {
		return
	}
	s.mu.RLock()
	stale := len(s.coins) == 0 || time.Since(s.lastPoll) > universePollInterval
	s.mu.RUnlock()
	if stale {
		s.poll(ctx)
	}
}

func (s *UniverseService) poll(ctx context.Context) {
	if s == nil || ctx.Err() != nil || s.stoppedFlag() {
		return
	}
	// Serialize polls: Ensure and the ticker can race on startup.
	s.pollMu.Lock()
	defer s.pollMu.Unlock()

	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	tickers, err := s.client.Tickers(callCtx)
	if err != nil {
		s.note("tickers unavailable: " + err.Error())
		return
	}
	s.clearNote()
	s.apply(tickers, time.Now().UTC())
	// Seed the 1h/4h/24h windows from free public history. Detach from the
	// caller's timeout so a request-scoped Ensure cannot cancel the seed.
	go s.bootstrap(context.WithoutCancel(ctx))
}

// apply turns one tickers response into the current universe and appends a
// history sample. Split from poll so ranking math is testable without network.
func (s *UniverseService) apply(tickers map[string]bybit.Ticker, now time.Time) {
	coins := make([]UniverseCoin, 0, len(tickers))
	samples := make(map[string]universeSample, len(tickers))

	for venueSymbol, ticker := range tickers {
		upper := strings.ToUpper(strings.TrimSpace(venueSymbol))
		if !strings.HasSuffix(upper, "USDT") || len(upper) <= len("USDT") {
			continue
		}
		canonical := Normalize(upper)
		if canonical == "" || strings.Contains(canonical, ":") {
			continue
		}
		price := ticker.MarkPrice
		if price <= 0 {
			price = ticker.LastPrice
		}
		if price <= 0 {
			continue
		}
		coins = append(coins, UniverseCoin{
			Symbol:       canonical,
			VenueSymbol:  upper,
			Price:        price,
			Change24hPct: ticker.Change24hPct,
			Turnover24h:  ticker.Turnover24h,
			OIUSD:        ticker.OpenInterestValue,
			At:           now,
		})
		samples[canonical] = universeSample{At: now, Price: price, OIUSD: ticker.OpenInterestValue}
	}
	scoreUniverse(coins)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.coins = coins
	s.bySymbol = make(map[string]UniverseCoin, len(coins))
	for _, coin := range coins {
		s.bySymbol[coin.Symbol] = coin
	}
	cutoff := now.Add(-universeHistoryWindow)
	for symbol, sample := range samples {
		history := append(s.history[symbol], sample)
		drop := 0
		for drop < len(history) && history[drop].At.Before(cutoff) {
			drop++
		}
		if drop > 0 {
			history = append(history[:0], history[drop:]...)
		}
		s.history[symbol] = history
	}
	// Symbols that delisted no longer have a current reading; drop their history
	// so the map cannot grow for the life of the process.
	for symbol := range s.history {
		if _, live := s.bySymbol[symbol]; !live {
			delete(s.history, symbol)
		}
	}
	s.lastPoll = now
}

// scoreUniverse assigns each coin a deterministic composite score. Every input
// is z-scored across the universe and log-compressed, so a single mega-cap
// cannot flatten the ranking.
func scoreUniverse(coins []UniverseCoin) {
	if len(coins) == 0 {
		return
	}
	logVolume := make([]float64, len(coins))
	logOI := make([]float64, len(coins))
	absChange := make([]float64, len(coins))
	for i, coin := range coins {
		logVolume[i] = math.Log1p(math.Max(0, coin.Turnover24h))
		logOI[i] = math.Log1p(math.Max(0, coin.OIUSD))
		absChange[i] = math.Abs(coin.Change24hPct)
	}
	volMean, volStd := meanStd(logVolume)
	oiMean, oiStd := meanStd(logOI)
	chgMean, chgStd := meanStd(absChange)

	for i := range coins {
		coins[i].Score = 0.45*zScore(logVolume[i], volMean, volStd) +
			0.35*zScore(logOI[i], oiMean, oiStd) +
			0.20*zScore(absChange[i], chgMean, chgStd)
	}
	sort.SliceStable(coins, func(i, j int) bool {
		if coins[i].Score != coins[j].Score {
			return coins[i].Score > coins[j].Score
		}
		if coins[i].Turnover24h != coins[j].Turnover24h {
			return coins[i].Turnover24h > coins[j].Turnover24h
		}
		return coins[i].Symbol < coins[j].Symbol
	})
}

func meanStd(values []float64) (mean, std float64) {
	if len(values) == 0 {
		return 0, 0
	}
	for _, value := range values {
		mean += value
	}
	mean /= float64(len(values))
	for _, value := range values {
		diff := value - mean
		std += diff * diff
	}
	std = math.Sqrt(std / float64(len(values)))
	return mean, std
}

func zScore(value, mean, std float64) float64 {
	if std <= 0 {
		return 0
	}
	return (value - mean) / std
}

// Coins returns the current universe sorted by screener score.
func (s *UniverseService) Coins() []UniverseCoin {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]UniverseCoin(nil), s.coins...)
}

// Screener returns the top-scored coins. The ordering is deterministic for a
// given snapshot, which is what makes candidate selection reproducible.
func (s *UniverseService) Screener(limit int) []UniverseCoin {
	coins := s.Coins()
	if limit > 0 && len(coins) > limit {
		coins = coins[:limit]
	}
	return coins
}

// OIRanking ranks symbols by open-interest change over the window. ok is false
// when the process has not observed the window yet or too few symbols qualify;
// callers then omit the section instead of ranking stale or empty data.
func (s *UniverseService) OIRanking(window time.Duration, limit int) (top, low []RankItem, ok bool) {
	if s == nil || window <= 0 {
		return nil, nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.lastPoll.IsZero() {
		return nil, nil, false
	}

	items := make([]RankItem, 0, len(s.bySymbol))
	for symbol := range s.bySymbol {
		current, then, found := windowPair(s.history[symbol], window)
		if !found || then.OIUSD <= 0 || current.OIUSD <= 0 {
			continue
		}
		items = append(items, RankItem{
			Symbol:      symbol,
			Price:       current.Price,
			ChangePct:   s.bySymbol[symbol].Change24hPct,
			OIUSD:       current.OIUSD,
			OIDeltaPct:  (current.OIUSD - then.OIUSD) / then.OIUSD * 100,
			Turnover24h: s.bySymbol[symbol].Turnover24h,
		})
	}
	if len(items) < universeMinRankedSymbols {
		return nil, nil, false
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].OIDeltaPct != items[j].OIDeltaPct {
			return items[i].OIDeltaPct > items[j].OIDeltaPct
		}
		return items[i].Symbol < items[j].Symbol
	})
	top, low = splitTopLow(items, limit)
	return top, low, true
}

// PriceRanking ranks symbols by price change. The 24h window comes from the
// venue's own ticker; shorter windows come from the local snapshot history and
// are unavailable until that history exists.
func (s *UniverseService) PriceRanking(window time.Duration, limit int) (top, low []RankItem, ok bool) {
	if s == nil || window <= 0 {
		return nil, nil, false
	}

	if window >= 24*time.Hour {
		coins := s.Coins()
		items := make([]RankItem, 0, len(coins))
		for _, coin := range coins {
			items = append(items, RankItem{
				Symbol:      coin.Symbol,
				Price:       coin.Price,
				ChangePct:   coin.Change24hPct,
				OIUSD:       coin.OIUSD,
				Turnover24h: coin.Turnover24h,
			})
		}
		if len(items) < universeMinRankedSymbols {
			return nil, nil, false
		}
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].ChangePct != items[j].ChangePct {
				return items[i].ChangePct > items[j].ChangePct
			}
			return items[i].Symbol < items[j].Symbol
		})
		top, low = splitTopLow(items, limit)
		return top, low, true
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.lastPoll.IsZero() {
		return nil, nil, false
	}
	items := make([]RankItem, 0, len(s.bySymbol))
	for symbol, coin := range s.bySymbol {
		current, then, found := windowPair(s.history[symbol], window)
		if !found || then.Price <= 0 {
			continue
		}
		items = append(items, RankItem{
			Symbol:      symbol,
			Price:       current.Price,
			ChangePct:   (current.Price - then.Price) / then.Price * 100,
			OIUSD:       current.OIUSD,
			Turnover24h: coin.Turnover24h,
		})
	}
	if len(items) < universeMinRankedSymbols {
		return nil, nil, false
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].ChangePct != items[j].ChangePct {
			return items[i].ChangePct > items[j].ChangePct
		}
		return items[i].Symbol < items[j].Symbol
	})
	top, low = splitTopLow(items, limit)
	return top, low, true
}

// bootstrap seeds hourly OI and price history for the most liquid contracts
// from free Binance endpoints. It runs once (retrying on the next poll if every
// request failed) and fills the windows the process has not observed yet.
func (s *UniverseService) bootstrap(ctx context.Context) {
	if s == nil || ctx.Err() != nil {
		return
	}
	s.mu.Lock()
	if s.bootstrapDone || s.bootstrapRunning {
		s.mu.Unlock()
		return
	}
	s.bootstrapRunning = true
	coins := append([]UniverseCoin(nil), s.coins...)
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.bootstrapRunning = false
		s.mu.Unlock()
	}()

	sort.Slice(coins, func(i, j int) bool { return coins[i].Turnover24h > coins[j].Turnover24h })
	if len(coins) > universeBootstrapSymbols {
		coins = coins[:universeBootstrapSymbols]
	}

	type seeded struct {
		symbol  string
		samples []universeSample
	}
	results := make(chan seeded, len(coins))
	var wg sync.WaitGroup
	gate := make(chan struct{}, 4)

	for _, coin := range coins {
		wg.Add(1)
		go func(coin UniverseCoin) {
			defer wg.Done()
			gate <- struct{}{}
			defer func() { <-gate }()

			oi, err := s.binance.OpenInterestHist(ctx, coin.VenueSymbol, "1h", 24)
			if err != nil || len(oi) == 0 {
				return
			}
			prices, err := s.binance.PriceHistory(ctx, coin.VenueSymbol, "1h", 24)
			if err != nil {
				return
			}
			priceByTime := make(map[int64]float64, len(prices))
			for _, point := range prices {
				priceByTime[point.TimestampMs] = point.Close
			}

			samples := make([]universeSample, 0, len(oi))
			for _, point := range oi {
				price := priceByTime[point.TimestampMs]
				if price <= 0 {
					price = coin.Price
				}
				if point.OIUSD <= 0 || price <= 0 {
					continue
				}
				samples = append(samples, universeSample{
					At:    time.UnixMilli(point.TimestampMs).UTC(),
					Price: price,
					OIUSD: point.OIUSD,
				})
			}
			if len(samples) > 0 {
				results <- seeded{symbol: coin.Symbol, samples: samples}
			}
		}(coin)
	}
	wg.Wait()
	close(results)

	now := time.Now().UTC()
	merged := 0
	s.mu.Lock()
	for result := range results {
		s.history[result.symbol] = mergeUniverseSamples(s.history[result.symbol], result.samples, now)
		merged++
	}
	if merged > 0 {
		s.bootstrapDone = true
	}
	s.mu.Unlock()

	if merged > 0 {
		logger.Infof("📚 Market universe seeded hourly OI/price history for %d symbols", merged)
	}
}

func mergeUniverseSamples(existing, extra []universeSample, now time.Time) []universeSample {
	byTime := make(map[int64]universeSample, len(existing)+len(extra))
	for _, sample := range existing {
		byTime[sample.At.UnixMilli()] = sample
	}
	for _, sample := range extra {
		if _, ok := byTime[sample.At.UnixMilli()]; !ok {
			byTime[sample.At.UnixMilli()] = sample
		}
	}
	cutoff := now.Add(-universeHistoryWindow)
	merged := make([]universeSample, 0, len(byTime))
	for _, sample := range byTime {
		if !sample.At.Before(cutoff) {
			merged = append(merged, sample)
		}
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].At.Before(merged[j].At) })
	return merged
}

// windowPair finds the newest observation that has a valid baseline exactly one
// window earlier. Scanning a few candidates matters because the history mixes
// cadences: hourly bootstrap samples plus 5-minute live ones. The newest live
// sample often has no baseline within tolerance, while the hourly sample just
// before it does.
func windowPair(history []universeSample, window time.Duration) (current, baseline universeSample, ok bool) {
	attempts := 0
	for i := len(history) - 1; i >= 0 && attempts < universeWindowPairAttempts; i-- {
		candidate := history[i]
		if then, found := sampleAt(history[:i], candidate.At.Add(-window)); found {
			return candidate, then, true
		}
		attempts++
	}
	return universeSample{}, universeSample{}, false
}

// sampleAt returns the newest sample at or before target, within tolerance.
func sampleAt(history []universeSample, target time.Time) (universeSample, bool) {
	best := universeSample{}
	found := false
	for _, sample := range history {
		if sample.At.After(target) {
			continue
		}
		if !found || sample.At.After(best.At) {
			best = sample
			found = true
		}
	}
	if !found || target.Sub(best.At) > universeSampleTolerance {
		return universeSample{}, false
	}
	return best, true
}

func splitTopLow(items []RankItem, limit int) (top, low []RankItem) {
	if limit <= 0 {
		limit = 10
	}
	if len(items) > limit {
		top = append([]RankItem(nil), items[:limit]...)
		low = append([]RankItem(nil), items[len(items)-limit:]...)
	} else {
		top = append([]RankItem(nil), items...)
		low = append([]RankItem(nil), items...)
	}
	// Losers read worst first.
	for i, j := 0, len(low)-1; i < j; i, j = i+1, j-1 {
		low[i], low[j] = low[j], low[i]
	}
	for i := range top {
		top[i].Rank = i + 1
	}
	for i := range low {
		low[i].Rank = i + 1
	}
	return top, low
}

func (s *UniverseService) note(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastErr == message {
		return
	}
	s.lastErr = message
	logger.Warnf("⚠️ Market universe: %s", message)
}

func (s *UniverseService) clearNote() {
	s.mu.Lock()
	s.lastErr = ""
	s.mu.Unlock()
}
