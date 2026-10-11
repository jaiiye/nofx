package store

import (
	"path/filepath"
	"testing"
	"time"
)

func newQueryTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := NewWithConfig(DBConfig{Type: DBTypeSQLite, Path: filepath.Join(t.TempDir(), "query.db")})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return st
}

func TestNetPnLStatsSubtractFeeAndFunding(t *testing.T) {
	st := newQueryTestStore(t)
	defer st.Close()

	now := time.Now().UTC()
	base := now.Add(-2 * time.Hour).UnixMilli()
	exit := now.Add(-time.Hour).UnixMilli()

	// Winner: gross +10, fee 1, funding 0.5 -> net 8.5.
	if _, err := st.Position().CreateFromClosedPnL("t1", "e1", "binance", &ClosedPnLRecord{
		Symbol: "BTCUSDT", Side: "long", EntryPrice: 100, ExitPrice: 110, Quantity: 1,
		RealizedPnL: 10, Fee: 1, FundingFee: 0.5, EntryTime: base, ExitTime: exit,
	}); err != nil {
		t.Fatalf("create winner: %v", err)
	}

	// Gross-positive but net-negative: +0.1 gross, fee 0.5 -> net -0.4 (a loss).
	if _, err := st.Position().CreateFromClosedPnL("t1", "e1", "binance", &ClosedPnLRecord{
		Symbol: "ETHUSDT", Side: "long", EntryPrice: 100, ExitPrice: 100.1, Quantity: 1,
		RealizedPnL: 0.1, Fee: 0.5, EntryTime: base, ExitTime: exit + 60000,
	}); err != nil {
		t.Fatalf("create fee-killed trade: %v", err)
	}

	stats, err := st.Position().GetFullStats("t1")
	if err != nil {
		t.Fatalf("GetFullStats: %v", err)
	}

	if stats.TotalTrades != 2 {
		t.Fatalf("TotalTrades = %d, want 2", stats.TotalTrades)
	}
	if !almostEqual(stats.TotalPnL, 10.1) {
		t.Fatalf("TotalPnL (gross) = %v, want 10.1", stats.TotalPnL)
	}
	if !almostEqual(stats.TotalFee, 1.5) {
		t.Fatalf("TotalFee = %v, want 1.5", stats.TotalFee)
	}
	if !almostEqual(stats.TotalFundingFee, 0.5) {
		t.Fatalf("TotalFundingFee = %v, want 0.5", stats.TotalFundingFee)
	}
	if !almostEqual(stats.NetPnL, 8.1) {
		t.Fatalf("NetPnL = %v, want 8.1", stats.NetPnL)
	}
	if stats.WinTrades != 1 || stats.LossTrades != 1 {
		t.Fatalf("win/loss = %d/%d, want 1/1 (net-based classification)", stats.WinTrades, stats.LossTrades)
	}
	if !almostEqual(stats.WinRate, 50) {
		t.Fatalf("WinRate = %v, want 50", stats.WinRate)
	}
}

func TestAddFundingFeeAccumulates(t *testing.T) {
	st := newQueryTestStore(t)
	defer st.Close()

	now := time.Now().UTC().UnixMilli()
	if err := st.Position().Create(&TraderPosition{
		TraderID: "t1", ExchangeID: "e1", ExchangeType: "binance",
		Symbol: "ETHUSDT", Side: "LONG", Quantity: 1, EntryPrice: 100,
		EntryTime: now, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create open position: %v", err)
	}

	if err := st.Position().AddFundingFee("t1", "ETHUSDT", "LONG", -0.25); err != nil {
		t.Fatalf("AddFundingFee: %v", err)
	}
	if err := st.Position().AddFundingFee("t1", "ETHUSDT", "LONG", -0.10); err != nil {
		t.Fatalf("AddFundingFee: %v", err)
	}

	pos, err := st.Position().GetOpenPositionBySymbol("t1", "ETHUSDT", "LONG")
	if err != nil || pos == nil {
		t.Fatalf("GetOpenPositionBySymbol: pos=%v err=%v", pos, err)
	}
	if !almostEqual(pos.FundingFee, -0.35) {
		t.Fatalf("FundingFee = %v, want -0.35", pos.FundingFee)
	}

	// Unknown symbol is a no-op, not an error.
	if err := st.Position().AddFundingFee("t1", "NOPEUSDT", "LONG", -1); err != nil {
		t.Fatalf("AddFundingFee on missing position should be a no-op, got %v", err)
	}
}

func almostEqual(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}
