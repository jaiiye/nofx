package market

import "math"

// Position structure profile: what a holder distribution means, expressed as
// unsigned quantities.
//
// Deliberately unsigned. The profile says where holders entered and where they
// get liquidated; it does not say which side that favours. Every trading sign
// lives in one factor table (P5) and is calibrated against forward returns
// rather than assumed — so nothing here can quietly bake a direction in.
const (
	// PositionValueAreaShare is the share of cost notional the value area has to
	// cover, following the market-profile convention of roughly 70%.
	PositionValueAreaShare = 0.70
	// PositionFuelWindowPct is how close to the mark price liquidation notional
	// counts as at hand: the fuel a move of this size would start igniting.
	PositionFuelWindowPct = 5.0
	// PositionWallMinSharePct is the smallest bucket share that still reads as a
	// wall or a magnet, so dust is not reported as structure.
	PositionWallMinSharePct = 3.0
)

// PositionProfile is one market's holder structure, derived from its buckets.
//
// Levels are prices; distances and shares are percents. Cost quantities describe
// where holders entered (the walls that defend a price), liquidation quantities
// describe where they are forced out (the fuel a move ignites). The two are kept
// separate because they answer different questions.
type PositionProfile struct {
	MarkPrice float64 `json:"markPrice"`
	BandPct   float64 `json:"bandPct"`
	BandLow   float64 `json:"bandLow"`
	BandHigh  float64 `json:"bandHigh"`
	BinCount  int     `json:"binCount"`
	BinStep   float64 `json:"binStep"`

	TotalCostNotional float64 `json:"totalCostNotional"`
	TotalLiqNotional  float64 `json:"totalLiqNotional"`
	TotalNotional     float64 `json:"totalNotional"`

	// Levels over the cost distribution: the consensus entry (POC) and the range
	// holding most of the cost (value area). The magnet is the nearest bucket that
	// still carries enough cost to matter.
	CostPOC              float64 `json:"costPoc"`
	CostPOCSharePct      float64 `json:"costPocSharePct"`
	ValueAreaLow         float64 `json:"valueAreaLow"`
	ValueAreaHigh        float64 `json:"valueAreaHigh"`
	ValueAreaSharePct    float64 `json:"valueAreaSharePct"`
	DistanceToCostPOCPct float64 `json:"distanceToCostPocPct"`
	Magnet               float64 `json:"magnet"`
	MagnetDistancePct    float64 `json:"magnetDistancePct"`

	// Nearest cost clusters on each side of the mark price, over the combined cost
	// distribution: the price a move has to push through.
	CostWallAbove            float64 `json:"costWallAbove"`
	CostWallAboveDistancePct float64 `json:"costWallAboveDistancePct"`
	CostWallAboveSharePct    float64 `json:"costWallAboveSharePct"`
	CostWallBelow            float64 `json:"costWallBelow"`
	CostWallBelowDistancePct float64 `json:"costWallBelowDistancePct"`
	CostWallBelowSharePct    float64 `json:"costWallBelowSharePct"`

	// Long side: how much of the long cost sits above the mark price (longs in
	// profit, i.e. the supply that can be sold), and where the long liquidations
	// are.
	LongCostNotional     float64 `json:"longCostNotional"`
	LongCostAboveMarkPct float64 `json:"longCostAboveMarkPct"`
	LongLiqNotional      float64 `json:"longLiqNotional"`
	LongLiqAboveMarkPct  float64 `json:"longLiqAboveMarkPct"`
	LongLiqWithinDownPct float64 `json:"longLiqWithinDownPct"`

	// Short side, mirrored: cost below the mark is shorts in profit, and the
	// liquidation fuel that matters sits above the price.
	ShortCostNotional     float64 `json:"shortCostNotional"`
	ShortCostBelowMarkPct float64 `json:"shortCostBelowMarkPct"`
	ShortLiqNotional      float64 `json:"shortLiqNotional"`
	ShortLiqBelowMarkPct  float64 `json:"shortLiqBelowMarkPct"`
	ShortLiqWithinUpPct   float64 `json:"shortLiqWithinUpPct"`

	// The strongest liquidation cluster: the price where the most forced flow
	// would be triggered.
	LiqCluster            float64 `json:"liqCluster"`
	LiqClusterSharePct    float64 `json:"liqClusterSharePct"`
	LiqClusterDistancePct float64 `json:"liqClusterDistancePct"`

	Coverage PositionCoverage `json:"coverage"`
	Bins     []PriceBin       `json:"bins"`
}

