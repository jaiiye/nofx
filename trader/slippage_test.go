package trader

import (
	"math"
	"testing"
)

func TestEstimateBookSlippagePctBuy(t *testing.T) {
	asks := [][]float64{{100, 1}, {101, 1}, {102, 1}}
	bids := [][]float64{{99, 1}}

	// 200 USDT: 1 contract @100 + ~0.99 @101 → avg ≈ 100.502.
	pct, ok := estimateBookSlippagePct(bids, asks, "BUY", 200)
	if !ok {
		t.Fatal("book should cover 200 USDT")
	}
	if math.Abs(pct-0.502) > 0.01 {
		t.Fatalf("slippage = %.4f%%, want ~0.502%%", pct)
	}

	// Covered entirely by the best level → zero slippage.
	pct, ok = estimateBookSlippagePct(bids, asks, "BUY", 100)
	if !ok || pct != 0 {
		t.Fatalf("best-level fill: pct=%.4f ok=%v, want 0/true", pct, ok)
	}
}

func TestEstimateBookSlippagePctSell(t *testing.T) {
	bids := [][]float64{{100, 1}, {99, 1}}
	asks := [][]float64{{101, 1}}

	pct, ok := estimateBookSlippagePct(bids, asks, "SELL", 199)
	if !ok {
		t.Fatal("book should cover 199 USDT")
	}
	if pct <= 0 || pct >= 1 {
		t.Fatalf("sell slippage = %.4f%%, want in (0, 1)", pct)
	}
}

func TestEstimateBookSlippageThinBook(t *testing.T) {
	asks := [][]float64{{100, 1}, {101, 1}} // 201 USDT depth
	if _, ok := estimateBookSlippagePct(nil, asks, "BUY", 500); ok {
		t.Fatal("500 USDT must exceed a 201 USDT book")
	}
	if _, ok := estimateBookSlippagePct(nil, nil, "BUY", 100); ok {
		t.Fatal("empty book must not be tradable")
	}
	// Exact coverage is fine.
	if _, ok := estimateBookSlippagePct(nil, asks, "BUY", 201); !ok {
		t.Fatal("201 USDT must fit a 201 USDT book")
	}
}

func TestCheckVenueSlippageThresholds(t *testing.T) {
	if maxVenueSlippagePct != 0.5 {
		t.Fatalf("slippage ceiling changed; update the code-enforced constant and its callers: %v", maxVenueSlippagePct)
	}
	if bookSideForAction("open_long") != "BUY" || bookSideForAction("close_short") != "BUY" ||
		bookSideForAction("open_short") != "SELL" || bookSideForAction("close_long") != "SELL" {
		t.Fatal("book side mapping is wrong")
	}
}
