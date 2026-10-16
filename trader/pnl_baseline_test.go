package trader

import (
	"math"
	"testing"
)

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
