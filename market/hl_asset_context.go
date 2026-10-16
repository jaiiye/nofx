package market

import (
	"context"
	"strings"
	"sync"
	"time"

	"nofx/logger"
	"nofx/provider/hyperliquid"
)

// Hyperliquid-native asset context.
//
// Funding, open interest, premium and 24h notional volume all come from the
// venue itself. This matters for two reasons:
//
//   - Binance has no HIP-3 assets, so for xyz stocks/commodities/indices the
//     Binance-only sources returned funding 0 and no OI at all: the AI judged
//     those candidates with no flow context, and the OI liquidity filter was
//     skipped for them.
//   - For assets traded on Hyperliquid, the venue's own funding/OI/volume are
//     the relevant ones.
//
// One request covers every asset on a dex, so results are cached and refreshed
// at most once per minute.
const hlAssetCtxTTL = 60 * time.Second

var hlAssetCtxCache struct {
	mu          sync.RWMutex
	bySymbol    map[string]hyperliquid.AssetCtx
	xyzUniverse map[string]bool // symbols the HIP-3 dex lists, normalized
	fetchedAt   time.Time
}

// hyperliquidXYZUniverse returns the HIP-3 asset set learned from the venue,
// or nil when it has not been populated yet.
//
// It never performs I/O (the set is refreshed as a side effect of the
// asset-context fetch, so hot paths such as symbol checks stay network-free),
// and it returns the map directly: refreshes replace the whole map instead of
// mutating it, so a returned reference is an immutable snapshot.
func hyperliquidXYZUniverse() map[string]bool {
	hlAssetCtxCache.mu.RLock()
	defer hlAssetCtxCache.mu.RUnlock()
	if len(hlAssetCtxCache.xyzUniverse) == 0 {
		return nil
	}
	return hlAssetCtxCache.xyzUniverse
}

// hyperliquidAssetCtx returns the venue context for a symbol (normalized), or
// false when the venue does not list it.
func hyperliquidAssetCtx(symbol string) (hyperliquid.AssetCtx, bool) {
	refreshHyperliquidAssetContexts()

	hlAssetCtxCache.mu.RLock()
	defer hlAssetCtxCache.mu.RUnlock()
	ctx, ok := hlAssetCtxCache.bySymbol[Normalize(symbol)]
	return ctx, ok
}

func refreshHyperliquidAssetContexts() {
	hlAssetCtxCache.mu.RLock()
	lastAttempt := hlAssetCtxCache.fetchedAt
	hlAssetCtxCache.mu.RUnlock()
	if !lastAttempt.IsZero() && time.Since(lastAttempt) < hlAssetCtxTTL {
		return
	}

	client := hyperliquid.NewClient()
	merged := make(map[string]hyperliquid.AssetCtx)
	xyzUniverse := make(map[string]bool)
	for _, dex := range []string{"", "xyz"} {
		contexts, err := client.GetAssetContexts(context.Background(), dex)
		if err != nil {
			label := "default"
			if dex != "" {
				label = dex
			}
			logger.Infof("⚠️ Hyperliquid asset contexts unavailable (%s dex): %v", label, err)
			continue
		}
		for name, ctx := range contexts {
			// Normalize both forms into our symbol space: "BTC" -> "BTCUSDT"
			// and "xyz:SILVER" -> "xyz:SILVER".
			normalized := Normalize(name)
			merged[normalized] = ctx
			// The venue is the authority on which assets the HIP-3 dex lists,
			// so newly listed stocks/commodities need no code change.
			if dex == "xyz" || strings.HasPrefix(strings.ToLower(name), "xyz:") {
				xyzUniverse[normalized] = true
			}
		}
	}

	hlAssetCtxCache.mu.Lock()
	if len(merged) > 0 {
		hlAssetCtxCache.bySymbol = merged
		if len(xyzUniverse) > 0 {
			hlAssetCtxCache.xyzUniverse = xyzUniverse
		}
	}
	// Mark the attempt even on failure: otherwise every symbol of every cycle
	// would pay the request timeout again while the venue is unreachable. The
	// previous snapshot is kept so the AI's context does not blank out.
	hlAssetCtxCache.fetchedAt = time.Now()
	hlAssetCtxCache.mu.Unlock()
}
