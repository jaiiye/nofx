package market

import (
	"context"
	"strings"
	"sync"
	"time"

	"nofx/logger"
	"nofx/provider/binance"
	"nofx/provider/bybit"
	"nofx/provider/hyperliquid"
)

const (
	// venueMetricsPollInterval is how often the venues are read. One Binance
	// premium-index call covers the whole market, one Bybit tickers call covers
	// the whole market, and Hyperliquid is served from its existing 60s asset
	// cache, so a poll costs roughly one request per symbol for Binance OI.
	venueMetricsPollInterval = 60 * time.Second
	// venueMetricsPersistEvery persists every Nth poll. The in-memory reading is
	// refreshed every minute; the stored series is 5-minute resolution, which is
	// enough for replay and keeps SQLite write volume bounded.
	venueMetricsPersistEvery = 5
	// venueMetricsStaleAfter is how old an in-memory reading may be before the
	// prompt treats it as absent rather than showing an aged number.
	venueMetricsStaleAfter = 3 * venueMetricsPollInterval
	// venueMetricsRequestTimeout bounds one venue call inside a poll.
	venueMetricsRequestTimeout = 5 * time.Second
	// venueMetricsRetentionDays is the stored history window.
	venueMetricsRetentionDays = 30
)

// VenueMetricsSnapshot is one persisted batch of raw readings.
type VenueMetricsSnapshot struct {
	Timestamp time.Time
	Quotes    []VenueQuote
}

// VenueMetricsSink persists one batch. It is a function rather than an interface
// over a store so this package stays free of database types.
type VenueMetricsSink func(*VenueMetricsSnapshot) error

// VenueMetricsCollector polls the three venues for the symbols the analysis
// cycle is looking at, keeps the newest cross-venue reading in memory for the
// prompt, and hands a batch to its sink every few polls.
type VenueMetricsCollector struct {
	mu            sync.RWMutex
	sink          VenueMetricsSink
	cleanup       func(days int) (int64, error)
	symbols       map[string]bool
	latest        map[string]*CrossVenue
	instruments   map[string]bybit.Instrument
	instrumentsAt time.Time
	// instrumentsRetryAt throttles retries after a failed instruments lookup.
	// Without it a venue outage would either hammer the endpoint every poll or,
	// with only the 6h success TTL, leave every Bybit funding rate on the 8h
	// assumption for hours.
	instrumentsRetryAt time.Time
	polledAt           time.Time
	lastErr            map[string]string
	lastCleanup        time.Time
	tick               int
	stopped            bool

	binance *binance.Client
	bybit   *bybit.Client
}

var (
	venueMetricsMu        sync.RWMutex
	venueMetricsCollector *VenueMetricsCollector
)

func NewVenueMetricsCollector(sink VenueMetricsSink) *VenueMetricsCollector {
	return &VenueMetricsCollector{
		sink:        sink,
		symbols:     make(map[string]bool),
		latest:      make(map[string]*CrossVenue),
		instruments: make(map[string]bybit.Instrument),
		lastErr:     make(map[string]string),
		binance:     binance.NewClient(),
		bybit:       bybit.NewClient(),
	}
}

// SetCleanup installs the retention job, called at most once a day.
func (c *VenueMetricsCollector) SetCleanup(cleanup func(days int) (int64, error)) {
	c.mu.Lock()
	c.cleanup = cleanup
	c.mu.Unlock()
}

// SetVenueMetricsCollector registers the process-wide collector so the prompt
// path can read it without threading a handle through every call site.
func SetVenueMetricsCollector(collector *VenueMetricsCollector) {
	venueMetricsMu.Lock()
	venueMetricsCollector = collector
	venueMetricsMu.Unlock()
}

func currentVenueMetricsCollector() *VenueMetricsCollector {
	venueMetricsMu.RLock()
	defer venueMetricsMu.RUnlock()
	return venueMetricsCollector
}

// UpdateVenueMetricSymbols steers polling to the symbols being analysed. Only
// the difference is applied, so this is cheap to call every cycle.
func UpdateVenueMetricSymbols(symbols []string) {
	if collector := currentVenueMetricsCollector(); collector != nil {
		collector.SetSymbols(symbols)
	}
}

// VenueMetricsForSymbol returns the newest cross-venue reading for a symbol.
// A missing or aged reading reports false, which callers render as "omitted".
func VenueMetricsForSymbol(symbol string) (*CrossVenue, bool) {
	collector := currentVenueMetricsCollector()
	if collector == nil {
		return nil, false
	}
	return collector.Snapshot(symbol)
}

