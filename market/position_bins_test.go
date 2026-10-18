package market

import "testing"

// mark 100 with a 10% band and 4 buckets gives [90,95) [95,100) [100,105) [105,110).
func TestBinPositionsPlacesCostAndLiquidation(t *testing.T) {
	positions := []AddressPosition{
		{Address: "0xaaa", Side: "long", Size: 10, EntryPrice: 97, LiquidationPx: 92},
		{Address: "0xbbb", Side: "short", Size: 4, EntryPrice: 108, LiquidationPx: 115},
		{Address: "0xccc", Side: "long", Size: 2},
	}

	bins, coverage := BinPositions(positions, 100, 10, 4)
	if len(bins) != 4 {
		t.Fatalf("buckets = %d, want 4", len(bins))
	}

	// Entry 97 -> bucket 1, liquidation 92 -> bucket 0, weight = size x mark = 1000.
	if bins[1].LongCost != 1000 {
		t.Fatalf("bucket 1 long cost = %v, want 1000", bins[1].LongCost)
	}
	if bins[0].LongLiq != 1000 {
		t.Fatalf("bucket 0 long liquidation = %v, want 1000", bins[0].LongLiq)
	}
	// Entry 108 -> bucket 3, weight 400; liquidation 115 is above the band.
	if bins[3].ShortCost != 400 {
		t.Fatalf("bucket 3 short cost = %v, want 400", bins[3].ShortCost)
	}
	if bins[3].ShortLiq != 0 {
		t.Fatalf("a liquidation above the band must not be placed, got %v", bins[3].ShortLiq)
	}

	if coverage.PositionsSeen != 3 || coverage.PositionsPlaced != 2 || coverage.PositionsOutside != 1 {
		t.Fatalf("coverage = %+v, want 3 seen / 2 placed / 1 outside", coverage)
	}
	if coverage.AddressesSeen != 3 {
		t.Fatalf("addresses = %d, want 3", coverage.AddressesSeen)
	}
	if coverage.WeightSource == "" {
		t.Fatal("coverage must state how positions were weighted")
	}
}

func TestBinPositionsBucketEdges(t *testing.T) {
	// The lower edge belongs to its bucket; the upper edge is exclusive.
	bins, _ := BinPositions([]AddressPosition{{Side: "long", Size: 1, EntryPrice: 95}}, 100, 10, 4)
	if bins[1].LongCost != 100 {
		t.Fatalf("entry at the shared edge landed in bucket %v, want bucket 1", bins[1].LongCost)
	}
	_, coverage := BinPositions([]AddressPosition{{Side: "long", Size: 1, EntryPrice: 110}}, 100, 10, 4)
	if coverage.PositionsOutside != 1 {
		t.Fatalf("entry at the exclusive upper edge must be counted outside, got %+v", coverage)
	}
}

func TestBinPositionsDegenerateInputs(t *testing.T) {
	position := []AddressPosition{{Side: "long", Size: 1, EntryPrice: 100}}
	cases := []struct {
		name      string
		markPrice float64
		bandPct   float64
		binCount  int
	}{
		{"zero mark", 0, 10, 4},
		{"negative mark", -100, 10, 4},
		{"zero band", 100, 0, 4},
		{"negative band", 100, -5, 4},
		{"zero buckets", 100, 10, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bins, coverage := BinPositions(position, tc.markPrice, tc.bandPct, tc.binCount)
			if bins != nil {
				t.Fatalf("no band can be described, buckets must be nil, got %d", len(bins))
			}
			if coverage.PositionsPlaced != 0 {
				t.Fatalf("nothing may be placed, got %+v", coverage)
			}
		})
	}
}

func TestBinPositionsSkipsUnknownSideAndSize(t *testing.T) {
	positions := []AddressPosition{
		{Side: "flat", Size: 5, EntryPrice: 100},
		{Side: "", Size: 5, EntryPrice: 100},
		{Side: "long", Size: 0, EntryPrice: 100},
		{Side: "long", Size: -3, EntryPrice: 100},
	}
	_, coverage := BinPositions(positions, 100, 10, 4)
	if coverage.PositionsSeen != 0 {
		t.Fatalf("unusable rows must not be counted, got %+v", coverage)
	}
}

func TestBinPositionsKeepsBandFloorPositive(t *testing.T) {
	// A band wider than the price would drive the lower edge negative; the floor
	// stays positive instead of producing nonsense buckets.
	bins, _ := BinPositions([]AddressPosition{{Side: "long", Size: 1, EntryPrice: 1}}, 1, 300, 4)
	if len(bins) != 4 {
		t.Fatalf("buckets = %d, want 4", len(bins))
	}
	if bins[0].Lo <= 0 {
		t.Fatalf("lower edge = %v, want a positive floor", bins[0].Lo)
	}
}
