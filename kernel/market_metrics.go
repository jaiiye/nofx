package kernel

import (
	"context"
	"time"

	"nofx/logger"
	"nofx/market"
	"nofx/store"
)

// StartMarketMetrics wires the cross-venue collector to the store and starts
// its polling loop. It returns the collector so boot can stop it.
//
// This is the only place a cross-venue reading becomes a stored row. The prompt
// reads the collector's in-memory snapshot (at most a few polls old) and the
// offline replay reads stored rows, so neither path asks a venue directly.
func StartMarketMetrics(ctx context.Context, st *store.Store) *market.VenueMetricsCollector {
	var sink market.VenueMetricsSink
	var cleanup func(days int) (int64, error)

	if st != nil {
		sink = func(snapshot *market.VenueMetricsSnapshot) error {
			rows := make([]*store.VenueMetricsRow, 0, len(snapshot.Quotes))
			for _, quote := range snapshot.Quotes {
				timestamp := snapshot.Timestamp
				if quote.TimestampMs > 0 {
					timestamp = time.UnixMilli(quote.TimestampMs)
				}
				funding1h, _ := market.NormalizeFunding(quote.FundingRate, quote.FundingIntervalH)
				intervalH := quote.FundingIntervalH
				if intervalH <= 0 {
					intervalH = 8
				}
				rows = append(rows, &store.VenueMetricsRow{
					Venue:            quote.Venue,
					Symbol:           market.Normalize(quote.Symbol),
					VenueSymbol:      quote.VenueSymbol,
					Timestamp:        timestamp,
					MarkPrice:        quote.MarkPrice,
					OIBase:           quote.OIBase,
					OIUSD:            quote.OIUSD,
					FundingRate1hPct: funding1h,
					FundingIntervalH: intervalH,
					PremiumPct:       quote.PremiumPct,
					SourceOK:         quote.SourceOK,
				})
			}
			return st.MarketMetrics().SaveVenueMetrics(rows)
		}
		cleanup = func(days int) (int64, error) {
			return st.MarketMetrics().CleanOldRecords(days)
		}
	}

	collector := market.NewVenueMetricsCollector(sink)
	collector.SetCleanup(cleanup)
	market.SetVenueMetricsCollector(collector)
	collector.Start(ctx)
	logger.Infof("📊 Cross-venue metrics started (Binance, Bybit, Hyperliquid; refresh every minute)")
	return collector
}

// StartLiquidationFlow wires both public liquidation streams to the store and
// registers the watcher for the prompt path. It never fails the caller: an
// unreachable venue contributes no prints and the prompt omits the section.
func StartLiquidationFlow(ctx context.Context, st *store.Store) *market.LiquidationWatcher {
	var sink market.LiquidationSink
	if st != nil {
		sink = func(prints []market.LiquidationPrint) error {
			rows := make([]*store.LiquidationRow, 0, len(prints))
			for _, print := range prints {
				rows = append(rows, &store.LiquidationRow{
					Venue:       print.Venue,
					Symbol:      market.Normalize(print.Symbol),
					VenueSymbol: print.VenueSymbol,
					Timestamp:   time.UnixMilli(print.TimestampMs),
					Side:        print.Side,
					Price:       print.Price,
					Qty:         print.Qty,
					NotionalUSD: print.NotionalUSD,
				})
			}
			return st.MarketMetrics().SaveLiquidations(rows)
		}
	}

	watcher := market.StartLiquidationFlow(ctx, sink)
	market.SetLiquidationWatcher(watcher)
	return watcher
}
