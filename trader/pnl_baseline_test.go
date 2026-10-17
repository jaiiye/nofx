package trader

import (
	"math"
	"testing"
	"time"

	"nofx/store"
)

// newBaselineTestTrader builds a live AutoTrader on a temp SQLite store whose
// trader row already carries the given capital and reconciliation point. The
// in-memory reconciliation point starts empty, like a fresh process, so every
// test goes through the load path.
func newBaselineTestTrader(t *testing.T, initialBalance, baselineWallet float64) *AutoTrader {
	t.Helper()
	st, err := store.New(t.TempDir() + "/nofx.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	const (
		userID   = "u1"
		traderID = "t1"
	)
	if err := st.Trader().Create(&store.Trader{
		ID:                traderID,
		UserID:            userID,
		Name:              "pnl-test",
		AIModelID:         "m1",
		ExchangeID:        "e1",
		InitialBalance:    initialBalance,
		PnLBaselineWallet: baselineWallet,
		PnLBaselineAtMs:   time.Now().Add(-time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatalf("create trader: %v", err)
	}
	return &AutoTrader{id: traderID, name: "pnl-test", userID: userID, store: st, initialBalance: initialBalance}
}

// addClosedPosition records one closed position with the given net PnL, closed
// now, so it falls inside any reconciliation window that opened earlier.
func addClosedPosition(t *testing.T, at *AutoTrader, netPnL float64) {
	t.Helper()
	pos := &store.TraderPosition{
		TraderID:    at.id,
		Symbol:      "BTC",
		Side:        "LONG",
		Quantity:    1,
		EntryPrice:  100,
		EntryTime:   time.Now().Add(-2 * time.Hour).UnixMilli(),
		RealizedPnL: netPnL,
	}
	if err := at.store.Position().Create(pos); err != nil {
		t.Fatalf("create position: %v", err)
	}
	if err := at.store.Position().ClosePosition(pos.ID, 100, "", netPnL, 0, "test"); err != nil {
		t.Fatalf("close position: %v", err)
	}
}

// A deposit that arrives while the ledger is live is handled by the ordinary
// cycle — no repair path involved — and the write is absolute, so repeating the
// cycle with the same wallet cannot fold the same drift twice.
func TestReconcileFoldsALaterDepositOnce(t *testing.T) {
	at := newBaselineTestTrader(t, 75.386714, 75.386714)
	addClosedPosition(t, at, -8.3631)

	// Wallet 152.993038 = 75.386714 of capital − 8.3631 of losses + 85.969424 deposited.
	at.reconcilePnLBaseline(152.993038)

	if diff := at.initialBalance - 161.356138; diff > 1e-6 || diff < -1e-6 {
		t.Fatalf("initial balance = %.6f, want 161.356138", at.initialBalance)
	}
	storedWallet, storedAt, err := at.store.Trader().GetPnLBaseline(at.userID, at.id)
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	if storedWallet != 152.993038 || storedAt <= 0 {
		t.Fatalf("stored reconciliation point = (%.2f, %d), want the current wallet", storedWallet, storedAt)
	}

	at.reconcilePnLBaseline(152.993038)
	if diff := at.initialBalance - 161.356138; diff > 1e-6 || diff < -1e-6 {
		t.Fatalf("a repeated cycle folded the same drift again: %.6f", at.initialBalance)
	}
}

// A wallet move the realized PnL on record explains is a trading result, not a
// withdrawal: the baseline must stay where it is.
func TestReconcileLeavesTradingResultInPnL(t *testing.T) {
	at := newBaselineTestTrader(t, 75.386714, 75.386714)
	addClosedPosition(t, at, -8.3631)

	at.reconcilePnLBaseline(67.023614)

	if diff := at.initialBalance - 75.386714; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("a trading loss moved the baseline to %.6f", at.initialBalance)
	}
}

// Changing accounts must not be booked as a deposit. After a re-anchor the
// starting wallet is unobserved (0), so the first cycle adopts the balance it
// finds and keeps the declared capital; the bug this guards against folded the
// difference between the two accounts in and turned a 500 balance into an 850
// baseline.
func TestReconcileAdoptsAReanchoredAccount(t *testing.T) {
	at := newBaselineTestTrader(t, 500, 0)

	at.reconcilePnLBaseline(500)

	if at.initialBalance != 500 {
		t.Fatalf("initial balance = %.2f, want the declared 500", at.initialBalance)
	}
	storedWallet, storedAt, err := at.store.Trader().GetPnLBaseline(at.userID, at.id)
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	if storedWallet != 500 || storedAt <= 0 {
		t.Fatalf("stored reconciliation point = (%.2f, %d), want the new account's wallet", storedWallet, storedAt)
	}
}

// The live case that motivated this: an $85.97 deposit on a ~$76 wallet. The
// old code compared equity against the static initial_balance and reported
// +102.94% while the account's realized result was -8.31.
func TestDetectCashFlowDeposit(t *testing.T) {
	// Wallet 76.31 → 162.28 with no realized PnL: the whole move is a deposit.
	cashFlow, ok := detectCashFlow(76.31, 162.28, 0)
	if !ok {
		t.Fatal("an $86 jump on a $76 wallet must be detected")
	}
	if math.Abs(cashFlow-85.97) > 1e-9 {
		t.Fatalf("cash flow = %.2f, want 85.97", cashFlow)
	}
}

func TestDetectCashFlowTradingOnly(t *testing.T) {
	// A wallet move fully explained by realized PnL is NOT a cash flow.
	if cashFlow, ok := detectCashFlow(100, 95.5, -4.5); ok {
		t.Fatalf("trading loss must not be treated as a cash flow (got %.2f)", cashFlow)
	}
	if cashFlow, ok := detectCashFlow(100, 112, 12); ok {
		t.Fatalf("trading profit must not be treated as a cash flow (got %.2f)", cashFlow)
	}
	// Funding/fee noise below the threshold is not a cash flow either.
	if _, ok := detectCashFlow(100, 100.4, 0); ok {
		t.Fatal("sub-threshold drift must be ignored")
	}
}

func TestDetectCashFlowWithdrawalWithTrading(t *testing.T) {
	// Loss of 3 plus a withdrawal of 50 → wallet 100 → 47.
	cashFlow, ok := detectCashFlow(100, 47, -3)
	if !ok {
		t.Fatal("withdrawal must be detected")
	}
	if math.Abs(cashFlow+50) > 1e-9 {
		t.Fatalf("cash flow = %.2f, want -50", cashFlow)
	}
}

func TestCashFlowMaterialityScalesWithWallet(t *testing.T) {
	if got := cashFlowMateriality(20); got != 1.0 {
		t.Fatalf("materiality(20) = %.2f, want the $1 floor", got)
	}
	if got := cashFlowMateriality(1000); got != 10.0 {
		t.Fatalf("materiality(1000) = %.2f, want 1%% of the wallet", got)
	}
}

func TestBaselineFromWallet(t *testing.T) {
	if got := BaselineFromWallet(152.993038, -8.3631); math.Abs(got-161.356138) > 1e-6 {
		t.Fatalf("baseline = %.6f, want 161.356138", got)
	}
	if got := BaselineFromWallet(75.386714, 0); got != 75.386714 {
		t.Fatalf("baseline = %.6f, want the wallet itself", got)
	}
}

func TestPnLAgainstBaseline(t *testing.T) {
	tests := []struct {
		name        string
		totalEquity float64
		baseline    float64
		wantPnL     float64
		wantPct     float64
	}{
		{name: "profit", totalEquity: 161, baseline: 75, wantPnL: 86, wantPct: 114.66666666666667},
		{name: "loss", totalEquity: 66.69, baseline: 75, wantPnL: -8.31, wantPct: -11.08},
		{name: "flat", totalEquity: 100, baseline: 100, wantPnL: 0, wantPct: 0},
		{name: "missing baseline", totalEquity: 100, baseline: 0, wantPnL: 0, wantPct: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pnl, pct := PnLAgainstBaseline(tt.totalEquity, tt.baseline)
			if diff := pnl - tt.wantPnL; diff > 1e-9 || diff < -1e-9 {
				t.Fatalf("pnl = %v, want %v", pnl, tt.wantPnL)
			}
			if diff := pct - tt.wantPct; diff > 1e-9 || diff < -1e-9 {
				t.Fatalf("pct = %v, want %v", pct, tt.wantPct)
			}
		})
	}
}

