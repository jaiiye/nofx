// Package execution abstracts how orders reach the market. The trading loop
// depends on Mode, so the same loop can drive a real exchange or a simulator
// (paper / dry-run) without changes.
package execution

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"nofx/trader/connector"
	"nofx/trader/types"
)

// Mode is the order-placement seam. It is the subset of an exchange adapter
// that the trading loop needs; any full exchange Trader satisfies it.
type Mode interface {
	OpenLong(symbol string, quantity float64, leverage int) (*connector.OrderResult, error)
	OpenShort(symbol string, quantity float64, leverage int) (*connector.OrderResult, error)
	CloseLong(symbol string, quantity float64) (*connector.OrderResult, error)
	CloseShort(symbol string, quantity float64) (*connector.OrderResult, error)
}

// PriceSource supplies mark prices to the simulator. A live exchange Trader
// satisfies it, so paper mode can read prices from the real adapter.
type PriceSource interface {
	GetMarketPrice(symbol string) (float64, error)
}

// SimulatedTrader is a paper-trading implementation of types.Trader. It fills
// orders at the current mark price and keeps positions/balance in memory, so
// the whole trading loop (AI decisions, grid, recovery, drawdown monitor) can
// run without ever reaching a live account. Market prices are read from a
// source adapter — reads carry no risk.
//
// It is intentionally approximate: funding, partial fills and slippage are not
// modelled, and closed-trade history is not persisted.
type SimulatedTrader struct {
	source  PriceSource
	fees    connector.FeeModel
	initial float64

	mu          sync.Mutex
	seq         int64
	positions   map[string]*simPosition // key: SYMBOL|SIDE
	realizedPnL float64
	feesPaid    float64
}

type simPosition struct {
	symbol   string
	side     string // LONG / SHORT
	quantity float64
	entry    float64
	leverage int
}

// NewSimulatedTrader builds a paper trader. source supplies market prices;
// initial is the virtual starting balance (defaults to 1000 when unset).
func NewSimulatedTrader(source PriceSource, initial float64, fees connector.FeeModel) *SimulatedTrader {
	if fees == nil {
		fees = connector.FeeModelFor("")
	}
	if initial <= 0 {
		initial = 1000
	}
	return &SimulatedTrader{
		source:    source,
		fees:      fees,
		initial:   initial,
		positions: make(map[string]*simPosition),
	}
}

func simKey(symbol, side string) string {
	return strings.ToUpper(symbol) + "|" + strings.ToUpper(side)
}

func (s *SimulatedTrader) nextID(prefix string) string {
	s.seq++
	return fmt.Sprintf("sim-%s-%d", prefix, s.seq)
}

func (s *SimulatedTrader) marketPrice(symbol string) (float64, error) {
	if s.source == nil {
		return 0, fmt.Errorf("simulated trader: no price source")
	}
	return s.source.GetMarketPrice(symbol)
}

// open adds to (or creates) a position at the current mark price.
func (s *SimulatedTrader) open(symbol, side string, quantity float64, leverage int) (*connector.OrderResult, error) {
	if quantity <= 0 {
		return nil, fmt.Errorf("simulated trader: quantity must be positive")
	}
	px, err := s.marketPrice(symbol)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	k := simKey(symbol, side)
	if p, ok := s.positions[k]; ok {
		total := p.quantity + quantity
		p.entry = (p.entry*p.quantity + px*quantity) / total
		p.quantity = total
		if leverage > 0 {
			p.leverage = leverage
		}
	} else {
		lev := leverage
		if lev <= 0 {
			lev = 1
		}
		s.positions[k] = &simPosition{symbol: symbol, side: strings.ToUpper(side), quantity: quantity, entry: px, leverage: lev}
	}
	id := s.nextID("open")
	fee := s.fees.Estimate(px*quantity, false)
	s.feesPaid += fee.Amount
	s.mu.Unlock()

	return &connector.OrderResult{
		OrderID: id, Symbol: symbol, Status: "FILLED",
		AvgFillPrice: px, FilledQty: quantity, Fee: fee.Amount, FeeAsset: fee.Asset,
	}, nil
}

