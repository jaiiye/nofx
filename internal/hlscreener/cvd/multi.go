// MultiWS is a single-connection WebSocket CVD recorder for N coins.
//
// The per-coin WSRecorder opens one connection per coin; at 20 coins
// we hit Hyperliquid's per-IP connection limits (observed: dial EOF,
// "Inactive" closes after ~60s). MultiWS dials ONCE and sends one
// subscribe message per coin over the shared connection — the same
// pattern HL's docs show for multi-feed consumers.
//
// Reconnect: on drop, redial + resubscribe all coins + HTTP resync
// per coin (recentTrades) to bridge the gap. lastTid is tracked
// per coin.
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

type MultiWS struct {
	wsURL    string
	hl       *hyperliquid.Client // nil in tests skips HTTP resync
	store    Store
	coins    []string
	bucketMs int64

	mu         sync.Mutex
	lastTids   map[string]int64
	writtenTotal int64
}

func NewMultiWS(wsURL string, hl *hyperliquid.Client, st Store, coins []string, bucketMs int64) *MultiWS {
	if wsURL == "" {
		wsURL = WSDefaultURL
	}
	if bucketMs == 0 {
		bucketMs = DefaultBucketMs
	}
	return &MultiWS{
		wsURL:    wsURL,
		hl:       hl,
		store:    st,
		coins:    coins,
		bucketMs: bucketMs,
		lastTids: map[string]int64{},
	}
}

// Written returns the cumulative number of bucket upserts (for status).
func (m *MultiWS) Written() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writtenTotal
}

// Run blocks until ctx is cancelled, maintaining one WS connection
// with exponential-backoff reconnects.
func (m *MultiWS) Run(ctx context.Context) error {
	delay := initialReconnectDelay
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := m.session(ctx)
		if err != nil && ctx.Err() == nil {
			log.Printf("[cvd-multi] session: %v (reconnect in %s)", err, delay)
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
		return err
	}
}

func (m *MultiWS) session(ctx context.Context) error {
	u, err := url.Parse(m.wsURL)
	if err != nil {
		return fmt.Errorf("parse ws url: %w", err)
	}
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	resetDeadline := func() {
		_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	}
	resetDeadline()
	conn.SetPingHandler(func(string) error {
		resetDeadline()
		return conn.WriteControl(websocket.PongMessage, nil, time.Now().Add(time.Second))
	})

	// HTTP resync per coin to bridge any gap since the last session
	// (skipped when hl is nil — test path).
	if m.hl != nil {
		for _, c := range m.coins {
			trades, err := m.hl.RecentTrades(ctx, c)
			if err != nil {
				log.Printf("[cvd-multi %s] resync: %v (continuing)", c, err)
				continue
			}
			if err := m.ingest(ctx, c, trades); err != nil {
				log.Printf("[cvd-multi %s] resync ingest: %v", c, err)
			}
		}
	}

	// Subscribe to every coin on this one connection.
	for _, c := range m.coins {
		sub := map[string]any{
			"method": "subscribe",
			"subscription": map[string]any{
				"type": "trades",
				"coin": c,
			},
		}
		if err := conn.WriteJSON(sub); err != nil {
			return fmt.Errorf("subscribe %s: %w", c, err)
		}
	}
	log.Printf("[cvd-multi] subscribed to %d coins", len(m.coins))

	for {
		resetDeadline()
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		var env struct {
			Channel string              `json:"channel"`
			Data    []hyperliquid.Trade `json:"data"`
		}
		if err := json.Unmarshal(msg, &env); err != nil {
			continue // subscriptionResponse etc.
		}
		if env.Channel != "trades" || len(env.Data) == 0 {
			continue
		}
		// Route the batch by coin (a trades frame is single-coin).
		byCoin := map[string][]hyperliquid.Trade{}
		for _, tr := range env.Data {
			byCoin[tr.Coin] = append(byCoin[tr.Coin], tr)
		}
		for coin, trades := range byCoin {
			if err := m.ingest(ctx, coin, trades); err != nil {
				log.Printf("[cvd-multi %s] ingest: %v", coin, err)
			}
		}
	}
}

// ingest buckets trades for one coin and persists. Dedupes by tid.
func (m *MultiWS) ingest(ctx context.Context, coin string, trades []hyperliquid.Trade) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var maxTid int64
	for _, tr := range trades {
		if tr.Tid > maxTid {
			maxTid = tr.Tid
		}
	}
	accs := map[int64]*bucketAcc{}
	for _, tr := range trades {
		if tr.Tid <= m.lastTids[coin] {
			continue
		}
		bucket := alignDown(tr.Time, m.bucketMs)
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
	if maxTid > m.lastTids[coin] {
		m.lastTids[coin] = maxTid
	}
	for bucket, a := range accs {
		delta := a.buy - a.sell
		if err := m.store.UpsertCVDBar(ctx, CVDBar{
			Coin: coin, Bucket: bucket,
			Buy: a.buy, Sell: a.sell, Delta: delta,
		}); err != nil {
			return err
		}
		m.writtenTotal++
	}
	return nil
}
