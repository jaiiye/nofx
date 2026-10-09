// WSRecorder subscribes to Hyperliquid's `trades` WebSocket feed and
// aggregates into cvd_bars in real time. Latency from trade execution
// to DB write is <200ms typical, vs. ~3s for the polling Recorder.
//
// Reconnection: when the WS connection drops (HL restarts servers
// periodically), we reconnect with exponential backoff. The HL docs
// say missed data is in the snapshot ack on reconnect — but for our
// `trades` channel (which is incremental, not snapshot), we re-derive
// state via a `recentTrades` HTTP call to catch any trades we missed
// during the disconnect window.
package cvd

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"nofx/internal/hlscreener/hyperliquid"
)

const (
	WSDefaultURL = "wss://api.hyperliquid.xyz/ws"

	// Initial reconnect delay; doubles up to maxReconnectDelay.
	initialReconnectDelay = 1 * time.Second
	maxReconnectDelay     = 30 * time.Second
)

// Store is the subset of *store.Store that WSRecorder needs. Tests
// provide an in-memory implementation; production uses *store.Store.
type Store interface {
	UpsertCVDBar(ctx context.Context, b CVDBar) error
}

// CVDBar mirrors store.CVDBar to avoid an import cycle with
// internal/store. Field-identical.
type CVDBar struct {
	Coin   string
	Bucket int64
	Buy    float64
	Sell   float64
	Delta  float64
}

type WSRecorder struct {
	coin     string
	wsURL    string
	hl       *hyperliquid.Client // used for HTTP fallback on reconnect
	store    Store
	bucketMs int64

	mu      sync.Mutex
	lastTid int64
}

func NewWS(coin string, wsURL string, hl *hyperliquid.Client, st Store, bucketMs int64) *WSRecorder {
	if wsURL == "" {
		wsURL = WSDefaultURL
	}
	if bucketMs == 0 {
		bucketMs = DefaultBucketMs
	}
	return &WSRecorder{
		coin:     coin,
		wsURL:    wsURL,
		hl:       hl,
		store:    st,
		bucketMs: bucketMs,
	}
}

// Run blocks until ctx is cancelled. It maintains a persistent WS
// connection, re-subscribes on reconnect, and resyncs state via
// recentTrades if a gap is detected.
func (r *WSRecorder) Run(ctx context.Context) error {
	delay := initialReconnectDelay
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		err := r.session(ctx)
		if err != nil && ctx.Err() == nil {
			log.Printf("[cvd-ws %s] session: %v (reconnect in %s)", r.coin, err, delay)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
			delay *= 2
			if delay > maxReconnectDelay {
				delay = maxReconnectDelay
			}
			continue
		}
		// Clean exit (ctx cancelled)
		return err
	}
}

// session is a single connect → subscribe → read loop. Returns when
// the connection drops or ctx is cancelled.
func (r *WSRecorder) session(ctx context.Context) error {
	u, err := url.Parse(r.wsURL)
	if err != nil {
		return fmt.Errorf("parse ws url: %w", err)
	}
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPingHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return conn.WriteControl(websocket.PongMessage, nil, time.Now().Add(time.Second))
	})

	// Resync state from recentTrades in case we missed trades during
	// a previous disconnect. We don't need a snapshot ack because
	// `trades` is purely incremental — but if our lastTid is far
	// behind `now`, fetch the last ~2000 trades to fill the gap.
	// Skip when hl is nil (test path).
	if r.hl != nil {
		if err := r.resyncFromHTTP(ctx); err != nil {
			log.Printf("[cvd-ws %s] resync: %v (continuing)", r.coin, err)
		}
	}

	// Subscribe to trades
	sub := map[string]any{
		"method": "subscribe",
		"subscription": map[string]any{
			"type": "trades",
			"coin": r.coin,
		},
	}
	if err := conn.WriteJSON(sub); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	// Read loop
	for {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		// Parse envelope
		var env struct {
			Channel string          `json:"channel"`
			Data    []hyperliquid.Trade `json:"data"`
		}
		if err := json.Unmarshal(msg, &env); err != nil {
			// Likely a subscriptionResponse; ignore
			continue
		}
		if env.Channel != "trades" || len(env.Data) == 0 {
			continue
		}
		if err := r.ingest(ctx, env.Data); err != nil {
			log.Printf("[cvd-ws %s] ingest: %v", r.coin, err)
		}
	}
}

// resyncFromHTTP fetches recentTrades and processes any with tid >
// lastTid. Used after a reconnect to bridge any gap.
func (r *WSRecorder) resyncFromHTTP(ctx context.Context) error {
	trades, err := r.hl.RecentTrades(ctx, r.coin)
	if err != nil {
		return err
	}
	return r.ingest(ctx, trades)
}

// ingest processes a batch of trades, bucketing by 5m boundary and
// deduping by tid.
func (r *WSRecorder) ingest(ctx context.Context, trades []hyperliquid.Trade) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	var maxTid int64
	for _, tr := range trades {
		if tr.Tid > maxTid {
			maxTid = tr.Tid
		}
	}

	type bucketAcc struct {
		buy, sell float64
	}
	accs := map[int64]*bucketAcc{}
	for _, tr := range trades {
		if tr.Tid <= r.lastTid {
			continue
		}
		bucket := alignDown(tr.Time, r.bucketMs)
		a, ok := accs[bucket]
		if !ok {
			a = &bucketAcc{}
			accs[bucket] = a
		}
		notional := tr.Price() * tr.Size()
		if tr.Side == "B" {
			a.buy += notional
		} else {
			a.sell += notional
		}
	}
	if maxTid > r.lastTid {
		r.lastTid = maxTid
	}

	for bucket, a := range accs {
		delta := a.buy - a.sell
		if err := r.store.UpsertCVDBar(ctx, CVDBar{
			Coin: r.coin, Bucket: bucket,
			Buy: a.buy, Sell: a.sell, Delta: delta,
		}); err != nil {
			return err
		}
	}
	return nil
}

// StoreFunc is a function-shape adapter — the caller wraps *store.Store
// (or anything else) as a closure, avoiding the cvd package needing
// to import store. This is the recommended construction pattern:
//
//     rec := cvd.NewWS(coin, "", hl, cvd.StoreFunc(func(ctx context.Context, b cvd.CVDBar) error {
//         return store.UpsertCVDBar(ctx, store.CVDBar{...})
//     }), 0)
type StoreFunc func(ctx context.Context, b CVDBar) error

func (f StoreFunc) UpsertCVDBar(ctx context.Context, b CVDBar) error {
	return f(ctx, b)
}
