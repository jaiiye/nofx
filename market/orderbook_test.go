package market

import (
	"testing"

	"nofx/provider/hyperliquid"
)

func TestHyperliquidBookLevels(t *testing.T) {
	l2 := &hyperliquid.L2BookResponse{
		Coin: "xyz:AAPL",
		Levels: [2][]hyperliquid.BookLevel{
			{{Px: "100.5", Sz: "2"}, {Px: "100.0", Sz: "3"}, {Px: "bad", Sz: "1"}, {Px: "99.5", Sz: "4"}},
			{{Px: "101.0", Sz: "1"}, {Px: "101.5", Sz: "5"}},
		},
	}

	bids, asks, err := hyperliquidBookLevels(l2, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The unparseable level is dropped, then the side is truncated to depth.
	if len(bids) != 2 || bids[0].Price != 100.5 || bids[1].Price != 100.0 {
		t.Fatalf("bids = %+v, want [100.5, 100.0] after drop+truncate", bids)
	}
	if len(asks) != 2 || asks[0].Price != 101.0 || asks[1].Price != 101.5 {
		t.Fatalf("asks = %+v, want [101.0, 101.5]", asks)
	}
	if bids[0].Size != 2 || asks[1].Size != 5 {
		t.Fatalf("sizes not parsed: %+v / %+v", bids, asks)
	}
}

func TestHyperliquidBookLevelsDegenerate(t *testing.T) {
	if _, _, err := hyperliquidBookLevels(nil, 5); err == nil {
		t.Fatal("nil response must error")
	}
	if _, _, err := hyperliquidBookLevels(&hyperliquid.L2BookResponse{}, 5); err == nil {
		t.Fatal("missing levels must error")
	}
	// One side entirely unparseable -> error.
	l2 := &hyperliquid.L2BookResponse{
		Levels: [2][]hyperliquid.BookLevel{
			{{Px: "nope", Sz: "1"}},
			{{Px: "101", Sz: "1"}},
		},
	}
	if _, _, err := hyperliquidBookLevels(l2, 5); err == nil {
		t.Fatal("empty parsed side must error")
	}
}

func TestBinanceDepthLimit(t *testing.T) {
	cases := map[int]int{1: 5, 5: 5, 20: 20, 25: 50, 50: 50, 80: 100, 100: 100, 250: 500, 2000: 1000, 0: 5}
	for in, want := range cases {
		if got := binanceDepthLimit(in); got != want {
			t.Fatalf("binanceDepthLimit(%d) = %d, want %d", in, got, want)
		}
	}
}