// VenueMetricsCoverage reports how many symbols have a reading and how old the
// newest poll is, for health logs.
func VenueMetricsCoverage() (int, time.Duration) {
	collector := currentVenueMetricsCollector()
	if collector == nil {
		return 0, 0
	}
	return collector.Coverage()
}

// SetSymbols replaces the polling set with normalized symbol names.
func (c *VenueMetricsCollector) SetSymbols(symbols []string) {
	next := make(map[string]bool, len(symbols))
	for _, symbol := range symbols {
		if canonical := Normalize(symbol); canonical != "" {
			next[canonical] = true
		}
	}

	c.mu.Lock()
	c.symbols = next
	// Drop readings for symbols that are no longer analysed. Unlike the
	// liquidation windows these carry no history, so they can go immediately.
	for symbol := range c.latest {
		if !next[symbol] {
			delete(c.latest, symbol)
		}
	}
	c.mu.Unlock()
}

// Snapshot returns the newest reading for a symbol when it is still fresh.
func (c *VenueMetricsCollector) Snapshot(symbol string) (*CrossVenue, bool) {
	canonical := Normalize(symbol)
	c.mu.RLock()
	reading, ok := c.latest[canonical]
	c.mu.RUnlock()
	if !ok || reading == nil {
		return nil, false
	}
	if time.Since(time.UnixMilli(reading.ComputedAtMs)) > venueMetricsStaleAfter {
		return nil, false
	}
	return reading, true
}

// Coverage reports symbol coverage and the age of the last poll.
func (c *VenueMetricsCollector) Coverage() (int, time.Duration) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	age := time.Duration(0)
	if !c.polledAt.IsZero() {
		age = time.Since(c.polledAt)
	}
	return len(c.latest), age
}

// Start runs the polling loop until ctx ends. It never fails the caller: a
// venue that is unreachable simply contributes no quotes.
func (c *VenueMetricsCollector) Start(ctx context.Context) {
	if c == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(venueMetricsPollInterval)
		defer ticker.Stop()

		c.poll(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.poll(ctx)
			}
		}
	}()
}

// Stop makes the next poll a no-op. The loop exits on ctx cancellation; this
// exists for symmetry with the other collectors and for callers that stop
// before cancelling the context.
func (c *VenueMetricsCollector) Stop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.stopped = true
	c.mu.Unlock()
}

func (c *VenueMetricsCollector) shouldStop() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.stopped
}

func (c *VenueMetricsCollector) poll(ctx context.Context) {
	if c == nil || ctx.Err() != nil || c.shouldStop() {
		return
	}

	c.mu.Lock()
	symbols := make([]string, 0, len(c.symbols))
	for symbol := range c.symbols {
		symbols = append(symbols, symbol)
	}
	c.tick++
	tick := c.tick
	c.mu.Unlock()

	if len(symbols) == 0 {
		return
	}

	now := time.Now().UTC()

	binancePremiums, binanceErr := c.fetchBinancePremiums(ctx, symbols)
	if binanceErr != nil {
		c.note("binance", "premium index unavailable: "+binanceErr.Error())
	} else {
		c.clearNote("binance")
	}

	bybitTickers, bybitErr := c.fetchBybitTickers(ctx)
	if bybitErr != nil {
		c.note("bybit", "tickers unavailable: "+bybitErr.Error())
	} else {
		c.clearNote("bybit")
	}
	c.refreshBybitInstruments(ctx)

	binanceOI := c.fetchBinanceOpenInterest(ctx, symbols, binancePremiums)

	quotes := make([]VenueQuote, 0, len(symbols)*3)
	for _, symbol := range symbols {
		quotes = append(quotes, c.quotesForSymbol(symbol, now, binancePremiums, binanceOI, bybitTickers)...)
	}

	c.mu.Lock()
	c.polledAt = now
	bySymbol := make(map[string][]VenueQuote, len(symbols))
	for _, quote := range quotes {
		canonical := Normalize(quote.Symbol)
		bySymbol[canonical] = append(bySymbol[canonical], quote)
	}
	for canonical, symbolQuotes := range bySymbol {
		if reading := BuildCrossVenue(canonical, symbolQuotes, now.UnixMilli()); reading != nil {
			c.latest[canonical] = reading
		}
	}
	sink := c.sink
	cleanup := c.cleanup
	lastCleanup := c.lastCleanup
	c.mu.Unlock()

	if sink != nil && tick%venueMetricsPersistEvery == 0 {
		if err := sink(&VenueMetricsSnapshot{Timestamp: now, Quotes: quotes}); err != nil {
			c.note("store", "persist failed: "+err.Error())
		}
	}

	if cleanup != nil && (lastCleanup.IsZero() || time.Since(lastCleanup) >= 24*time.Hour) {
		if _, err := cleanup(venueMetricsRetentionDays); err != nil {
			c.note("cleanup", "retention failed: "+err.Error())
		} else {
			c.mu.Lock()
			c.lastCleanup = time.Now()
			c.mu.Unlock()
		}
	}
}

