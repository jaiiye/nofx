package market

import (
	"context"
	"strings"
	"sync"
	"time"

	"nofx/logger"
	"nofx/provider/binance"
	"nofx/provider/bybit"
)

const (
	// liquidationFlushInterval batches prints into one store transaction. The
	// streams can be bursty, so writing per print would mean thousands of tiny
	// transactions.
	liquidationFlushInterval = 2 * time.Second
	// liquidationPendingCap bounds the buffer when the sink is failing.
	liquidationPendingCap = 10000
	// liquidationFlushBatch is the maximum rows handed to the sink at once.
	liquidationFlushBatch = 500
	// liquidationWindowGrace is how long a window survives after its symbol
	// leaves the analysed set. Candidate lists rotate; a symbol that returns
	// within this window keeps its baseline.
	liquidationWindowGrace = 30 * time.Minute
)

// LiquidationSink persists a batch of prints. It is a function rather than an
// interface over a store so this package stays free of database types.
type LiquidationSink func([]LiquidationPrint) error

// LiquidationWatcher consumes the two public liquidation streams, keeps a
// per-symbol flow window for the prompt, and batches prints to its sink.
type LiquidationWatcher struct {
	mu      sync.RWMutex
	flows   map[string]*LiquidationWindow // canonical symbol -> window
	symbols map[string]string             // venue symbol -> canonical symbol
	lastMsg map[string]int64              // venue -> last print (ms)
	// received counts prints delivered by each venue before the symbol filter.
	// It separates "the market is quiet" from "the feed is broken", which the
	// per-symbol coverage alone cannot.
	received map[string]int64
	lastErr  map[string]string
	stopped  bool

	pendingMu sync.Mutex
	pending   []LiquidationPrint
	dropped   int64

	sink LiquidationSink

	binance *binance.Client
	bybit   *bybit.Client
}

var (
	liquidationWatcherMu sync.RWMutex
	liquidationWatcher   *LiquidationWatcher
)

func NewLiquidationWatcher(sink LiquidationSink) *LiquidationWatcher {
	return &LiquidationWatcher{
		flows:    make(map[string]*LiquidationWindow),
		symbols:  make(map[string]string),
		lastMsg:  make(map[string]int64),
		received: make(map[string]int64),
		lastErr:  make(map[string]string),
		sink:     sink,
		binance:  binance.NewClient(),
		bybit:    bybit.NewClient(),
	}
}

// StartLiquidationFlow consumes both public streams until ctx ends. It never
// fails the caller: an unreachable venue simply contributes no prints.
func StartLiquidationFlow(ctx context.Context, sink LiquidationSink) *LiquidationWatcher {
	watcher := NewLiquidationWatcher(sink)
	watcher.start(ctx)
	return watcher
}

// SetLiquidationWatcher registers the process-wide watcher so the prompt path
// can read it without threading a handle through every call site.
func SetLiquidationWatcher(watcher *LiquidationWatcher) {
	liquidationWatcherMu.Lock()
	liquidationWatcher = watcher
	liquidationWatcherMu.Unlock()
}

func currentLiquidationWatcher() *LiquidationWatcher {
	liquidationWatcherMu.RLock()
	defer liquidationWatcherMu.RUnlock()
	return liquidationWatcher
}

// UpdateLiquidationSymbols steers the streams to the symbols being analysed.
func UpdateLiquidationSymbols(symbols []string) {
	if watcher := currentLiquidationWatcher(); watcher != nil {
		watcher.SetSymbols(symbols)
	}
}

// LiquidationFlowForSymbol returns the derived flow view for a symbol.
func LiquidationFlowForSymbol(symbol string) (*LiquidationReading, bool) {
	watcher := currentLiquidationWatcher()
	if watcher == nil {
		return nil, false
	}
	return watcher.Reading(symbol)
}

// LiquidationFlowCoverage reports how many symbols have prints and how old the
// newest print is, for health logs.
func LiquidationFlowCoverage() (int, time.Duration) {
	watcher := currentLiquidationWatcher()
	if watcher == nil {
		return 0, 0
	}
	return watcher.Coverage()
}

// SetSymbols replaces the watched set. Binance streams the whole market and is
// filtered locally; Bybit is subscribed per symbol by the stream itself.
//
// Windows for symbols that dropped out of the set are kept for a grace period
// (candidate lists rotate, and a symbol that returns would otherwise lose its
// baseline) and then evicted, so the map cannot grow for the life of the
// process.
func (w *LiquidationWatcher) SetSymbols(symbols []string) {
	if w == nil {
		return
	}
	next := make(map[string]string, len(symbols))
	for _, symbol := range symbols {
		canonical, isXYZ, binanceSymbol, bybitSymbol, _ := venueSymbols(symbol)
		if canonical == "" || isXYZ {
			continue
		}
		next[binanceSymbol] = canonical
		next[bybitSymbol] = canonical
	}

	w.mu.Lock()
	w.symbols = next
	tracked := make(map[string]bool, len(next))
	for _, canonical := range next {
		tracked[canonical] = true
	}
	cutoff := time.Now().Add(-liquidationWindowGrace).UnixMilli()
	for symbol, window := range w.flows {
		if tracked[symbol] {
			continue
		}
		if window.LastTimestampMs() < cutoff {
			delete(w.flows, symbol)
		}
	}
	w.mu.Unlock()
}

// bybitSymbols returns the current venue symbols in Bybit's notation.
func (w *LiquidationWatcher) bybitSymbols() []string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	symbols := make([]string, 0, len(w.symbols)/2)
	for symbol := range w.symbols {
		symbols = append(symbols, symbol)
	}
	return symbols
}

