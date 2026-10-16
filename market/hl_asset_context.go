package market

import (
	"context"
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
	mu        sync.RWMutex
	bySymbol  map[string]hyperliquid.AssetCtx
	fetchedAt time.Time
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
	fresh := len(hlAssetCtxCache.bySymbol) > 0 && time.Since(hlAssetCtxCache.fetchedAt) < hlAssetCtxTTL
	hlAssetCtxCache.mu.RUnlock()
	if fresh {
		return
	}

	client := hyperliquid.NewClient()
	merged := make(map[string]hyperliquid.AssetCtx)
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
			merged[Normalize(name)] = ctx
		}
	}
	if len(merged) == 0 {
		// Keep the previous snapshot on a total failure rather than blanking
		// the AI's context.
		return
	}

	hlAssetCtxCache.mu.Lock()
	hlAssetCtxCache.bySymbol = merged
	hlAssetCtxCache.fetchedAt = time.Now()
	hlAssetCtxCache.mu.Unlock()
}
