package vergex

import "testing"

func TestHeatmapVariantMemo(t *testing.T) {
	if _, ok := LookupHeatmapVariant("xyz:CL"); ok {
		t.Fatal("expected no remembered variant initially")
	}
	RememberHeatmapVariant("xyz:CL", Query{MarketType: "hip3_perp", Symbol: "CL", Chain: "mainnet"})
	q, ok := LookupHeatmapVariant("XYZ:cl")
	if !ok {
		t.Fatal("expected remembered variant to be found (case/prefix insensitive)")
	}
	if q.MarketType != "hip3_perp" || q.Chain != "mainnet" {
		t.Fatalf("unexpected remembered variant: %+v", q)
	}
}

func TestCacheTTLForPrefersPathOverride(t *testing.T) {
	if got := CacheTTLFor(CostLiquidationHeatmapPath); got != defaultDetailTTL {
		t.Fatalf("expected detail TTL %v, got %v", defaultDetailTTL, got)
	}
	if got := CacheTTLFor("/api/v1/vergex/unknown"); got <= 0 {
		t.Fatalf("expected positive default TTL, got %v", got)
	}
}