// Reading derives the flow view for one symbol at the current time.
func (w *LiquidationWatcher) Reading(symbol string) (*LiquidationReading, bool) {
	if w == nil {
		return nil, false
	}
	canonical := Normalize(symbol)
	w.mu.RLock()
	window := w.flows[canonical]
	w.mu.RUnlock()
	if window == nil {
		return nil, false
	}
	reading := window.Reading(canonical, time.Now())
	if reading == nil {
		return nil, false
	}
	return reading, true
}

// ReceivedCounts reports how many prints each venue delivered before the
// symbol filter. A non-zero count with zero tracked symbols means the feeds are
// healthy and the watched market is simply quiet.
func (w *LiquidationWatcher) ReceivedCounts() map[string]int64 {
	if w == nil {
		return nil
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	counts := make(map[string]int64, len(w.received))
	for venue, count := range w.received {
		counts[venue] = count
	}
	return counts
}

// Coverage reports how many symbols have a retained window and the age of the
// newest print across venues.
func (w *LiquidationWatcher) Coverage() (int, time.Duration) {
	if w == nil {
		return 0, 0
	}
	w.mu.RLock()
	defer w.mu.RUnlock()

	age := time.Duration(0)
	newest := int64(0)
	for _, timestamp := range w.lastMsg {
		if timestamp > newest {
			newest = timestamp
		}
	}
	if newest > 0 {
		age = time.Since(time.UnixMilli(newest))
	}
	return len(w.flows), age
}

// Stop makes further ingestion a no-op. The streams exit on ctx cancellation.
func (w *LiquidationWatcher) Stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.stopped = true
	w.mu.Unlock()
}

func (w *LiquidationWatcher) isStopped() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.stopped
}

func (w *LiquidationWatcher) start(ctx context.Context) {
	if w == nil {
		return
	}

	go w.runBinanceStream(ctx)
	go w.runBybitStream(ctx)
	go w.runFlusher(ctx)

	logger.Infof("📉 Liquidation flow started (Binance + Bybit public streams)")
}

func (w *LiquidationWatcher) runBinanceStream(ctx context.Context) {
	if err := w.binance.StreamForceOrders(ctx, func(order binance.ForceOrder) {
		w.ingest("binance", order.Symbol, order.Side, order.Price, order.Qty, order.NotionalUSD, order.TimeMs)
	}); err != nil && ctx.Err() == nil {
		w.note("binance-stream", err.Error())
	}
}

func (w *LiquidationWatcher) runBybitStream(ctx context.Context) {
	getter := func() []string {
		if w.isStopped() {
			return nil
		}
		return w.bybitSymbols()
	}
	if err := w.bybit.StreamAllLiquidation(ctx, getter, func(liquidation bybit.AllLiquidation) {
		w.ingest("bybit", liquidation.Symbol, liquidation.Side, liquidation.Price, liquidation.Qty, liquidation.NotionalUSD, liquidation.TimeMs)
	}); err != nil && ctx.Err() == nil {
		w.note("bybit-stream", err.Error())
	}
}

// ingest records one print, dropping symbols that are not currently analysed.
func (w *LiquidationWatcher) ingest(venue, venueSymbol, side string, price, qty, notional float64, timestampMs int64) {
	if w == nil || w.isStopped() {
		return
	}
	venueSymbol = strings.ToUpper(strings.TrimSpace(venueSymbol))

	w.mu.Lock()
	w.received[venue]++
	canonical, tracked := w.symbols[venueSymbol]
	if !tracked || canonical == "" {
		w.mu.Unlock()
		return
	}
	window := w.flows[canonical]
	if window == nil {
		window = NewLiquidationWindow()
		w.flows[canonical] = window
	}
	w.lastMsg[venue] = timestampMs
	w.mu.Unlock()

	print := LiquidationPrint{
		TimestampMs: timestampMs,
		Venue:       venue,
		Symbol:      canonical,
		VenueSymbol: venueSymbol,
		Side:        side,
		Price:       price,
		Qty:         qty,
		NotionalUSD: notional,
	}
	window.Add(print)

	w.pendingMu.Lock()
	if len(w.pending) >= liquidationPendingCap {
		w.pending = w.pending[1:]
		w.dropped++
	}
	w.pending = append(w.pending, print)
	w.pendingMu.Unlock()
}

func (w *LiquidationWatcher) runFlusher(ctx context.Context) {
	ticker := time.NewTicker(liquidationFlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.flush()
			return
		case <-ticker.C:
			w.flush()
		}
	}
}

func (w *LiquidationWatcher) flush() {
	if w == nil {
		return
	}

	// Drain before the sink check: a watcher without a sink (tests, or a boot
	// with no store) must still discard its buffer instead of growing it.
	w.pendingMu.Lock()
	batch := w.pending
	w.pending = nil
	dropped := w.dropped
	w.dropped = 0
	w.pendingMu.Unlock()

	if w.sink == nil || len(batch) == 0 {
		return
	}
	if dropped > 0 {
		w.note("buffer", "liquidation buffer overflow, dropped oldest prints")
	}

	for start := 0; start < len(batch); start += liquidationFlushBatch {
		end := start + liquidationFlushBatch
		if end > len(batch) {
			end = len(batch)
		}
		if err := w.sink(batch[start:end]); err != nil {
			w.note("store", "persist failed: "+err.Error())
			return
		}
	}
	w.clearNote("store")
}

func (w *LiquidationWatcher) note(key, message string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.lastErr[key] == message {
		return
	}
	w.lastErr[key] = message
	logger.Warnf("⚠️ Liquidation flow (%s): %s", key, message)
}

func (w *LiquidationWatcher) clearNote(key string) {
	w.mu.Lock()
	delete(w.lastErr, key)
	w.mu.Unlock()
}
