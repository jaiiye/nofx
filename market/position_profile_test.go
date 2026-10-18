package market

import (
	"math"
	"testing"
)

// mark 100 with a 10% band over 4 buckets: [90,95) [95,100) [100,105) [105,110).
// Notional per bucket is chosen so every quantity below has a hand-checkable
// answer, including the two ties (magnet, value-area expansion).
func profileFixture() []PriceBin {
	bins, _ := BinPositions(nil, 100, 10, 4)
	bins[0].LongCost, bins[0].ShortCost, bins[0].LongLiq = 100, 100, 300
	bins[1].LongCost, bins[1].LongLiq = 400, 150
	bins[2].LongCost, bins[2].ShortCost, bins[2].ShortLiq = 50, 500, 200
	bins[3].ShortLiq = 400
	return bins
}

func fixtureCoverage() PositionCoverage {
	return PositionCoverage{AddressesSeen: 120, PositionsSeen: 300, PositionsPlaced: 280, PositionsOutside: 20,
		WeightSource: PositionWeightSourceNotional}
}

func almostEqual(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

func TestBuildPositionProfileLevels(t *testing.T) {
	profile := BuildPositionProfile(profileFixture(), fixtureCoverage(), 100, 10)
	if profile == nil {
		t.Fatal("a populated distribution must yield a profile")
	}

	// Costs: 200 / 400 / 550 / 0 -> the point of control is the bucket above the
	// mark, holding 550 of 1150.
	almostEqual(t, "total cost", profile.TotalCostNotional, 1150)
	almostEqual(t, "cost poc", profile.CostPOC, 102.5)
	almostEqual(t, "cost poc share", profile.CostPOCSharePct, 550.0/1150*100)
	almostEqual(t, "distance to poc", profile.DistanceToCostPOCPct, 2.5)

	// The value area absorbs the heavier neighbour (400 below, against 0 above)
	// until it covers 70% of the cost.
	almostEqual(t, "value area low", profile.ValueAreaLow, 95)
	almostEqual(t, "value area high", profile.ValueAreaHigh, 105)
	almostEqual(t, "value area share", profile.ValueAreaSharePct, 950.0/1150*100)

	// The magnet is the nearest bucket carrying at least 3% of the cost; the
	// bucket below and the bucket above are equally close, and the tie keeps the
	// lower one.
	almostEqual(t, "magnet", profile.Magnet, 97.5)
	almostEqual(t, "magnet distance", profile.MagnetDistancePct, -2.5)

	almostEqual(t, "band low", profile.BandLow, 90)
	almostEqual(t, "band high", profile.BandHigh, 110)
	almostEqual(t, "bin step", profile.BinStep, 5)
	almostEqual(t, "bin count", float64(profile.BinCount), 4)
	if profile.Coverage.AddressesSeen != 120 || profile.Coverage.WeightSource == "" {
		t.Fatalf("coverage must travel with the profile, got %+v", profile.Coverage)
	}
	if len(profile.Bins) != 4 {
		t.Fatalf("buckets = %d, want the distribution to be kept", len(profile.Bins))
	}
}

func TestBuildPositionProfileWalls(t *testing.T) {
	profile := BuildPositionProfile(profileFixture(), fixtureCoverage(), 100, 10)

	almostEqual(t, "wall above", profile.CostWallAbove, 102.5)
	almostEqual(t, "wall above distance", profile.CostWallAboveDistancePct, 2.5)
	almostEqual(t, "wall above share", profile.CostWallAboveSharePct, 550.0/1150*100)

	almostEqual(t, "wall below", profile.CostWallBelow, 97.5)
	almostEqual(t, "wall below distance", profile.CostWallBelowDistancePct, -2.5)
	almostEqual(t, "wall below share", profile.CostWallBelowSharePct, 400.0/1150*100)
}

func TestBuildPositionProfileFuelAndOverhang(t *testing.T) {
	profile := BuildPositionProfile(profileFixture(), fixtureCoverage(), 100, 10)

	// Long cost above the mark is longs in profit: 50 of 550.
	almostEqual(t, "long cost above mark", profile.LongCostAboveMarkPct, 50.0/550*100)
	// Short cost below the mark is the mirrored quantity: 100 of 600.
	almostEqual(t, "short cost below mark", profile.ShortCostBelowMarkPct, 100.0/600*100)

	// Fuel inside the 5% window on the dangerous side of each kind of position.
	almostEqual(t, "long liq within down window", profile.LongLiqWithinDownPct, 150.0/450*100)
	almostEqual(t, "short liq within up window", profile.ShortLiqWithinUpPct, 200.0/600*100)
	almostEqual(t, "long liq above mark", profile.LongLiqAboveMarkPct, 0)
	almostEqual(t, "short liq below mark", profile.ShortLiqBelowMarkPct, 0)

	// The largest forced-flow cluster sits at the top of the band: 400 of 1050.
	almostEqual(t, "liq cluster", profile.LiqCluster, 107.5)
	almostEqual(t, "liq cluster share", profile.LiqClusterSharePct, 400.0/1050*100)
	almostEqual(t, "liq cluster distance", profile.LiqClusterDistancePct, 7.5)
	almostEqual(t, "total liquidation", profile.TotalLiqNotional, 1050)
}

func TestBuildPositionProfileValueAreaExpandsUpOnTie(t *testing.T) {
	// A symmetric distribution around the point of control: the two neighbours
	// are equally heavy, and the documented tie rule expands upwards.
	bins, _ := BinPositions(nil, 100, 10, 4)
	bins[0].LongCost = 50
	bins[1].LongCost = 100
	bins[2].LongCost = 50

	profile := BuildPositionProfile(bins, fixtureCoverage(), 100, 10)
	if profile == nil {
		t.Fatal("profile expected")
	}
	almostEqual(t, "value area low", profile.ValueAreaLow, bins[1].Lo)
	almostEqual(t, "value area high", profile.ValueAreaHigh, bins[2].Hi)
}

func TestBuildPositionProfileDegenerateInputs(t *testing.T) {
	if profile := BuildPositionProfile(nil, fixtureCoverage(), 100, 10); profile != nil {
		t.Fatal("no buckets must yield no profile")
	}
	if profile := BuildPositionProfile(profileFixture(), fixtureCoverage(), 0, 10); profile != nil {
		t.Fatal("an unusable mark price must yield no profile")
	}

	empty, _ := BinPositions(nil, 100, 10, 4)
	if profile := BuildPositionProfile(empty, fixtureCoverage(), 100, 10); profile != nil {
		t.Fatal("a distribution with no notional must yield no profile")
	}
}

func TestNearestSignificantIgnoresDust(t *testing.T) {
	bins, _ := BinPositions(nil, 100, 10, 4)
	bins[3].LongCost = 1000 // 100% of the cost, well above the threshold
	bins[2].LongCost = 1    // 0.1%, dust right next to the mark

	price, distance := nearestSignificant(bins, []float64{0, 0, 1, 1000}, 1001, 100, 1)
	almostEqual(t, "significance filter price", price, bins[3].Px)
	almostEqual(t, "significance filter distance", distance, bins[3].Px-100)

	// With nothing significant on the requested side the answer is "no wall".
	price, _ = nearestSignificant(bins, []float64{0, 0, 1, 1000}, 1001, 100, -1)
	almostEqual(t, "empty side price", price, 0)
}