// close reduces (or removes) a position; quantity 0 closes the whole thing.
func (s *SimulatedTrader) close(symbol, side string, quantity float64) (*connector.OrderResult, error) {
	px, err := s.marketPrice(symbol)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	k := simKey(symbol, side)
	p, ok := s.positions[k]
	if !ok {
		s.mu.Unlock()
		return &connector.OrderResult{Status: "NO_POSITION", Symbol: symbol}, nil
	}
	closed := quantity
	if closed <= 0 || closed >= p.quantity {
		closed = p.quantity
	}
	dir := 1.0
	if p.side == "SHORT" {
		dir = -1.0
	}
	s.realizedPnL += (px - p.entry) * closed * dir
	p.quantity -= closed
	if p.quantity <= 0 {
		delete(s.positions, k)
	}
	id := s.nextID("close")
	fee := s.fees.Estimate(px*closed, false)
	s.feesPaid += fee.Amount
	s.mu.Unlock()
	return &connector.OrderResult{
		OrderID: id, Symbol: symbol, Status: "FILLED",
		AvgFillPrice: px, FilledQty: closed, Fee: fee.Amount, FeeAsset: fee.Asset,
	}, nil
}

func (s *SimulatedTrader) OpenLong(symbol string, quantity float64, leverage int) (*connector.OrderResult, error) {
	return s.open(symbol, "LONG", quantity, leverage)
}

func (s *SimulatedTrader) OpenShort(symbol string, quantity float64, leverage int) (*connector.OrderResult, error) {
	return s.open(symbol, "SHORT", quantity, leverage)
}

func (s *SimulatedTrader) CloseLong(symbol string, quantity float64) (*connector.OrderResult, error) {
	return s.close(symbol, "LONG", quantity)
}

func (s *SimulatedTrader) CloseShort(symbol string, quantity float64) (*connector.OrderResult, error) {
	return s.close(symbol, "SHORT", quantity)
}

// GetPositions renders the simulated book in the adapter's map shape.
func (s *SimulatedTrader) GetPositions() ([]map[string]interface{}, error) {
	s.mu.Lock()
	snapshot := make([]*simPosition, 0, len(s.positions))
	for _, p := range s.positions {
		cp := *p
		snapshot = append(snapshot, &cp)
	}
	s.mu.Unlock()

	out := make([]map[string]interface{}, 0, len(snapshot))
	for _, p := range snapshot {
		mark, err := s.marketPrice(p.symbol)
		if err != nil {
			mark = p.entry
		}
		dir := 1.0
		if p.side == "SHORT" {
			dir = -1.0
		}
		out = append(out, map[string]interface{}{
			"symbol":           p.symbol,
			"side":             strings.ToLower(p.side),
			"positionAmt":      p.quantity,
			"entryPrice":       p.entry,
			"markPrice":        mark,
			"unRealizedProfit": (mark - p.entry) * p.quantity * dir,
			"leverage":         float64(p.leverage),
			"liquidationPrice": 0.0,
		})
	}
	return out, nil
}

// GetAccountBalance returns a virtual balance marked to market. Realized PnL and
// accumulated fees flow into the wallet, and the available balance excludes the
// margin currently backing open positions — keeping the risk gate's
// balance-sufficiency check meaningful in paper mode.
//
// The wallet and the position set are captured under one lock and marked with
// one price snapshot, so a single call cannot mix an old wallet with new
// positions. Margin is charged on the mark notional (what a venue actually
// reserves), not on the entry notional, which used to overstate the available
// balance on a position in profit.
func (s *SimulatedTrader) GetAccountBalance() (types.AccountBalance, error) {
	type positionSnapshot struct {
		symbol   string
		side     string
		quantity float64
		entry    float64
		leverage int
	}

	s.mu.Lock()
	wallet := s.initial + s.realizedPnL - s.feesPaid
	positions := make([]positionSnapshot, 0, len(s.positions))
	for _, p := range s.positions {
		positions = append(positions, positionSnapshot{
			symbol: p.symbol, side: p.side, quantity: p.quantity, entry: p.entry, leverage: p.leverage,
		})
	}
	s.mu.Unlock()

	var unrealized, marginUsed float64
	for _, p := range positions {
		dir := 1.0
		if strings.EqualFold(p.side, "SHORT") {
			dir = -1
		}
		mark := p.entry
		if price, err := s.marketPrice(p.symbol); err == nil && price > 0 {
			mark = price
		}
		unrealized += (mark - p.entry) * p.quantity * dir

		lev := float64(p.leverage)
		if lev <= 0 {
			lev = 1
		}
		marginUsed += mark * p.quantity / lev
	}

	balance := types.BalanceFromWallet("paper", wallet, unrealized, 0)
	balance.MarginUsed = marginUsed
	available := balance.Equity - marginUsed
	if available < 0 {
		available = 0
	}
	balance.AvailableMargin = available
	return balance, nil
}

