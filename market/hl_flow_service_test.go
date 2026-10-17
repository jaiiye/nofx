package market

import (
	"math"
	"testing"
	"time"

	"github.com/sonirico/go-hyperliquid"
)

func TestParseTrade(t *testing.T) {
	base := time.Now().UnixMilli()

	ts, isBuy, notional, ok := parseTrade(hyperliquid.Trade{Coin: "BTC", Side: "B", Px: "60000", Sz: "0.5", Time: base})
	if !ok || !isBuy || ts != base || math.Abs(notional-30000) > 1e-9 {
		t.Fatalf("buy print parsed as ts=%d buy=%v notional=%v ok=%v", ts, isBuy, notional, ok)
	}

	// "A" is the sell side (the venue reports the aggressor's side).
	_, isBuy, _, ok = parseTrade(hyperliquid.Trade{Coin: "BTC", Side: "a", Px: "60000", Sz: "0.5", Time: base})
	if !ok || isBuy {
		t.Fatalf("sell print parsed as buy=%v ok=%v, want a sell", isBuy, ok)
	}

	// Malformed messages are dropped, never counted as flow.
	bad := []hyperliquid.Trade{
		{Coin: "BTC", Side: "B", Px: "60000", Sz: "0.5"},             // no timestamp
		{Coin: "BTC", Side: "X", Px: "60000", Sz: "0.5", Time: base}, // unknown side
		{Coin: "BTC", Side: "B", Px: "", Sz: "0.5", Time: base},      // no price
		{Coin: "BTC", Side: "B", Px: "60000", Sz: "0", Time: base},   // zero size
		{Coin: "BTC", Side: "B", Px: "-1", Sz: "1", Time: base},      // negative price
		{Coin: "BTC", Side: "B", Px: "abc", Sz: "1", Time: base},     // unparsable
	}
	for i, tr := range bad {
		if _, _, _, ok := parseTrade(tr); ok {
			t.Errorf("malformed print %d must be dropped", i)
		}
	}
}

// A closed feed must not keep describing the market: the flow clause is only
// rendered while the newest print is recent.
func TestTakerFlowFreshness(t *testing.T) {
	now := time.Now().UnixMilli()
	window := HLFlowWindow()

	if takerFlowFresh("FRESHUSDT", now) {
		t.Fatal("a symbol with no prints must not be considered fresh")
	}

	window.AddTaker("FRESHUSDT", now-int64(30*time.Second/time.Millisecond), true, 1000)
	if !takerFlowFresh("FRESHUSDT", now) {
		t.Fatal("a print from 30s ago must count as fresh")
	}

	window.AddTaker("STALEUSDT", now-int64(30*time.Minute/time.Millisecond), true, 1000)
	if takerFlowFresh("STALEUSDT", now) {
		t.Fatalf("a print older than %v must not be considered fresh", hlFlowStaleAfter)
	}
}

// The watcher is best-effort: without a connection every operation is a no-op
// rather than a panic, because the positioning block simply omits the flow.
func TestHLFlowWatcherWithoutConnectionIsSafe(t *testing.T) {
	w := &HLFlowWatcher{}
	w.SetSymbols([]string{"BTCUSDT", "xyz:SILVER"})

	if subs, age := w.Coverage(); subs != 0 || age != 0 {
		t.Fatalf("inert watcher coverage = %d/%v, want 0/0", subs, age)
	}
	w.Stop()
	w.Stop() // idempotent

	// The process-level steering helpers tolerate a missing watcher.
	SetHLFlowWatcher(nil)
	UpdateHLFlowSymbols([]string{"BTCUSDT"})
	if subs, _ := HLFlowCoverage(); subs != 0 {
		t.Fatalf("coverage without a watcher = %d, want 0", subs)
	}
}
