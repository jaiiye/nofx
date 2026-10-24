package store

import (
	"fmt"
	"testing"
	"time"
)

// The liquidation streams are bursty: Binance's all-market stream is throttled
// to roughly one order per symbol per second, which can reach several hundred
// prints per second during a cascade. This benchmark measures the real write
// path (unique index + ON CONFLICT DO NOTHING, 500-row batches) so the batch
// size can be judged against that peak rather than guessed.
func BenchmarkSaveLiquidations(b *testing.B) {
	st, err := New(b.TempDir() + "/nofx.db")
	if err != nil {
		b.Fatalf("open store: %v", err)
	}

	const batch = 500
	base := time.Now().UTC().Add(-time.Duration(b.N*batch) * time.Second)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows := make([]*LiquidationRow, 0, batch)
		for j := 0; j < batch; j++ {
			rows = append(rows, &LiquidationRow{
				Venue:       "binance",
				Symbol:      "BTCUSDT",
				VenueSymbol: "BTCUSDT",
				Timestamp:   base.Add(time.Duration(i*batch+j) * time.Second),
				Side:        "long",
				Price:       60000,
				Qty:         1,
				NotionalUSD: 60000,
			})
		}
		if err := st.MarketMetrics().SaveLiquidations(rows); err != nil {
			b.Fatalf("save: %v", err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(b.N*batch)/b.Elapsed().Seconds(), "rows/s")
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "batches/s")
}

func BenchmarkSaveVenueMetrics(b *testing.B) {
	st, err := New(b.TempDir() + "/nofx.db")
	if err != nil {
		b.Fatalf("open store: %v", err)
	}

	const rowsPerBatch = 39 // 3 venues x 13 symbols
	base := time.Now().UTC().Add(-time.Duration(b.N) * time.Minute)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows := make([]*VenueMetricsRow, 0, rowsPerBatch)
		for j := 0; j < rowsPerBatch; j++ {
			rows = append(rows, &VenueMetricsRow{
				Venue:            fmt.Sprintf("venue-%d", j%3),
				Symbol:           fmt.Sprintf("SYM%dUSDT", j%13),
				Timestamp:        base.Add(time.Duration(i) * time.Minute),
				MarkPrice:        60000,
				OIUSD:            1e9,
				FundingRate1hPct: 0.001,
				FundingIntervalH: 8,
				SourceOK:         true,
			})
		}
		if err := st.MarketMetrics().SaveVenueMetrics(rows); err != nil {
			b.Fatalf("save: %v", err)
		}
	}
	b.ReportMetric(float64(b.N*rowsPerBatch)/b.Elapsed().Seconds(), "rows/s")
}
