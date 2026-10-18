package market

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sonirico/go-hyperliquid"

	"nofx/logger"
)

const (
	// hlFlowStaleAfter is how long a symbol may go without a print before its
	// flow reading is treated as absent. Hyperliquid prints continuously on
	// liquid books, so silence means the feed or the subscription is broken —
	// in that case the clause is omitted rather than reported from stale data.
	hlFlowStaleAfter = 15 * time.Minute
	// hlFlowRetryAfter throttles resubscription attempts for a coin whose
	// subscription was rejected (for example an unknown HIP-3 name).
	hlFlowRetryAfter = 30 * time.Minute
)

// parseTrade converts one venue trade message into an aggressor-side print.
// Hyperliquid reports the taker side: "B" means the aggressor bought, "A" means
// the aggressor sold. Malformed messages are dropped rather than counted.
func parseTrade(tr hyperliquid.Trade) (tsMs int64, isBuy bool, notionalUSD float64, ok bool) {
	if tr.Time <= 0 {
		return 0, false, 0, false
	}
	side := strings.ToUpper(strings.TrimSpace(tr.Side))
	if side != "B" && side != "A" {
		return 0, false, 0, false
	}
	px, err := strconv.ParseFloat(strings.TrimSpace(tr.Px), 64)
	if err != nil || px <= 0 {
		return 0, false, 0, false
	}
	sz, err := strconv.ParseFloat(strings.TrimSpace(tr.Sz), 64)
	if err != nil || sz <= 0 {
		return 0, false, 0, false
	}
	return tr.Time, side == "B", px * sz, true
}

// HLFlowWatcher streams the venue's public trades for a rotating set of coins
// and feeds the taker-flow window behind the positioning reading.
//
// The SDK's websocket client owns reconnection and resubscribes every
// subscription after one, so this type only owns the subscription set, the
// health bookkeeping and shutdown. It is best-effort by design: when the feed
// is unavailable the positioning block simply omits its flow clause.
type HLFlowWatcher struct {
	mu       sync.Mutex
	client   *hyperliquid.WebsocketClient
	subs     map[string]*hyperliquid.Subscription // venue coin -> subscription
	lastMsg  map[string]int64                     // venue coin -> last print (ms)
	retryAt  map[string]time.Time                 // venue coin -> next subscribe attempt
	warned   map[string]bool
	lastErr  string
	stopped  bool
	coverage int
}

// StartHLFlow connects the trades feed. It never fails the caller: if the
// connection cannot be established the returned watcher stays inert and the
// flow clause is omitted from the prompt.
func StartHLFlow(ctx context.Context) *HLFlowWatcher {
	w := &HLFlowWatcher{
		subs:    make(map[string]*hyperliquid.Subscription),
		lastMsg: make(map[string]int64),
		retryAt: make(map[string]time.Time),
		warned:  make(map[string]bool),
	}
	w.client = hyperliquid.NewWebsocketClient("")
	if err := w.client.Connect(ctx); err != nil {
		logger.Infof("⚠️ Hyperliquid trades feed unavailable (taker flow omitted from the prompt): %v", err)
		return w
	}
	logger.Info("📡 Hyperliquid trades feed connected — positioning taker flow enabled")
	return w
}

// SetSymbols makes the subscription set match the given symbols (positions plus
// candidates). Only the difference is applied, so calling it every cycle does
// not churn the feed.
func (w *HLFlowWatcher) SetSymbols(symbols []string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.client == nil || w.stopped {
		return
	}

	desired := make(map[string]string, len(symbols)) // venue coin -> our symbol
	for _, symbol := range symbols {
		coin := venueCoinName(symbol)
		if coin == "" {
			continue
		}
		desired[coin] = Normalize(symbol)
	}

	for coin, sub := range w.subs {
		if _, keep := desired[coin]; keep {
			continue
		}
		if sub != nil && sub.Close != nil {
			sub.Close()
		}
		delete(w.subs, coin)
		delete(w.lastMsg, coin)
	}

	now := time.Now()
	for coin, symbol := range desired {
		if _, subscribed := w.subs[coin]; subscribed {
			continue
		}
		if next, wait := w.retryAt[coin]; wait && now.Before(next) {
			continue
		}

		sub, err := w.client.Trades(hyperliquid.TradesSubscriptionParams{Coin: coin}, w.callback(symbol))
		if err != nil {
			w.retryAt[coin] = now.Add(hlFlowRetryAfter)
			if !w.warned[coin] {
				w.warned[coin] = true
				logger.Infof("⚠️ Hyperliquid trades subscription rejected for %s (flow omitted for it): %v", coin, err)
			}
			continue
		}
		w.subs[coin] = sub
	}
	w.coverage = len(desired)
}