// BuildPositionProfile derives the structure quantities from a bucket
// distribution. Coverage travels with the profile on purpose: a profile whose
// coverage is poor must stay recognisable as such wherever it is used.
//
// Above/below the mark price is decided by each bucket's mid price, which is
// exact enough at the bucket widths involved (the band is spread over tens of
// buckets). Returns nil when there is nothing to describe: no buckets, an
// unusable mark price, or a distribution with no notional in it.
func BuildPositionProfile(bins []PriceBin, coverage PositionCoverage, markPrice, bandPct float64) *PositionProfile {
	if len(bins) == 0 || markPrice <= 0 {
		return nil
	}

	profile := &PositionProfile{
		MarkPrice: markPrice,
		BandPct:   bandPct,
		BandLow:   bins[0].Lo,
		BandHigh:  bins[len(bins)-1].Hi,
		BinCount:  len(bins),
		BinStep:   bins[0].Hi - bins[0].Lo,
		Coverage:  coverage,
		Bins:      bins,
	}

	costShares := make([]float64, len(bins))
	liqShares := make([]float64, len(bins))
	fuelWindow := markPrice * PositionFuelWindowPct / 100

	for _, bin := range bins {
		cost := bin.LongCost + bin.ShortCost
		liq := bin.LongLiq + bin.ShortLiq
		profile.TotalCostNotional += cost
		profile.TotalLiqNotional += liq

		profile.LongCostNotional += bin.LongCost
		profile.LongLiqNotional += bin.LongLiq
		profile.ShortCostNotional += bin.ShortCost
		profile.ShortLiqNotional += bin.ShortLiq

		if bin.Px > markPrice {
			profile.LongCostAboveMarkPct += bin.LongCost
			profile.LongLiqAboveMarkPct += bin.LongLiq
			if bin.Px <= markPrice+fuelWindow {
				profile.ShortLiqWithinUpPct += bin.ShortLiq
			}
		} else {
			profile.ShortCostBelowMarkPct += bin.ShortCost
			profile.ShortLiqBelowMarkPct += bin.ShortLiq
			if bin.Px >= markPrice-fuelWindow {
				profile.LongLiqWithinDownPct += bin.LongLiq
			}
		}
	}

	profile.TotalNotional = profile.TotalCostNotional + profile.TotalLiqNotional
	if profile.TotalNotional <= 0 {
		return nil
	}

	for i, bin := range bins {
		costShares[i] = bin.LongCost + bin.ShortCost
		liqShares[i] = bin.LongLiq + bin.ShortLiq
	}

	profile.CostPOC, profile.CostPOCSharePct = concentration(bins, costShares, profile.TotalCostNotional)
	profile.DistanceToCostPOCPct = signedDistancePct(profile.CostPOC, markPrice)
	profile.ValueAreaLow, profile.ValueAreaHigh, profile.ValueAreaSharePct = valueArea(bins, costShares, profile.TotalCostNotional)
	profile.Magnet, profile.MagnetDistancePct = nearestSignificant(bins, costShares, profile.TotalCostNotional, markPrice, 0)

	profile.CostWallAbove, profile.CostWallAboveDistancePct, profile.CostWallAboveSharePct =
		wallOnSide(bins, costShares, profile.TotalCostNotional, markPrice, true)
	profile.CostWallBelow, profile.CostWallBelowDistancePct, profile.CostWallBelowSharePct =
		wallOnSide(bins, costShares, profile.TotalCostNotional, markPrice, false)

	profile.LiqCluster, profile.LiqClusterSharePct = concentration(bins, liqShares, profile.TotalLiqNotional)
	profile.LiqClusterDistancePct = signedDistancePct(profile.LiqCluster, markPrice)

	profile.LongCostAboveMarkPct = pct(profile.LongCostAboveMarkPct, profile.LongCostNotional)
	profile.LongLiqAboveMarkPct = pct(profile.LongLiqAboveMarkPct, profile.LongLiqNotional)
	profile.LongLiqWithinDownPct = pct(profile.LongLiqWithinDownPct, profile.LongLiqNotional)
	profile.ShortCostBelowMarkPct = pct(profile.ShortCostBelowMarkPct, profile.ShortCostNotional)
	profile.ShortLiqBelowMarkPct = pct(profile.ShortLiqBelowMarkPct, profile.ShortLiqNotional)
	profile.ShortLiqWithinUpPct = pct(profile.ShortLiqWithinUpPct, profile.ShortLiqNotional)

	return profile
}

