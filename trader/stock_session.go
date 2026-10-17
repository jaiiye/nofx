package trader

import (
	"fmt"
	"time"

	"nofx/market"
	"nofx/provider/hyperliquid"
	"nofx/store"
)

// A stock perp on the HIP-3 dex quotes continuously, but its underlying cash
// market does not. Outside the session the perp drifts on a thin book and the
// next open gaps to the new underlying price — a gap an ATR stop cannot protect,
// because the price jumps straight past it. Rather than maintain a US market
// calendar (holidays, half days, halts, DST), the gate reads the venue itself:
// the most actively traded instrument on the board prints constantly while its
// market is open, and goes quiet when it is not.
const (
	// minStockBookLiquidity is the book-quality floor used when the trades feed
	// is unavailable: below it the book looks like a closed market.
	minStockBookLiquidity = 20
	// maxStockBookSpreadBps is the spread ceiling used in the same fallback.
	maxStockBookSpreadBps = 50.0
)

// stockActivity is the venue-activity reading the gate decides on.
type stockActivity struct {
	// feedActive means the trades feed is connected and receiving, so "no prints
	// for this symbol" is evidence rather than a missing data source.
	feedActive bool
	// lastPrintAge is the age of the symbol's newest venue trade (0 = none seen).
	lastPrintAge time.Duration
	// book metrics, used only when the feed is unavailable.
	hasBook        bool
	liquidityScore int
	spreadBps      float64
}

// coldStockEntry decides whether a NEW stock entry must be blocked. It is a pure
// function so every branch is testable.
//
// The order of the checks matters: with a live feed, silence is the signal. With
// no feed, the book is the signal. With neither, the function allows the entry —
// a data gap must never stop trading (the same fail-open rule the slippage guard
// follows).
func coldStockEntry(isStock bool, activity stockActivity, quietMinutes int) (bool, string) {
	if !isStock {
		return false, ""
	}
	if quietMinutes <= 0 {
		quietMinutes = store.DefaultStockQuietMinutes
	}

	if activity.feedActive {
		if activity.lastPrintAge <= 0 {
			return true, "no trades seen on the venue feed"
		}
		if activity.lastPrintAge > time.Duration(quietMinutes)*time.Minute {
			return true, fmt.Sprintf("no trades for %s", activity.lastPrintAge.Round(time.Second))
		}
		return false, ""
	}

	if !activity.hasBook {
		return false, ""
	}
	if activity.liquidityScore < minStockBookLiquidity {
		return true, fmt.Sprintf("order book liquidity %d/100 (thin)", activity.liquidityScore)
	}
	if activity.spreadBps > maxStockBookSpreadBps {
		return true, fmt.Sprintf("spread %.0f bps (wide)", activity.spreadBps)
	}
	return false, ""
}

// collectStockActivity gathers the venue-activity reading. The order book is only
// fetched when the trades feed cannot answer the question, so the common case
// costs no request.
func collectStockActivity(symbol string) stockActivity {
	feedActive, lastPrintAge := market.VenueActivity(symbol)
	activity := stockActivity{feedActive: feedActive, lastPrintAge: lastPrintAge}
	if feedActive {
		// The feed answered the question; no book fetch needed.
		return activity
	}

	book, err := market.GetOrderBookDepth(symbol, 10)
	if err != nil || book == nil {
		return activity
	}
	activity.hasBook = true
	if analysis := market.AnalyzeMicrostructure(book, nil); analysis != nil {
		activity.liquidityScore = analysis.LiquidityScore
		activity.spreadBps = analysis.SpreadBps
	}
	return activity
}

// checkStockSession blocks a new position in a stock when the venue looks closed.
// Opening only: a close or reduce must never be delayed by a session check.
func (at *AutoTrader) checkStockSession(symbol string) error {
	if at.config.StrategyConfig == nil {
		return nil
	}
	enabled, quietMinutes := at.config.StrategyConfig.RiskControl.StockEntryGate()
	if !enabled || !hyperliquid.IsStockAsset(symbol) {
		return nil
	}

	activity := collectStockActivity(symbol)
	blocked, reason := coldStockEntry(true, activity, quietMinutes)
	if !blocked {
		return nil
	}

	at.logInfof("⏸ New stock entry skipped for %s: %s — the underlying cash market is most likely closed (disable with block_cold_stock_entries, or lower stock_quiet_minutes)",
		symbol, reason)
	return fmt.Errorf("stock market looks closed for %s: %s", symbol, reason)
}
