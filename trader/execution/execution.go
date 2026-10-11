// Package execution abstracts how orders reach the market. The trading loop
// depends on Mode, so the same loop can drive a real exchange (LiveMode) or a
// simulator (paper / dry-run / backtest) without changes.
package execution

import (
	"fmt"
	"sync"

	"nofx/trader/connector"
)

// Mode is the order-placement seam. It is the subset of an exchange adapter
// that the trading loop needs; any full exchange Trader satisfies it.
type Mode interface {
	OpenLong(symbol string, quantity float64, leverage int) (*connector.OrderResult, error)
	OpenShort(symbol string, quantity float64, leverage int) (*connector.OrderResult, error)
	CloseLong(symbol string, quantity float64) (*connector.OrderResult, error)
	CloseShort(symbol string, quantity float64) (*connector.OrderResult, error)
}

// PriceFunc resolves the current mark price for a symbol.
type PriceFunc func(symbol string) (float64, error)

// SimulatedMode fills orders at the current mark price with no exchange round
// trip. It is meant for paper trading / dry-run and backtests, where acting on
// the live account would be wrong.
type SimulatedMode struct {
	price PriceFunc
	fees  connector.FeeModel

	mu  sync.Mutex
	seq int64
}

// NewSimulatedMode builds a paper-trading mode. price must be non-nil; fees
// defaults to a generic schedule when nil.
func NewSimulatedMode(price PriceFunc, fees connector.FeeModel) *SimulatedMode {
	if fees == nil {
		fees = connector.FeeModelFor("")
	}
	return &SimulatedMode{price: price, fees: fees}
}

func (s *SimulatedMode) nextID(prefix string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return fmt.Sprintf("sim-%s-%d", prefix, s.seq)
}

func (s *SimulatedMode) fill(symbol, prefix string, quantity float64, maker bool) (*connector.OrderResult, error) {
	// Live adapters treat quantity 0 as "close all"; the simulator has no
	// position book to resolve that from, so refuse rather than fake a fill.
	if quantity <= 0 {
		return nil, fmt.Errorf("simulated mode: quantity must be positive (close-all is not supported)")
	}
	if s.price == nil {
		return nil, fmt.Errorf("simulated mode: no price source configured")
	}
	px, err := s.price(symbol)
	if err != nil {
		return nil, err
	}
	fee := s.fees.Estimate(px*quantity, maker)
	return &connector.OrderResult{
		OrderID:      s.nextID(prefix),
		Symbol:       symbol,
		Status:       "FILLED",
		AvgFillPrice: px,
		FilledQty:    quantity,
		Fee:          fee.Amount,
		FeeAsset:     fee.Asset,
		IsMaker:      maker,
	}, nil
}

func (s *SimulatedMode) OpenLong(symbol string, quantity float64, leverage int) (*connector.OrderResult, error) {
	return s.fill(symbol, "open_long", quantity, false)
}

func (s *SimulatedMode) OpenShort(symbol string, quantity float64, leverage int) (*connector.OrderResult, error) {
	return s.fill(symbol, "open_short", quantity, false)
}

func (s *SimulatedMode) CloseLong(symbol string, quantity float64) (*connector.OrderResult, error) {
	return s.fill(symbol, "close_long", quantity, false)
}

func (s *SimulatedMode) CloseShort(symbol string, quantity float64) (*connector.OrderResult, error) {
	return s.fill(symbol, "close_short", quantity, false)
}