// A deposit must not read as profit: folding it into the baseline keeps the PnL
// amount at the trading result instead of the wallet jump. The percentage is
// diluted by the larger capital base, which is intended — the amount is what
// has to stay cash-flow neutral.
func TestPnLAgainstBaselineCashFlowNeutral(t *testing.T) {
	const deposit = 86.0
	beforePnL, _ := PnLAgainstBaseline(66.69, 75.0)
	afterPnL, _ := PnLAgainstBaseline(66.69+deposit, 75.0+deposit)

	if diff := afterPnL - beforePnL; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("cash flow changed the PnL amount: before=%v after=%v", beforePnL, afterPnL)
	}
	if afterPnL >= 0 {
		t.Fatalf("expected the trading loss to survive the deposit, got %v", afterPnL)
	}

	// The bug this guards against: an unadjusted baseline reports the deposit
	// itself as profit (the live account showed +102.94% after an $86 deposit).
	unadjustedPnL, unadjustedPct := PnLAgainstBaseline(66.69+deposit, 75.0)
	if unadjustedPnL <= 0 {
		t.Fatalf("expected the unadjusted baseline to show the deposit as profit, got %v", unadjustedPnL)
	}
	if unadjustedPct < 100 {
		t.Fatalf("expected the unadjusted distortion to exceed 100%%, got %v%%", unadjustedPct)
	}
}