func (c *VenueMetricsCollector) fetchBinancePremiums(ctx context.Context, symbols []string) (map[string]binance.PremiumIndex, error) {
	if len(cryptoVenueSymbols(symbols)) == 0 {
		return nil, nil
	}
	callCtx, cancel := context.WithTimeout(ctx, 2*venueMetricsRequestTimeout)
	defer cancel()
	return c.binance.PremiumIndexAll(callCtx)
}

func (c *VenueMetricsCollector) fetchBybitTickers(ctx context.Context) (map[string]bybit.Ticker, error) {
	callCtx, cancel := context.WithTimeout(ctx, 2*venueMetricsRequestTimeout)
	defer cancel()
	return c.bybit.Tickers(callCtx)
}

// refreshBybitInstruments learns the settlement interval per contract. It is a
// startup/daily cost, not a per-poll one. A failed lookup retries after ten
// minutes rather than at the next poll (too aggressive) or after the six-hour
// success TTL (too slow: every Bybit rate would sit on the 8h assumption).
func (c *VenueMetricsCollector) refreshBybitInstruments(ctx context.Context) {
	const retryAfterFailure = 10 * time.Minute

	c.mu.RLock()
	lastRefresh := c.instrumentsAt
	known := len(c.instruments)
	retryAt := c.instrumentsRetryAt
	c.mu.RUnlock()

	if !retryAt.IsZero() && time.Now().Before(retryAt) {
		return
	}
	if known > 0 && !lastRefresh.IsZero() && time.Since(lastRefresh) < 6*time.Hour {
		return
	}

	callCtx, cancel := context.WithTimeout(ctx, 5*venueMetricsRequestTimeout)
	defer cancel()
	instruments, err := c.bybit.InstrumentsInfo(callCtx)
	if err != nil || len(instruments) == 0 {
		if err != nil {
			c.note("bybit-instruments", "funding intervals unavailable, assuming 8h: "+err.Error())
		}
		c.mu.Lock()
		c.instrumentsRetryAt = time.Now().Add(retryAfterFailure)
		c.mu.Unlock()
		return
	}
	c.clearNote("bybit-instruments")
	c.mu.Lock()
	c.instruments = instruments
	c.instrumentsAt = time.Now()
	c.instrumentsRetryAt = time.Time{}
	c.mu.Unlock()
}

