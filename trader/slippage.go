package trader

import (
	"fmt"
)

// maxVenueSlippagePct is the code-enforced ceiling on the projected cost of
// walking the venue order book for an opening order, as a percent of the
// best price. Books vary by venue and symbol, so the guard rejects only
// clearly hostile fills rather than chasing marginal quality.
const maxVenueSlippagePct = 0.5

// bookSideForAction maps a decision action to the book side a market order
// consumes: opens and closing shorts buy, opens and closing longs sell.
func bookSideForAction(action string) string {
	switch action {
	case "open_short", "close_long":
		return "SELL"
	default: // open_long, close_short
		return "BUY"
	}
}

// estimateBookSlippagePct walks the venue order book to estimate the average
// fill price for `notionalUSD` of exposure consumed on `side` (BUY consumes
// asks, SELL consumes bids). Levels are [price, size] with size in base
// asset units. Returns the slippage versus the best price in percent; ok is
// false when the book cannot cover the notional (too thin) or is unusable —
// callers treat that as excessive.
func estimateBookSlippagePct(bids, asks [][]float64, side string, notionalUSD float64) (float64, bool) {
	if notionalUSD <= 0 {
		return 0, true
	}
	var levels [][]float64
	switch side {
	case "BUY":
		levels = asks
	case "SELL":
		levels = bids
	default:
		return 0, false
	}
	if len(levels) == 0 {
		return 0, false
	}
	best := levels[0][0]
	if best <= 0 {
		return 0, false
	}

	var costUSD, qtyBase float64
	for _, lvl := range levels {
		if len(lvl) < 2 {
			continue
		}
		price, size := lvl[0], lvl[1]
		if price <= 0 || size <= 0 {
			continue
		}
		takeUSD := price * size
		if costUSD+takeUSD >= notionalUSD {
			need := notionalUSD - costUSD
			costUSD += need
			qtyBase += need / price
			break
		}
		costUSD += takeUSD
		qtyBase += size
	}
	if costUSD < notionalUSD || qtyBase <= 0 {
		return 0, false // book too thin to fill the order
	}

	avgPrice := costUSD / qtyBase
	slippage := (avgPrice - best) / best * 100
	if side == "SELL" {
		slippage = (best - avgPrice) / best * 100
	}
	if slippage < 0 {
		slippage = 0
	}
	return slippage, true
}

// orderBookSource is the duck-typed surface for reading the venue book. The
// exchange adapters implement it; venues without a book simply fail the
// assertion and the slippage check degrades to a skip.
type orderBookSource interface {
	GetOrderBook(symbol string, depth int) (bids, asks [][]float64, err error)
}

// checkVenueSlippage rejects opening orders whose projected cost to walk the
// venue order book exceeds the ceiling. Book-fetch failures fail open: the
// book is an execution-quality signal, not a safety requirement, and a data
// gap must not freeze trading.
func (at *AutoTrader) checkVenueSlippage(symbol, action string, notionalUSD float64) error {
	src, hasBook := at.trader.(orderBookSource)
	if !hasBook {
		return nil
	}
	bids, asks, err := src.GetOrderBook(symbol, 20)
	if err != nil {
		at.logInfof("  ℹ️ Venue slippage check skipped for %s: %v", symbol, err)
		return nil
	}
	pct, ok := estimateBookSlippagePct(bids, asks, bookSideForAction(action), notionalUSD)
	if !ok {
		return fmt.Errorf("venue book for %s cannot cover a %.0f USDT order (too thin)", symbol, notionalUSD)
	}
	if pct > maxVenueSlippagePct {
		return fmt.Errorf("projected venue slippage %.2f%% exceeds the %.2f%% limit for %s (%.0f USDT)",
			pct, maxVenueSlippagePct, symbol, notionalUSD)
	}
	if pct > maxVenueSlippagePct/2 {
		at.logWarnf("  ⚠️ Elevated venue slippage on %s: %.2f%% projected (%.0f USDT)", symbol, pct, notionalUSD)
	}
	return nil
}
