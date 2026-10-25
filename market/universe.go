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
	"nofx/provider/hyperliquid"
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
	// universeTakerFlowSymbols bounds the taker-flow ranking to the most liquid
	// contracts, because it needs one kline request per symbol.
	universeTakerFlowSymbols = 40
	// universeTakerFlowTTL caches that scan; the ranking does not need a tighter
	// cadence than the rest of the universe.
	universeTakerFlowTTL = 5 * time.Minute
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

// TakerFlowItem is one symbol's aggressor-side share over a window.
type TakerFlowItem struct {
	Rank        int
	Symbol      string
	BuySharePct float64
	QuoteVolume float64
}

// TakerFlowRanking is the market-wide buy/sell imbalance ranking.
type TakerFlowRanking struct {
	Window    string
	Top       []TakerFlowItem
	Low       []TakerFlowItem
	FetchedAt time.Time
}

// UniverseService polls the venue, keeps 24h of snapshots, and derives the
// screener/rankings from them.
type UniverseService struct {
	mu               sync.RWMutex
	pollMu           sync.Mutex
	venue            Venue
	client           *bybit.Client
	binance          *binance.Client
	hyperliquid      *hyperliquid.Client
	coins            []UniverseCoin
	bySymbol         map[string]UniverseCoin
	history          map[string][]universeSample
	lastPoll         time.Time
	lastErr          string
	stopped          bool
	bootstrapRunning bool
	bootstrapDone    bool
	takerMu          sync.Mutex
	takerCache       map[string]*TakerFlowRanking
	takerAt          map[string]time.Time
}

var (
	universeMu      sync.RWMutex
	universeService *UniverseService // reference universe (Bybit-wide)
	universeByVenue = map[Venue]*UniverseService{}
)

func NewUniverseService() *UniverseService {
	return newUniverseService(VenueBinance)
}

// NewHyperliquidUniverseService builds the universe from Hyperliquid's own
// asset contexts: one request per dex covers every listed asset.
func NewHyperliquidUniverseService() *UniverseService {
	return newUniverseService(VenueHyperliquid)
}

func newUniverseService(venue Venue) *UniverseService {
	return &UniverseService{
		venue:       venue,
		client:      bybit.NewClient(),
		binance:     binance.NewClient(),
		hyperliquid: hyperliquid.NewClient(),
		bySymbol:    make(map[string]UniverseCoin),
		history:     make(map[string][]universeSample),
		takerCache:  make(map[string]*TakerFlowRanking),
		takerAt:     make(map[string]time.Time),
	}
}

// StartUniverse creates and starts the process-wide reference universe.
func StartUniverse(ctx context.Context) *UniverseService {
	service := NewUniverseService()
	SetUniverseService(service)
	service.Start(ctx)
	logger.Infof("📊 Reference market universe started (Bybit tickers, refresh every 5 minutes)")
	return service
}

// StartHyperliquidUniverse starts the HL-native universe used by HL traders.
func StartHyperliquidUniverse(ctx context.Context) *UniverseService {
	service := NewHyperliquidUniverseService()
	SetUniverseServiceForVenue(VenueHyperliquid, service)
	service.Start(ctx)
	logger.Infof("📊 Hyperliquid universe started (venue asset contexts, refresh every 5 minutes)")
	return service
}

func SetUniverseService(service *UniverseService) {
	universeMu.Lock()
	universeService = service
	universeMu.Unlock()
}

func SetUniverseServiceForVenue(venue Venue, service *UniverseService) {
	universeMu.Lock()
	universeByVenue[venue] = service
	universeMu.Unlock()
}

// CurrentUniverseForVenue returns the HL-native universe for Hyperliquid and
// the reference universe for everything else.
func CurrentUniverseForVenue(venue Venue) *UniverseService {
	universeMu.RLock()
	defer universeMu.RUnlock()
	if venue == VenueHyperliquid {
		if service := universeByVenue[VenueHyperliquid]; service != nil {
			return service
		}
	}
	return universeService
}

func CurrentUniverse() *UniverseService {
	return CurrentUniverseForVenue(VenueBinance)
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

	if s.venue == VenueHyperliquid {
		s.pollHyperliquid(ctx)
		return
	}

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

// pollHyperliquid builds the universe from the venue's own asset contexts.
// Two requests (default dex + HIP-3 dex) cover every listed asset, including
// stocks, commodities and indices that the reference universe cannot see.
func (s *UniverseService) pollHyperliquid(ctx context.Context) {
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	now := time.Now().UTC()
	coins := make([]UniverseCoin, 0, 256)
	for _, dex := range []string{"", "xyz"} {
		contexts, err := s.hyperliquid.GetAssetContexts(callCtx, dex)
		if err != nil {
			s.note("hyperliquid asset contexts unavailable: " + err.Error())
			continue
		}
		for name, assetCtx := range contexts {
			canonical := Normalize(name)
			if canonical == "" {
				continue
			}
			price := assetCtx.MarkPx
			if price <= 0 {
				price = assetCtx.MidPx
			}
			if price <= 0 {
				price = assetCtx.OraclePx
			}
			if price <= 0 {
				continue
			}
			changePct := 0.0
			if assetCtx.PrevDayPx > 0 {
				changePct = (price - assetCtx.PrevDayPx) / assetCtx.PrevDayPx * 100
			}
			coins = append(coins, UniverseCoin{
				Symbol:       canonical,
				VenueSymbol:  name,
				Price:        price,
				Change24hPct: changePct,
				Turnover24h:  assetCtx.DayNtlVlm,
				OIUSD:        assetCtx.OpenInterest * price,
				At:           now,
			})
		}
	}
	if len(coins) == 0 {
		return
	}
	s.clearNote()
	s.applyCoins(coins, now)
}

// apply turns one tickers response into the current universe and appends a
// history sample. Split from poll so ranking math is testable without network.
func (s *UniverseService) apply(tickers map[string]bybit.Ticker, now time.Time) {
	coins := make([]UniverseCoin, 0, len(tickers))
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
	}
	s.applyCoins(coins, now)
}