// GetBalance returns the compatibility map. Prefer GetAccountBalance.
func (s *SimulatedTrader) GetBalance() (map[string]interface{}, error) {
	balance, err := s.GetAccountBalance()
	if err != nil {
		return nil, err
	}
	return balance.ToMap(), nil
}

func (s *SimulatedTrader) SetLeverage(symbol string, leverage int) error {
	if leverage <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, side := range []string{"LONG", "SHORT"} {
		if p, ok := s.positions[simKey(symbol, side)]; ok {
			p.leverage = leverage
		}
	}
	return nil
}

// The remaining surface is a no-op: a simulated account has no exchange-side
// orders, margin mode, or conditional orders to manage.
func (s *SimulatedTrader) SetMarginMode(symbol string, isCrossMargin bool) error { return nil }

func (s *SimulatedTrader) SetStopLoss(symbol, positionSide string, quantity, stopPrice float64) error {
	return nil
}

func (s *SimulatedTrader) SetTakeProfit(symbol, positionSide string, quantity, takeProfitPrice float64) error {
	return nil
}

func (s *SimulatedTrader) CancelStopLossOrders(symbol string) error   { return nil }
func (s *SimulatedTrader) CancelTakeProfitOrders(symbol string) error { return nil }
func (s *SimulatedTrader) CancelAllOrders(symbol string) error        { return nil }
func (s *SimulatedTrader) CancelStopOrders(symbol string) error       { return nil }

func (s *SimulatedTrader) FormatQuantity(symbol string, quantity float64) (string, error) {
	return fmt.Sprintf("%.6f", quantity), nil
}

func (s *SimulatedTrader) GetMarketPrice(symbol string) (float64, error) {
	return s.marketPrice(symbol)
}

func (s *SimulatedTrader) GetOrderStatus(symbol string, orderID string) (*connector.OrderResult, error) {
	return &connector.OrderResult{OrderID: orderID, Symbol: symbol, Status: "FILLED"}, nil
}

// GetOrderBook synthesizes a tight, deep book around the current mark price:
// paper mode passes the venue-slippage guard with negligible projected cost,
// and book consumers get a well-formed snapshot.
func (s *SimulatedTrader) GetOrderBook(symbol string, depth int) (bids, asks [][]float64, err error) {
	if depth <= 0 {
		depth = 20
	}
	if depth > 50 {
		depth = 50
	}
	px, err := s.marketPrice(symbol)
	if err != nil {
		return nil, nil, err
	}
	bids = make([][]float64, depth)
	asks = make([][]float64, depth)
	for i := 0; i < depth; i++ {
		bids[i] = []float64{px * (1 - 0.0001*float64(i+1)), 1000}
		asks[i] = []float64{px * (1 + 0.0001*float64(i+1)), 1000}
	}
	return bids, asks, nil
}

func (s *SimulatedTrader) GetClosedPnL(startTime time.Time, limit int) ([]types.ClosedPnLRecord, error) {
	return nil, nil
}

func (s *SimulatedTrader) GetOpenOrders(symbol string) ([]types.OpenOrder, error) {
	return nil, nil
}
