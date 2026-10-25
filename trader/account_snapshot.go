package trader

import (
	"nofx/market"
	"nofx/trader/types"
)

// accountSnapshot is the account state and the open positions, read through the
// typed connector API.
//
// Every consumer (decision context, prompt, risk gate, equity snapshot, grid)
// used to fetch the balance map and re-derive equity with its own fallbacks, so
// the same account produced different numbers depending on who asked. They all
// go through here now.
type accountSnapshot struct {
	Balance   types.AccountBalance
	Positions []map[string]interface{}
}

// readAccountSnapshot fetches the account state and positions once.
func (at *AutoTrader) readAccountSnapshot() (accountSnapshot, error) {
	balance, err := at.trader.GetAccountBalance()
	if err != nil {
		return accountSnapshot{}, err
	}
	positions, err := at.trader.GetPositions()
	if err != nil {
		return accountSnapshot{}, err
	}
	return accountSnapshot{Balance: balance, Positions: positions}, nil
}

// accountPoolFor returns the equity and available margin of the pool a symbol
// settles against.
//
// HIP-3 (xyz) assets only have their own collateral in Standard/Manual mode; the
// connector reports that as XyzSeparate. In the default Unified mode the dex
// state is an overlay on the shared balance, and using it here used to collapse
// an xyz order's size cap to the margin of the position already open.
func accountPoolFor(balance types.AccountBalance, symbol string) (equity, available float64) {
	if market.IsXyzDexAsset(symbol) && balance.XyzSeparate {
		available = balance.AvailableMargin
		if balance.XyzAvailable > 0 {
			available = balance.XyzAvailable
		}
		return balance.XyzEquity, available
	}
	return balance.Equity, balance.AvailableMargin
}

// estimateMarginUsed sums qty*mark/leverage across positions. It is an estimate:
// venues report initial margin per tier, which we do not model. The flag lets
// consumers know the figure is derived.
func estimateMarginUsed(positions []map[string]interface{}) (used float64) {
	for _, p := range positions {
		qty, ok := p["positionAmt"].(float64)
		if !ok || qty == 0 {
			continue
		}
		if qty < 0 {
			qty = -qty
		}
		mark, ok := p["markPrice"].(float64)
		if !ok || mark <= 0 {
			mark, _ = p["entryPrice"].(float64)
		}
		lev := 10.0
		if l, ok := p["leverage"].(float64); ok && l > 0 {
			lev = l
		}
		used += qty * mark / lev
	}
	return used
}