// applyCoins scores a snapshot, stores it and appends one history sample per
// symbol. Shared by the reference and Hyperliquid universes.
func (s *UniverseService) applyCoins(coins []UniverseCoin, now time.Time) {
	scoreUniverse(coins)
	samples := make(map[string]universeSample, len(coins))
	for _, coin := range coins {
		samples[coin.Symbol] = universeSample{At: now, Price: coin.Price, OIUSD: coin.OIUSD}
	}

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

// TakerFlowRanking ranks the most liquid contracts by aggressor buy share over
// the window. It needs one hourly kline request per symbol, so it is limited to
// the top of the universe and cached for a full poll interval.
func (s *UniverseService) TakerFlowRanking(window time.Duration, limit int) (*TakerFlowRanking, bool) {
	if s == nil || window <= 0 || window > universeHistoryWindow {
		return nil, false
	}
	// Hyperliquid candles carry base volume but no aggressor split, so this
	// ranking is only meaningful for the reference (Binance/Bybit) universe.
	// HL traders get the per-symbol taker flow from the trades watcher instead.
	if s.venue == VenueHyperliquid {
		return nil, false
	}
	key := window.String()
	now := time.Now()

	s.takerMu.Lock()
	if cached, ok := s.takerCache[key]; ok && now.Sub(s.takerAt[key]) < universeTakerFlowTTL {
		s.takerMu.Unlock()
		return cached, true
	}
	s.takerMu.Unlock()

	coins := s.Screener(universeTakerFlowSymbols)
	hours := int(window / time.Hour)
	if hours <= 0 {
		hours = 1
	}
	if len(coins) < universeMinRankedSymbols {
		return nil, false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	type result struct {
		symbol string
		share  float64
		volume float64
		ok     bool
	}
	results := make(chan result, len(coins))
	var wg sync.WaitGroup
	gate := make(chan struct{}, 4)
	for _, coin := range coins {
		wg.Add(1)
		go func(coin UniverseCoin) {
			defer wg.Done()
			gate <- struct{}{}
			defer func() { <-gate }()

			points, err := s.binance.PriceHistory(ctx, coin.VenueSymbol, "1h", hours)
			if err != nil || len(points) < hours {
				results <- result{}
				return
			}
			var quoteVolume, buyVolume float64
			for _, point := range points[len(points)-hours:] {
				quoteVolume += point.QuoteVolume
				buyVolume += point.TakerBuyQuote
			}
			if quoteVolume <= 0 {
				results <- result{}
				return
			}
			results <- result{
				symbol: coin.Symbol,
				share:  buyVolume / quoteVolume * 100,
				volume: quoteVolume,
				ok:     true,
			}
		}(coin)
	}
	wg.Wait()
	close(results)

	items := make([]TakerFlowItem, 0, len(coins))
	for result := range results {
		if result.ok {
			items = append(items, TakerFlowItem{
				Symbol:      result.symbol,
				BuySharePct: result.share,
				QuoteVolume: result.volume,
			})
		}
	}
	if len(items) < universeMinRankedSymbols {
		return nil, false
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].BuySharePct != items[j].BuySharePct {
			return items[i].BuySharePct > items[j].BuySharePct
		}
		return items[i].Symbol < items[j].Symbol
	})
	if limit <= 0 {
		limit = 10
	}
	top, low := splitTakerFlow(items, limit)

	ranking := &TakerFlowRanking{Window: key, Top: top, Low: low, FetchedAt: now}
	s.takerMu.Lock()
	s.takerCache[key] = ranking
	s.takerAt[key] = now
	s.takerMu.Unlock()
	return ranking, true
}

func splitTakerFlow(items []TakerFlowItem, limit int) (top, low []TakerFlowItem) {
	if len(items) > limit {
		top = append([]TakerFlowItem(nil), items[:limit]...)
		low = append([]TakerFlowItem(nil), items[len(items)-limit:]...)
	} else {
		top = append([]TakerFlowItem(nil), items...)
		low = append([]TakerFlowItem(nil), items...)
	}
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
