package market

import (
	"testing"
	"time"

	"nofx/provider/hyperliquid"
)

// The venue charges funding hourly while Binance charges every 8h: the market
// layer normalizes both to a per-day carry cost. Getting the multiplier wrong
// would misstate the cost of a 3-5 day hold by 3-24x.
func TestMarketContextNormalizesFundingPerDay(t *testing.T) {
	// Seed the venue cache directly (no network in unit tests).
	hlAssetCtxCache.mu.Lock()
	hlAssetCtxCache.bySymbol = map[string]hyperliquid.AssetCtx{
		"BTCUSDT":    {Funding: 0.0001, OpenInterest: 5000, DayNtlVlm: 2_500_000_000, Premium: 0.0003},
		"xyz:SILVER": {Funding: -0.00002, OpenInterest: 120, DayNtlVlm: 45_000_000, Premium: -0.0001},
	}
	hlAssetCtxCache.fetchedAt = time.Now()
	hlAssetCtxCache.mu.Unlock()
	t.Cleanup(func() {
		hlAssetCtxCache.mu.Lock()
		hlAssetCtxCache.bySymbol = nil
		hlAssetCtxCache.fetchedAt = time.Time{}
		hlAssetCtxCache.mu.Unlock()
	})

	// Crypto: hourly 0.0001 → 0.0024/day.
	oi, fundingPerDay, premiumPct, dayNotional := marketContext("BTCUSDT")
	if fundingPerDay < 0.00239 || fundingPerDay > 0.00241 {
		t.Fatalf("BTC funding/day = %v, want ~0.0024 (hourly x 24)", fundingPerDay)
	}
	if oi == nil || oi.Latest != 5000 {
		t.Fatalf("BTC OI = %+v, want Latest 5000", oi)
	}
	if premiumPct < 0.029 || premiumPct > 0.031 {
		t.Fatalf("BTC premium%% = %v, want ~0.03", premiumPct)
	}
	if dayNotional != 2_500_000_000 {
		t.Fatalf("BTC 24h notional = %v", dayNotional)
	}

	// HIP-3 asset: Binance cannot serve it at all, so the venue is the only
	// source of OI/funding/volume for stocks, commodities and indices.
	oi, fundingPerDay, premiumPct, dayNotional = marketContext("xyz:SILVER")
	if oi == nil || oi.Latest != 120 {
		t.Fatalf("xyz:SILVER OI = %+v, want Latest 120", oi)
	}
	if fundingPerDay > -0.00047 || fundingPerDay < -0.00049 {
		t.Fatalf("xyz:SILVER funding/day = %v, want ~-0.00048", fundingPerDay)
	}
	if premiumPct > -0.009 || premiumPct < -0.011 {
		t.Fatalf("xyz:SILVER premium%% = %v, want ~-0.01", premiumPct)
	}
	if dayNotional != 45_000_000 {
		t.Fatalf("xyz:SILVER 24h notional = %v", dayNotional)
	}
}

// The HIP-3 asset list came from a hardcoded map, so a newly listed stock
// needed a code change. The venue's own universe now takes precedence (learned
// as a side effect of the context refresh), with the static list as fallback.
func TestIsXyzDexAssetUsesVenueUniverse(t *testing.T) {
	// Cold cache: an un-prefixed symbol the static list does not know is not
	// recognised (an explicit "xyz:" prefix is authoritative on its own).
	if IsXyzDexAsset("NEWLYLISTED") {
		t.Fatal("unknown asset must not be treated as xyz on a cold cache")
	}

	hlAssetCtxCache.mu.Lock()
	hlAssetCtxCache.xyzUniverse = map[string]bool{"xyz:NEWLYLISTED": true}
	hlAssetCtxCache.mu.Unlock()
	t.Cleanup(func() {
		hlAssetCtxCache.mu.Lock()
		hlAssetCtxCache.xyzUniverse = nil
		hlAssetCtxCache.mu.Unlock()
	})

	// Plain, prefixed and suffixed forms must all resolve.
	for _, sym := range []string{"xyz:NEWLYLISTED", "XYZ:NEWLYLISTED", "NEWLYLISTED"} {
		if !IsXyzDexAsset(sym) {
			t.Errorf("IsXyzDexAsset(%q) = false, want true from the venue universe", sym)
		}
	}
	// Crypto symbols are unaffected (and the call must not recurse: Normalize
	// consults IsXyzDexAsset).
	if IsXyzDexAsset("BTCUSDT") {
		t.Error("crypto symbol misclassified as xyz")
	}
	if Normalize("BTC") != "BTCUSDT" || Normalize("xyz:NEWLYLISTED") != "xyz:NEWLYLISTED" {
		t.Error("Normalize broken for xyz/crypto paths")
	}
}
