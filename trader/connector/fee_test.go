package connector

import "testing"

func TestFeeModelForKnownAndUnknown(t *testing.T) {
	if got := FeeModelFor("okx").TakerRate(); got != 0.0005 {
		t.Fatalf("okx taker rate = %v, want 0.0005", got)
	}
	// Lookup is case- and whitespace-insensitive.
	if got := FeeModelFor(" OKX ").TakerRate(); got != 0.0005 {
		t.Fatalf("okx (padded) taker rate = %v, want 0.0005", got)
	}
	if got := FeeModelFor("nonexistent").TakerRate(); got != 0.0005 {
		t.Fatalf("fallback taker rate = %v, want 0.0005", got)
	}
}

func TestStaticFeeModelEstimate(t *testing.T) {
	m := StaticFeeModel{Maker: 0.0002, Taker: 0.0005, Asset: "USDT"}

	taker := m.Estimate(10000, false)
	if taker.Amount != 5 || taker.Rate != 0.0005 || taker.Asset != "USDT" || taker.Maker {
		t.Fatalf("taker estimate wrong: %+v", taker)
	}

	maker := m.Estimate(10000, true)
	if maker.Amount != 2 || maker.Rate != 0.0002 || !maker.Maker {
		t.Fatalf("maker estimate wrong: %+v", maker)
	}
}
