package types

import (
	"fmt"
	"math"
)

// AccountBalance is the single source of truth for the money in an account.
//
// Consumers used to read a map with four overlapping keys (totalEquity /
// total_equity / totalWalletBalance / balance) and apply their own fallbacks, so
// the same account produced different equity figures depending on who asked —
// the risk gate and the AI prompt could disagree, and a missing key silently
// degraded to "available margin is the equity".
//
// The invariant is enforced by construction, not by convention:
//
//	Equity == Wallet + UnrealizedPnL
//
// Use BalanceFromEquity or BalanceFromWallet with whichever figure the venue
// reports; the other is derived, so the identity cannot drift.
type AccountBalance struct {
	Equity          float64 // mark-to-market equity (wallet + unrealized)
	Wallet          float64 // cash / realized side (transfers and realized PnL only)
	UnrealizedPnL   float64
	AvailableMargin float64 // margin a new order can use
	MarginUsed      float64
	MarginEstimated bool // MarginUsed is a qty*mark/leverage estimate, not the venue's figure

	// Per-dex collateral. Only meaningful when the venue really keeps a separate
	// pool (Hyperliquid Standard/Manual mode); XyzSeparate says whether it applies.
	XyzEquity    float64
	XyzAvailable float64
	XyzSeparate  bool

	// Complete is false when a sub-query failed and a component is missing.
	// Consumers must fail closed rather than size a trade against a hole.
	Complete bool
	Venue    string
}

// BalanceFromEquity derives the wallet from a venue-reported equity.
func BalanceFromEquity(venue string, equity, unrealizedPnL, available float64) AccountBalance {
	return AccountBalance{
		Equity:          equity,
		Wallet:          equity - unrealizedPnL,
		UnrealizedPnL:   unrealizedPnL,
		AvailableMargin: available,
		Complete:        true,
		Venue:           venue,
	}
}

// BalanceFromWallet derives the equity from a venue-reported cash wallet.
func BalanceFromWallet(venue string, wallet, unrealizedPnL, available float64) AccountBalance {
	return AccountBalance{
		Equity:          wallet + unrealizedPnL,
		Wallet:          wallet,
		UnrealizedPnL:   unrealizedPnL,
		AvailableMargin: available,
		Complete:        true,
		Venue:           venue,
	}
}

// Incomplete marks a balance whose inputs could not all be read.
func (b AccountBalance) Incomplete() AccountBalance {
	b.Complete = false
	return b
}

// Validate reports a broken invariant. The tolerance absorbs venue rounding:
// exchanges publish two-decimal figures in some places and six in others.
func (b AccountBalance) Validate() error {
	if diff := math.Abs(b.Equity - (b.Wallet + b.UnrealizedPnL)); diff > 0.01 {
		return fmt.Errorf("%s balance identity broken: equity %.6f != wallet %.6f + unrealized %.6f (diff %.6f)",
			b.Venue, b.Equity, b.Wallet, b.UnrealizedPnL, diff)
	}
	return nil
}

// FromMap reconstructs an AccountBalance from a map produced by ToMap (the
// connectors cache that form). It is not a venue parser: the keys are the
// canonical aliases we emit ourselves, so there is nothing to guess.
func FromMap(m map[string]interface{}) AccountBalance {
	get := func(key string) float64 {
		v, _ := m[key].(float64)
		return v
	}
	b := AccountBalance{
		Equity:          get("totalEquity"),
		Wallet:          get("totalWalletBalance"),
		UnrealizedPnL:   get("totalUnrealizedProfit"),
		AvailableMargin: get("availableBalance"),
		MarginUsed:      get("marginUsed"),
		Venue:           "",
		Complete:        m["balance_incomplete"] != true,
	}
	if venue, ok := m["venue"].(string); ok {
		b.Venue = venue
	}
	if estimated, ok := m["marginEstimated"].(bool); ok {
		b.MarginEstimated = estimated
	}
	if _, ok := m["xyzDexBalance"]; ok {
		b.XyzSeparate = true
		b.XyzEquity = get("xyzDexBalance")
		b.XyzAvailable = get("xyzDexAvailable")
	}
	return b
}

// ToMap renders the compatibility map that GetBalance still returns. Every alias
// is emitted so no consumer has to guess a key name.
func (b AccountBalance) ToMap() map[string]interface{} {
	m := map[string]interface{}{
		"totalEquity":           b.Equity,
		"total_equity":          b.Equity,
		"totalWalletBalance":    b.Wallet,
		"wallet_balance":        b.Wallet,
		"totalUnrealizedProfit": b.UnrealizedPnL,
		"unrealized_profit":     b.UnrealizedPnL,
		"availableBalance":      b.AvailableMargin,
		"balance":               b.Equity,
		"marginUsed":            b.MarginUsed,
		"marginEstimated":       b.MarginEstimated,
		"venue":                 b.Venue,
	}
	if b.XyzSeparate {
		m["xyzDexBalance"] = b.XyzEquity
		m["xyzDexAvailable"] = b.XyzAvailable
	}
	if !b.Complete {
		m["balance_incomplete"] = true
	}
	return m
}
