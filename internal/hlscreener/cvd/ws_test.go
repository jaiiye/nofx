package cvd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"nofx/internal/hlscreener/hyperliquid"
)

// mockStore records UpsertCVDBar calls for assertions.
type mockStore struct {
	mu    sync.Mutex
	bars  []CVDBar
	calls int32
}

func (m *mockStore) UpsertCVDBar(_ context.Context, b CVDBar) error {
	atomic.AddInt32(&m.calls, 1)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bars = append(m.bars, b)
	return nil
}

func (m *mockStore) Count() int { return int(atomic.LoadInt32(&m.calls)) }
func (m *mockStore) Bars() []CVDBar {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]CVDBar, len(m.bars))
	copy(out, m.bars)
	return out
}

// mockWS upgrades HTTP to WS and pushes trades on demand.
type mockWS struct {
	*httptest.Server
	mu        sync.Mutex
	conns     []*websocket.Conn
	gotSub    int32
}

func newMockWS() *mockWS {
	m := &mockWS{}
	m.Server = httptest.NewServer(http.HandlerFunc(m.handle))
	return m
}

func (m *mockWS) handle(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	m.mu.Lock()
	m.conns = append(m.conns, c)
	m.mu.Unlock()
	// Read loop — consume subscription acks so client can move on
	for {
		mt, msg, err := c.ReadMessage()
		if err != nil {
			c.Close()
			return
		}
		_ = mt
		_ = msg
	}
}

func (m *mockWS) wsURL() string {
	return "ws" + strings.TrimPrefix(m.Server.URL, "http")
}

func (m *mockWS) push(t *testing.T, trades []hyperliquid.Trade) {
	t.Helper()
	env := map[string]any{
		"channel": "trades",
		"data":    trades,
	}
	b, _ := json.Marshal(env)
	m.mu.Lock()
	conns := append([]*websocket.Conn(nil), m.conns...)
	m.mu.Unlock()
	for _, c := range conns {
		if err := c.WriteMessage(websocket.TextMessage, b); err != nil {
			t.Logf("push write: %v", err)
		}
	}
}

func TestWSRecorder_IngestTrades(t *testing.T) {
	mw := newMockWS()
	defer mw.Close()

	ms := &mockStore{}
	// hl is unused in this test (no resync), pass nil
	r := NewWS("BTC", mw.wsURL(), nil, ms, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- r.Run(ctx) }()

	// Wait for the recorder to connect and subscribe.
	waitForConn(t, mw, 2*time.Second)

	bucketMs := int64(5 * 60 * 1000)
	now := time.Now().UnixMilli()
	bucket := (now / bucketMs) * bucketMs

	mw.push(t, []hyperliquid.Trade{
		{Coin: "BTC", Side: "B", Px: "100", Sz: "1", Tid: 1, Time: bucket + 1000},
		{Coin: "BTC", Side: "A", Px: "100", Sz: "0.5", Tid: 2, Time: bucket + 2000},
		{Coin: "BTC", Side: "B", Px: "101", Sz: "2", Tid: 3, Time: bucket + 3000},
	})

	// Wait for ingest
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && ms.Count() == 0 {
		time.Sleep(20 * time.Millisecond)
	}

	if ms.Count() == 0 {
		t.Fatal("no CVDBar upserted after 2s")
	}
	bars := ms.Bars()
	if len(bars) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(bars))
	}
	// buy: 100*1 + 101*2 = 302, sell: 100*0.5 = 50, delta = 252
	if bars[0].Buy != 302 {
		t.Errorf("buy = %v, want 302", bars[0].Buy)
	}
	if bars[0].Sell != 50 {
		t.Errorf("sell = %v, want 50", bars[0].Sell)
	}
	if bars[0].Delta != 252 {
		t.Errorf("delta = %v, want 252", bars[0].Delta)
	}

	cancel()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Log("Run didn't return after cancel; that's OK")
	}
}

func TestWSRecorder_DedupByTid(t *testing.T) {
	mw := newMockWS()
	defer mw.Close()
	ms := &mockStore{}
	r := NewWS("BTC", mw.wsURL(), nil, ms, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)
	waitForConn(t, mw, 2*time.Second)

	now := time.Now().UnixMilli()
	bucket := (now / int64(5*60*1000)) * int64(5*60*1000)

	// Send same trade twice
	trades := []hyperliquid.Trade{
		{Coin: "BTC", Side: "B", Px: "100", Sz: "1", Tid: 100, Time: bucket + 1000},
	}
	mw.push(t, trades)
	mw.push(t, trades) // duplicate

	// Allow time for ingest
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && ms.Count() < 1 {
		time.Sleep(20 * time.Millisecond)
	}
	// Give extra time for the duplicate to (not) arrive
	time.Sleep(200 * time.Millisecond)

	if ms.Count() != 1 {
		t.Errorf("expected 1 upsert (duplicate deduped), got %d", ms.Count())
	}
	cancel()
}

func TestStoreFunc(t *testing.T) {
	var captured CVDBar
	f := StoreFunc(func(_ context.Context, b CVDBar) error {
		captured = b
		return nil
	})
	if err := f.UpsertCVDBar(context.Background(), CVDBar{
		Coin: "X", Bucket: 123, Buy: 1, Sell: 2, Delta: -1,
	}); err != nil {
		t.Fatal(err)
	}
	if captured.Coin != "X" || captured.Bucket != 123 || captured.Delta != -1 {
		t.Errorf("captured = %+v", captured)
	}
}

func waitForConn(t *testing.T, m *mockWS, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		n := len(m.conns)
		m.mu.Unlock()
		if n > 0 {
			// Give the subscribe message a moment to arrive
			time.Sleep(50 * time.Millisecond)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("WS connection never established")
}