// fetchBinanceOpenInterest reads OI per symbol. Binance has no all-symbol OI
// endpoint, so this is the only fan-out in the poll; it is bounded and failures
// degrade to "no OI for that symbol" rather than dropping the venue.
func (c *VenueMetricsCollector) fetchBinanceOpenInterest(ctx context.Context, symbols []string, premiums map[string]binance.PremiumIndex) map[string]float64 {
	crypto := cryptoVenueSymbols(symbols)
	result := make(map[string]float64, len(crypto))
	if len(crypto) == 0 {
		return result
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	gate := make(chan struct{}, 4)
	for _, symbol := range crypto {
		if premiums != nil {
			if _, listed := premiums[symbol]; !listed {
				continue
			}
		}
		wg.Add(1)
		go func(symbol string) {
			defer wg.Done()
			gate <- struct{}{}
			defer func() { <-gate }()

			callCtx, cancel := context.WithTimeout(ctx, venueMetricsRequestTimeout)
			defer cancel()
			oi, err := c.binance.OpenInterest(callCtx, symbol)
			if err != nil || oi == nil {
				return
			}
			mu.Lock()
			result[symbol] = oi.OpenInterest
			mu.Unlock()
		}(symbol)
	}
	wg.Wait()
	return result
}

func (c *VenueMetricsCollector) quotesForSymbol(
	symbol string,
	now time.Time,
	binancePremiums map[string]binance.PremiumIndex,
	binanceOI map[string]float64,
	bybitTickers map[string]bybit.Ticker,
) []VenueQuote {
	canonical, isXYZ, binanceSymbol, bybitSymbol, hlCoin := venueSymbols(symbol)
	if canonical == "" {
		return nil
	}

	quotes := make([]VenueQuote, 0, 3)

	if !isXYZ {
		if premium, ok := binancePremiums[binanceSymbol]; ok {
			oiBase := binanceOI[binanceSymbol]
			quotes = append(quotes, VenueQuote{
				Symbol:           canonical,
				Venue:            "binance",
				VenueSymbol:      binanceSymbol,
				MarkPrice:        premium.MarkPrice,
				OIBase:           oiBase,
				OIUSD:            oiBase * premium.MarkPrice,
				FundingRate:      premium.LastFundingRate,
				FundingIntervalH: binance.FundingInterval.Hours(),
				PremiumPct:       premiumPct(premium.MarkPrice, premium.IndexPrice),
				TimestampMs:      premiumTimeMs(premium, now),
				SourceOK:         premium.MarkPrice > 0,
			})
		}

		if ticker, ok := bybitTickers[bybitSymbol]; ok {
			intervalH := bybitFundingIntervalH(c.instrumentFor(bybitSymbol))
			quotes = append(quotes, VenueQuote{
				Symbol:           canonical,
				Venue:            "bybit",
				VenueSymbol:      bybitSymbol,
				MarkPrice:        ticker.MarkPrice,
				OIBase:           ticker.OpenInterest,
				OIUSD:            ticker.OpenInterestValue,
				FundingRate:      ticker.FundingRate,
				FundingIntervalH: intervalH,
				PremiumPct:       premiumPct(ticker.MarkPrice, ticker.IndexPrice),
				TimestampMs:      now.UnixMilli(),
				SourceOK:         ticker.MarkPrice > 0,
			})
		}
	}

	if assetCtx, ok := hyperliquidAssetCtx(symbol); ok {
		markPrice := assetCtx.MarkPx
		if markPrice <= 0 {
			markPrice = assetCtx.MidPx
		}
		if markPrice <= 0 {
			markPrice = assetCtx.OraclePx
		}
		quotes = append(quotes, VenueQuote{
			Symbol:           canonical,
			Venue:            "hyperliquid",
			VenueSymbol:      hlCoin,
			MarkPrice:        markPrice,
			OIBase:           assetCtx.OpenInterest,
			OIUSD:            assetCtx.OpenInterest * markPrice,
			FundingRate:      assetCtx.Funding, // the venue charges hourly
			FundingIntervalH: 1,
			PremiumPct:       assetCtx.Premium * 100,
			TimestampMs:      now.UnixMilli(),
			SourceOK:         markPrice > 0,
		})
	}

	return quotes
}

func (c *VenueMetricsCollector) instrumentFor(symbol string) bybit.Instrument {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.instruments[symbol]
}

func (c *VenueMetricsCollector) note(key, message string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastErr[key] == message {
		return
	}
	c.lastErr[key] = message
	logger.Warnf("⚠️ Venue metrics (%s): %s", key, message)
}

func (c *VenueMetricsCollector) clearNote(key string) {
	c.mu.Lock()
	delete(c.lastErr, key)
	c.mu.Unlock()
}

// venueSymbols maps our normalized symbol onto each venue's symbol space.
// xyz assets exist only on Hyperliquid, so the other venues are skipped.
func venueSymbols(symbol string) (canonical string, isXYZ bool, binanceSymbol, bybitSymbol, hlCoin string) {
	canonical = Normalize(symbol)
	if canonical == "" {
		return "", false, "", "", ""
	}
	if strings.HasPrefix(strings.ToLower(canonical), "xyz:") {
		return canonical, true, "", "", canonical
	}
	return canonical, false, canonical, canonical, hyperliquid.NormalizeCoinBase(canonical)
}

func cryptoVenueSymbols(symbols []string) []string {
	result := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		if canonical, isXYZ, _, _, _ := venueSymbols(symbol); canonical != "" && !isXYZ {
			result = append(result, canonical)
		}
	}
	return result
}

func bybitFundingIntervalH(instrument bybit.Instrument) float64 {
	if instrument.FundingIntervalMinutes > 0 {
		return float64(instrument.FundingIntervalMinutes) / 60
	}
	return fundingAssumedIntervalH
}

func premiumPct(markPrice, indexPrice float64) float64 {
	if indexPrice <= 0 {
		return 0
	}
	return (markPrice - indexPrice) / indexPrice * 100
}

func premiumTimeMs(premium binance.PremiumIndex, now time.Time) int64 {
	if premium.Time > 0 {
		return premium.Time
	}
	return now.UnixMilli()
}
