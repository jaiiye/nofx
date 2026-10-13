package execution

import (
	"sync"
	"testing"

	"nofx/trader/connector"
)

type fakeSource struct {
	mu    sync.Mutex
	price float64
}

func (f *fakeSource) GetMarketPrice(symbol string) (float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.price, nil
}

func (f *fakeSource) set(p float64) {
	f.mu.Lock()
	f.price = p
	f.mu.Unlock()
}

// A SimulatedTrader is a full exchange Trader (so it can replace the live
// adapter wholesale) and therefore also satisfies the narrow Mode seam.
var _ Mode = (*SimulatedTrader)(nil)

func TestSimulatedTraderOpenMarksToMarket(t *testing.T) {
	src := &fakeSource{price: 100}
	st := NewSimulatedTrader(src, 1000, connector.StaticFeeModel{Taker: 0.001, Asset: "USDT"})

	res, err := st.OpenLong("BTCUSDT", 2, 10)
	if err != nil {
		t.Fatalf("OpenLong: %v", err)
	}
	if res.Status != "FILLED" || res.AvgFillPrice != 100 || res.FilledQty != 2 || res.Fee != 0.2 {
		t.Fatalf("unexpected open result: %+v", res)
	}

	// Fees hit the wallet, and the available balance excludes the margin
	// backing the open position (entry*qty/leverage = 200/10 = 20).
	bal, _ := st.GetBalance()
	if bal["totalEquity"].(float64) != 999.8 {
		t.Fatalf("equity before move = %v, want 999.8", bal["totalEquity"])
	}
	if bal["availableBalance"].(float64) != 979.8 {
		t.Fatalf("available before move = %v, want 979.8", bal["availableBalance"])
	}

	// Price rises: long mark-to-market gains (mark-entry)*qty.
	src.set(110)
	bal, _ = st.GetBalance()
	if got := bal["totalEquity"].(float64); got != 1019.8 {
		t.Fatalf("equity after move = %v, want 1019.8", got)
	}
}

func TestSimulatedTraderCloseAndNoPosition(t *testing.T) {
	src := &fakeSource{price: 50}
	st := NewSimulatedTrader(src, 1000, nil)

	// Closing when flat reports NO_POSITION, not a fabricated fill.
	res, err := st.CloseLong("ETHUSDT", 0)
	if err != nil {
		t.Fatalf("CloseLong: %v", err)
	}
	if res.Status != "NO_POSITION" {
		t.Fatalf("status = %q, want NO_POSITION", res.Status)
	}

	if _, err := st.OpenLong("ETHUSDT", 4, 5); err != nil {
		t.Fatalf("OpenLong: %v", err)
	}
	if _, err := st.CloseLong("ETHUSDT", 0); err != nil { // 0 = close all
		t.Fatalf("CloseLong all: %v", err)
	}
	positions, _ := st.GetPositions()
	if len(positions) != 0 {
		t.Fatalf("expected flat book, got %d positions", len(positions))
	}
}

func TestSimulatedTraderShortMarkToMarket(t *testing.T) {
	src := &fakeSource{price: 100}
	st := NewSimulatedTrader(src, 1000, nil)
	if _, err := st.OpenShort("BTCUSDT", 1, 10); err != nil {
		t.Fatalf("OpenShort: %v", err)
	}
	src.set(90) // price down 10 -> short gains 10
	bal, _ := st.GetBalance()
	if got := bal["totalEquity"].(float64); got != 1009.95 {
		t.Fatalf("short equity = %v, want 1009.95 (1000 + 10 pnl - 0.05 open fee)", got)
	}
}
