package market

import (
	"math"
	"strings"
)

// AddressPosition is one holder's open position in a market, reduced to the two
// prices a cost/liquidation profile needs.
type AddressPosition struct {
	Address       string
	Side          string  // "long" or "short"
	Size          float64 // absolute base size
	EntryPrice    float64
	LiquidationPx float64 // 0 when the venue reports none
}

// PriceBin is one bucket of the price band around the mark price. Cost buckets
// hold entry prices (where holders defend); liquidation buckets hold the prices
// where they are forced out (the fuel for a cascade or a squeeze).
type PriceBin struct {
	Index     int     `json:"index"`
	Lo        float64 `json:"lo"`
	Hi        float64 `json:"hi"`
	Px        float64 `json:"px"` // bucket mid, for display
	LongCost  float64 `json:"longCost"`
	ShortCost float64 `json:"shortCost"`
	LongLiq   float64 `json:"longLiq"`
	ShortLiq  float64 `json:"shortLiq"`
}

// PositionCoverage is the honesty block of a profile: how much of the market the
// buckets actually represent. A profile with poor coverage may be shown as
// context but must never be used to block a trade.
type PositionCoverage struct {
	AddressesSeen    int    `json:"addressesSeen"`
	PositionsSeen    int    `json:"positionsSeen"`
	PositionsPlaced  int    `json:"positionsPlaced"`
	PositionsOutside int    `json:"positionsOutside"`
	WeightSource     string `json:"weightSource"`
}

// PositionWeightSourceNotional documents how each position was weighted.
const PositionWeightSourceNotional = "notional (size x mark)"

// BinPositions buckets holder positions into a symmetric band around the mark
// price, weighting every position by notional (size × mark). An entry price
// places the position on the cost side of its bucket; a liquidation price places
// it on the liquidation side. A position counts as placed when either price
// landed inside the band.
//
// Prices outside the band are counted as outside rather than clamped into the
// edge buckets: a band that is too narrow then shows up in the coverage instead
// of silently piling fake structure onto the edges. Returns nil buckets when the
// inputs cannot describe a band, which callers read as "no structure available".
func BinPositions(positions []AddressPosition, markPrice, bandPct float64, binCount int) ([]PriceBin, PositionCoverage) {
	coverage := PositionCoverage{WeightSource: PositionWeightSourceNotional}
	if markPrice <= 0 || bandPct <= 0 || binCount <= 0 {
		return nil, coverage
	}

	lo := markPrice * (1 - bandPct/100)
	if lo <= 0 {
		// A band wider than the price itself: keep the floor positive.
		lo = markPrice / 2
	}
	hi := markPrice * (1 + bandPct/100)
	step := (hi - lo) / float64(binCount)
	if step <= 0 {
		return nil, coverage
	}

	bins := make([]PriceBin, binCount)
	for i := range bins {
		bin := PriceBin{Index: i, Lo: lo + step*float64(i), Hi: lo + step*float64(i+1)}
		bin.Px = (bin.Lo + bin.Hi) / 2
		bins[i] = bin
	}

	addresses := make(map[string]struct{}, len(positions))
	for _, position := range positions {
		side := strings.ToLower(strings.TrimSpace(position.Side))
		if side != "long" && side != "short" {
			continue
		}
		if position.Size <= 0 {
			continue
		}
		coverage.PositionsSeen++
		if position.Address != "" {
			addresses[position.Address] = struct{}{}
		}

		weight := position.Size * markPrice
		placed := false
		if index, ok := binIndexFor(position.EntryPrice, lo, step, binCount); ok {
			if side == "long" {
				bins[index].LongCost += weight
			} else {
				bins[index].ShortCost += weight
			}
			placed = true
		}
		if index, ok := binIndexFor(position.LiquidationPx, lo, step, binCount); ok {
			if side == "long" {
				bins[index].LongLiq += weight
			} else {
				bins[index].ShortLiq += weight
			}
			placed = true
		}
		if placed {
			coverage.PositionsPlaced++
		} else {
			coverage.PositionsOutside++
		}
	}
	coverage.AddressesSeen = len(addresses)

	return bins, coverage
}

// binEdgeEpsilon absorbs floating point drift when a price sits exactly on a
// bucket edge. The band's lower edge is computed as mark × (1 − band/100), which
// can land a hair below the intended value (100 × 0.9 = 90.00000000000001), and
// without this a price exactly on its bucket edge would fall into the bucket
// below it.
const binEdgeEpsilon = 1e-9

// binIndexFor maps a price to its bucket. ok is false for an unknown price (0 or
// negative) and for anything outside the band, whose upper edge is exclusive.
func binIndexFor(price, lo, step float64, binCount int) (int, bool) {
	if price <= 0 || step <= 0 {
		return 0, false
	}
	index := int(math.Floor((price-lo)/step + binEdgeEpsilon))
	if index < 0 || index >= binCount {
		return 0, false
	}
	return index, true
}