// concentration returns the bucket mid with the largest share and that share.
// Ties keep the lower bucket, so the result does not depend on iteration order.
func concentration(bins []PriceBin, values []float64, total float64) (price, sharePct float64) {
	best, bestValue := 0, -1.0
	for i, value := range values {
		if value > bestValue {
			best, bestValue = i, value
		}
	}
	if bestValue <= 0 {
		return 0, 0
	}
	return bins[best].Px, pct(bestValue, total)
}

// valueArea grows a contiguous range out from the point of control, always
// absorbing the heavier neighbour, until it covers PositionValueAreaShare of the
// distribution. On a tie it expands upwards, which is documented rather than
// left to iteration order.
func valueArea(bins []PriceBin, values []float64, total float64) (low, high, sharePct float64) {
	if total <= 0 || len(bins) == 0 {
		return 0, 0, 0
	}
	peak, peakValue := 0, -1.0
	for i, value := range values {
		if value > peakValue {
			peak, peakValue = i, value
		}
	}
	if peakValue <= 0 {
		return 0, 0, 0
	}

	target := total * PositionValueAreaShare
	covered := peakValue
	lo, hi := peak, peak
	for covered < target && (lo > 0 || hi < len(values)-1) {
		var up, down float64
		if hi < len(values)-1 {
			up = values[hi+1]
		}
		if lo > 0 {
			down = values[lo-1]
		}
		if up >= down && hi < len(values)-1 {
			hi++
			covered += up
			continue
		}
		if lo > 0 {
			lo--
			covered += down
			continue
		}
		break
	}
	return bins[lo].Lo, bins[hi].Hi, pct(covered, total)
}

// nearestSignificant returns the bucket closest to the mark price whose share is
// at least PositionWallMinSharePct. direction 0 searches both sides, +1 above,
// -1 below. Distances are signed so a caller can tell which side it is on, and a
// tie keeps the lower bucket.
func nearestSignificant(bins []PriceBin, values []float64, total, markPrice float64, direction int) (price, distancePct float64) {
	if total <= 0 {
		return 0, 0
	}
	minShare := total * PositionWallMinSharePct / 100
	bestDistance, bestIndex := math.Inf(1), -1
	for i, value := range values {
		if value < minShare {
			continue
		}
		if direction > 0 && bins[i].Px <= markPrice {
			continue
		}
		if direction < 0 && bins[i].Px >= markPrice {
			continue
		}
		distance := math.Abs(bins[i].Px - markPrice)
		if distance < bestDistance {
			bestDistance, bestIndex = distance, i
		}
	}
	if bestIndex < 0 {
		return 0, 0
	}
	return bins[bestIndex].Px, signedDistancePct(bins[bestIndex].Px, markPrice)
}

// wallOnSide reports the nearest significant cost cluster above or below the mark
// price, with its signed distance and its share of the whole cost distribution.
func wallOnSide(bins []PriceBin, values []float64, total, markPrice float64, above bool) (price, distancePct, sharePct float64) {
	direction := -1
	if above {
		direction = 1
	}
	price, distancePct = nearestSignificant(bins, values, total, markPrice, direction)
	if price == 0 {
		return 0, 0, 0
	}
	for i, bin := range bins {
		if bin.Px == price {
			return price, distancePct, pct(values[i], total)
		}
	}
	return price, distancePct, 0
}

// signedDistancePct returns the signed distance of a price from the mark price, so a
// caller can tell which side of the market it is on.
func signedDistancePct(price, markPrice float64) float64 {
	if price <= 0 || markPrice <= 0 {
		return 0
	}
	return (price - markPrice) / markPrice * 100
}

func pct(part, total float64) float64 {
	if total <= 0 {
		return 0
	}
	return part / total * 100
}