// The same live account, one ledger later: the $85.97 deposit landed before this
// ledger existed, so nothing had folded it into the baseline and the dashboard
// kept reading +99.71% (equity 150.56 against a 75.386714 baseline). Adopting the
// current wallet as the starting point cannot undo capital already inside it —
// re-deriving money in is what repairs it.
func TestDeriveBaselineRepairsDepositThatPredatesTheLedger(t *testing.T) {
	const wallet = 152.993038
	const netRealizedAll = -8.3631 // 21 closed positions: -4.2869 realized, 4.0762 fees

	derived, ok := deriveBaseline(wallet, netRealizedAll, 75.386714)
	if !ok {
		t.Fatal("a baseline that predates the ledger must be repaired")
	}
	if math.Abs(derived-161.356138) > 1e-6 {
		t.Fatalf("derived = %.6f, want 161.356138", derived)
	}
	// What is left after the repair is the trading result, not the deposit.
	if pnl := wallet - derived; math.Abs(pnl-netRealizedAll) > 1e-6 {
		t.Fatalf("pnl = %.4f, want the realized result %.4f", pnl, netRealizedAll)
	}
}

func TestDeriveBaselineLeavesConsistentBaselinesAlone(t *testing.T) {
	// A trader that never took external funding: wallet == baseline + realized.
	if _, ok := deriveBaseline(75.386714, 0, 75.386714); ok {
		t.Fatal("a baseline that already matches the wallet must not be rewritten")
	}
	// Fee/funding noise below the materiality threshold is not a deposit either.
	if _, ok := deriveBaseline(100.4, 0, 100); ok {
		t.Fatal("sub-threshold drift must not rewrite the baseline")
	}
	// An unusable derivation (negative capital) keeps the stored baseline.
	if _, ok := deriveBaseline(10, 20, 5); ok {
		t.Fatal("a non-positive derived baseline must be rejected")
	}
}

func TestDeriveBaselineFoldsADepositMadeBeforeTheFirstCycle(t *testing.T) {
	// Funded between creation and the first cycle: no positions on record, so the
	// whole wallet move is capital and must not read as profit.
	derived, ok := deriveBaseline(150.0, 0, 75.0)
	if !ok {
		t.Fatal("a deposit made before the first cycle must be folded in")
	}
	if derived != 150.0 {
		t.Fatalf("derived = %.2f, want 150.00", derived)
	}
}
