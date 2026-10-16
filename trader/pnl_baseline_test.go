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
