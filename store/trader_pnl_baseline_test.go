package store

import "testing"

// The stored reconciliation point has to survive a round trip: the loop adopts it
// after a restart, and a read that silently returned zeros — GORM derived
// `pn_l_baseline_wallet` from the untagged field name, which matched no column —
// made every restart re-anchor the ledger to the current wallet and lose any
// deposit that happened while the process was down.
func TestPnLBaselineRoundTrip(t *testing.T) {
	st, err := New(t.TempDir() + "/nofx.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Trader().Create(&Trader{
		ID:                "t1",
		UserID:            "u1",
		Name:              "n",
		AIModelID:         "m",
		ExchangeID:        "e",
		InitialBalance:    75.386714,
		PnLBaselineWallet: 75.386714,
		PnLBaselineAtMs:   1_791_700_000_000,
	}); err != nil {
		t.Fatalf("create trader: %v", err)
	}

	wallet, atMs, err := st.Trader().GetPnLBaseline("u1", "t1")
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	if wallet != 75.386714 || atMs != 1_791_700_000_000 {
		t.Fatalf("baseline = (%.6f, %d), want the values written with the row", wallet, atMs)
	}

	if err := st.Trader().SetPnLBaseline("u1", "t1", 161.356138, 152.993038, 1_792_000_000_000); err != nil {
		t.Fatalf("set baseline: %v", err)
	}
	wallet, atMs, err = st.Trader().GetPnLBaseline("u1", "t1")
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	if wallet != 152.993038 || atMs != 1_792_000_000_000 {
		t.Fatalf("baseline = (%.6f, %d), want the reconciled point", wallet, atMs)
	}
	trader, err := st.Trader().GetByID("t1")
	if err != nil {
		t.Fatalf("read trader: %v", err)
	}
	if trader.InitialBalance != 161.356138 {
		t.Fatalf("initial balance = %.6f, want the written baseline", trader.InitialBalance)
	}

	// A re-anchor keeps the wallet unobserved: 0 means the starting point is
	// adopted on the next cycle instead of folding the account difference in.
	if err := st.Trader().SetPnLBaseline("u1", "t1", 500, 0, 1_792_100_000_000); err != nil {
		t.Fatalf("re-anchor baseline: %v", err)
	}
	if wallet, _, err = st.Trader().GetPnLBaseline("u1", "t1"); err != nil || wallet != 0 {
		t.Fatalf("re-anchored wallet = (%.6f, %v), want 0", wallet, err)
	}
}
