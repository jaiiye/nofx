package connector

import "strings"

// Fee is a trading cost: an amount in a given asset, plus the rate that
// produced it and whether it was a maker fill.
type Fee struct {
	Amount float64
	Rate   float64 // fraction of notional (e.g. 0.0005 = 5 bps)
	Asset  string
	Maker  bool
}

// FeeModel provides per-exchange fee rates. It replaces the single hardcoded
// taker rate the sizing logic used to assume for every venue.
type FeeModel interface {
	MakerRate() float64
	TakerRate() float64
	// Estimate returns the fee for a notional and liquidity role.
	Estimate(notional float64, maker bool) Fee
}

// StaticFeeModel is a fixed maker/taker schedule.
type StaticFeeModel struct {
	Maker float64
	Taker float64
	Asset string
}

func (m StaticFeeModel) MakerRate() float64 { return m.Maker }
func (m StaticFeeModel) TakerRate() float64 { return m.Taker }

func (m StaticFeeModel) Estimate(notional float64, maker bool) Fee {
	rate := m.Taker
	if maker {
		rate = m.Maker
	}
	asset := m.Asset
	if asset == "" {
		asset = "USDT"
	}
	return Fee{Amount: notional * rate, Rate: rate, Asset: asset, Maker: maker}
}

// defaultFees approximates each venue's public taker schedule (fraction of
// notional). It is a planning estimate, not a billing authority — actual fees
// come from exchange fills.
var defaultFees = map[string]StaticFeeModel{
	"binance":     {Maker: 0.0002, Taker: 0.0005, Asset: "USDT"},
	"bybit":       {Maker: 0.0002, Taker: 0.00055, Asset: "USDT"},
	"okx":         {Maker: 0.0002, Taker: 0.0005, Asset: "USDT"},
	"bitget":      {Maker: 0.0002, Taker: 0.0006, Asset: "USDT"},
	"gate":        {Maker: 0.0002, Taker: 0.0005, Asset: "USDT"},
	"kucoin":      {Maker: 0.0002, Taker: 0.0006, Asset: "USDT"},
	"hyperliquid": {Maker: 0.00015, Taker: 0.00045, Asset: "USDC"},
	"aster":       {Maker: 0.0002, Taker: 0.0005, Asset: "USDT"},
	"lighter":     {Maker: 0.0, Taker: 0.0002, Asset: "USDC"},
	"indodax":     {Maker: 0.0, Taker: 0.003, Asset: "IDR"},
}

// FeeModelFor returns the default fee schedule for an exchange, falling back to
// a generic 5 bps taker schedule for unknown venues.
func FeeModelFor(exchange string) FeeModel {
	if m, ok := defaultFees[strings.ToLower(strings.TrimSpace(exchange))]; ok {
		return m
	}
	return StaticFeeModel{Maker: 0.0002, Taker: 0.0005, Asset: "USDT"}
}
