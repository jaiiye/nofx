package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nofx/store"
)

// Replays the incident: a wallet of 147.331233 whose all-time net realized PnL
// is -13.9977 implies a 161.328933 capital base, but a phantom deposit and
// withdrawal (the Hyperliquid xyz margin) had left it at 160.457954.
func TestRepairBaselineDryRunThenApply(t *testing.T) {
	st, err := store.NewWithConfig(store.DBConfig{
		Type: store.DBTypeSQLite,
		Path: filepath.Join(t.TempDir(), "repair.db"),
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	tr := &store.Trader{
		ID: "tr1", UserID: "u1", Name: "Autopilot",
		AIModelID: "m1", ExchangeID: "e1", InitialBalance: 160.457954,
	}
	if err := st.Trader().Create(tr); err != nil {
		t.Fatalf("create trader: %v", err)
	}

	now := time.Now().UTC()
	if _, err := st.Position().CreateFromClosedPnL("tr1", "e1", "hyperliquid", &store.ClosedPnLRecord{
		Symbol: "xyz:CL", Side: "long", EntryPrice: 102.85, ExitPrice: 103.48, Quantity: 0.778,
		RealizedPnL: -13, Fee: 0.5, FundingFee: 0.4977, // net -13.9977
		EntryTime: now.Add(-2 * time.Hour).UnixMilli(), ExitTime: now.Add(-time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatalf("create closed position: %v", err)
	}

	if err := st.Equity().Save(&store.EquitySnapshot{
		TraderID: "tr1", Timestamp: now,
		TotalEquity: 147.331233, Balance: 147.331233, UnrealizedPnL: 0,
		InitialBalance: 160.457954,
	}); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	report, changed, err := repairBaseline(st, tr, false)
	if err != nil {
		t.Fatalf("repairBaseline(dry run): %v", err)
	}
	if !changed {
		t.Fatal("expected the dry run to report a needed change")
	}
	if !strings.Contains(report, "161.328933") {
		t.Fatalf("report does not contain the derived baseline:\n%s", report)
	}
	if !strings.Contains(report, "+0.870979") {
		t.Fatalf("report does not contain the delta:\n%s", report)
	}

	// Dry run must not touch the row.
	stored, err := st.Trader().GetByID("tr1")
	if err != nil {
		t.Fatalf("reload trader: %v", err)
	}
	if stored.InitialBalance != 160.457954 {
		t.Fatalf("dry run wrote the baseline: %v", stored.InitialBalance)
	}

	if _, _, err := repairBaseline(st, tr, true); err != nil {
		t.Fatalf("repairBaseline(apply): %v", err)
	}
	stored, err = st.Trader().GetByID("tr1")
	if err != nil {
		t.Fatalf("reload trader: %v", err)
	}
	if diff := stored.InitialBalance - 161.328933; diff > 1e-6 || diff < -1e-6 {
		t.Fatalf("applied baseline = %.6f, want 161.328933", stored.InitialBalance)
	}
	if stored.PnLBaselineWallet != 147.331233 {
		t.Fatalf("reconciliation point = %v, want the current wallet 147.331233", stored.PnLBaselineWallet)
	}
}

func TestRepairBaselineSkipsConsistentTrader(t *testing.T) {
	st, err := store.NewWithConfig(store.DBConfig{
		Type: store.DBTypeSQLite,
		Path: filepath.Join(t.TempDir(), "repair2.db"),
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	tr := &store.Trader{
		ID: "tr2", UserID: "u1", Name: "Consistent",
		AIModelID: "m1", ExchangeID: "e1", InitialBalance: 100,
	}
	if err := st.Trader().Create(tr); err != nil {
		t.Fatalf("create trader: %v", err)
	}
	if err := st.Equity().Save(&store.EquitySnapshot{
		TraderID: "tr2", Timestamp: time.Now().UTC(),
		TotalEquity: 100, Balance: 100, InitialBalance: 100,
	}); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	_, changed, err := repairBaseline(st, tr, false)
	if err != nil {
		t.Fatalf("repairBaseline: %v", err)
	}
	if changed {
		t.Fatal("a consistent baseline must not be reported as changed")
	}
}

func TestRepairBaselineRefusesUnusableValue(t *testing.T) {
	st, err := store.NewWithConfig(store.DBConfig{
		Type: store.DBTypeSQLite,
		Path: filepath.Join(t.TempDir(), "repair3.db"),
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	tr := &store.Trader{
		ID: "tr3", UserID: "u1", Name: "Broken",
		AIModelID: "m1", ExchangeID: "e1", InitialBalance: 100,
	}
	if err := st.Trader().Create(tr); err != nil {
		t.Fatalf("create trader: %v", err)
	}
	now := time.Now().UTC()
	if err := st.Equity().Save(&store.EquitySnapshot{
		TraderID: "tr3", Timestamp: now, TotalEquity: 10, Balance: 10, InitialBalance: 100,
	}); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}
	// A wallet of 10 against 600 of realized profit implies a negative capital
	// base (10 - 600), which is not a usable denominator.
	if _, err := st.Position().CreateFromClosedPnL("tr3", "e1", "binance", &store.ClosedPnLRecord{
		Symbol: "BTCUSDT", Side: "long", EntryPrice: 90, ExitPrice: 100, Quantity: 60,
		RealizedPnL: 600, EntryTime: now.Add(-2 * time.Hour).UnixMilli(), ExitTime: now.Add(-time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatalf("create closed position: %v", err)
	}

	if _, _, err := repairBaseline(st, tr, false); err == nil {
		t.Fatal("expected an error when the derived baseline is not a usable capital base")
	}
}