// callback turns venue trades into aggressor-side prints. The same symbol keeps
// its earlier prints across a resubscription, so a reconnect does not reset the
// window (the SDK replaces the subscription, not the accumulator).
func (w *HLFlowWatcher) callback(symbol string) func([]hyperliquid.Trade, error) {
	return func(trades []hyperliquid.Trade, err error) {
		if err != nil {
			// The SDK reconnects on its own; log each distinct error once so a
			// persistent outage is visible without flooding the log.
			w.mu.Lock()
			if w.lastErr != err.Error() {
				w.lastErr = err.Error()
				logger.Infof("⚠️ Hyperliquid trades feed error (will reconnect): %v", err)
			}
			w.mu.Unlock()
			return
		}

		for _, tr := range trades {
			tsMs, isBuy, notional, ok := parseTrade(tr)
			if !ok {
				continue
			}
			hlPositioning.flow.AddTaker(symbol, tsMs, isBuy, notional)
			// Both sides of the print are the only way to discover which addresses
			// hold this market: the holder-position index polls them for their
			// cost/liquidation structure.
			captureTradeAddresses(symbol, tr.Users, tsMs)

			w.mu.Lock()
			if tsMs > w.lastMsg[tr.Coin] {
				w.lastMsg[tr.Coin] = tsMs
			}
			w.mu.Unlock()
		}
	}
}

// Coverage reports how many coins are subscribed and how long ago the newest
// print across them arrived (0 when nothing has arrived yet).
func (w *HLFlowWatcher) Coverage() (int, time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()

	subscribed := len(w.subs)
	var newest int64
	for _, ts := range w.lastMsg {
		if ts > newest {
			newest = ts
		}
	}
	if newest == 0 {
		return subscribed, 0
	}
	return subscribed, time.Duration(time.Now().UnixMilli()-newest) * time.Millisecond
}

// Stop closes every subscription and the connection.
func (w *HLFlowWatcher) Stop() {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return
	}
	w.stopped = true
	subs := w.subs
	client := w.client
	w.subs = make(map[string]*hyperliquid.Subscription)
	w.mu.Unlock()

	for _, sub := range subs {
		if sub != nil && sub.Close != nil {
			sub.Close()
		}
	}
	if client != nil {
		if err := client.Close(); err != nil {
			logger.Infof("⚠️ Hyperliquid trades feed close error: %v", err)
		}
	}
}

// Process-level handle, so the kernel can steer the feed without threading it
// through every call.
var hlFlow = struct {
	mu      sync.RWMutex
	watcher *HLFlowWatcher
}{}

// SetHLFlowWatcher registers the running watcher (called from main).
func SetHLFlowWatcher(w *HLFlowWatcher) {
	hlFlow.mu.Lock()
	hlFlow.watcher = w
	hlFlow.mu.Unlock()
}

// UpdateHLFlowSymbols tells the trades feed which coins to stream. A no-op when
// the feed is not running (tests, or a deployment without the connection), which
// leaves the flow clause out of the prompt.
func UpdateHLFlowSymbols(symbols []string) {
	hlFlow.mu.RLock()
	w := hlFlow.watcher
	hlFlow.mu.RUnlock()
	if w == nil {
		return
	}
	w.SetSymbols(symbols)
}

// HLFlowCoverage exposes the feed health for logging.
func HLFlowCoverage() (int, time.Duration) {
	hlFlow.mu.RLock()
	w := hlFlow.watcher
	hlFlow.mu.RUnlock()
	if w == nil {
		return 0, 0
	}
	return w.Coverage()
}

// HLFlowFeedActive reports whether the trades feed is connected, subscribed and
// currently receiving. Callers that treat "no prints for a symbol" as evidence
// about the market must check this first: no feed is not the same as no trades.
func HLFlowFeedActive() bool {
	subscribed, age := HLFlowCoverage()
	if subscribed == 0 || age == 0 {
		return false
	}
	return age <= hlFlowStaleAfter
}

// VenueActivity reports how recently a symbol traded on the venue feed. It does
// no I/O. feedActive is false when the feed is not running, in which case the
// age carries no information about the market and callers must fall back to
// another signal (or allow the trade).
func VenueActivity(symbol string) (feedActive bool, lastPrintAge time.Duration) {
	if !HLFlowFeedActive() {
		return false, 0
	}
	return true, hlPositioning.flow.LastPrintAge(Normalize(symbol), time.Now().UnixMilli())
}

// takerFlowFresh reports whether the flow window's newest print for a symbol is
// recent enough to describe the current market. A stale feed means the clause
// is omitted instead of reporting a ratio built from minutes-old prints.
func takerFlowFresh(symbol string, nowMs int64) bool {
	age := hlPositioning.flow.LastPrintAge(symbol, nowMs)
	if age == 0 {
		return false
	}
	return age <= hlFlowStaleAfter
}
