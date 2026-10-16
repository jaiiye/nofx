package trader

import (
	"math"
	"time"

	"nofx/logger"
)

// Cash-flow neutral PnL baseline.
//
// Wallet equity moves for exactly two reasons: realized trading PnL (net of
// fees and funding) and external deposits/withdrawals. Comparing the wallet
// move with the realized PnL booked in the same window therefore isolates
// cash flows, which used to be indistinguishable from profit — a live account
// showed +102.94% after an $86 deposit on a $75 balance while its realized
// result was -8.31.
//
// Detected cash flows are folded into the trader's initial_balance, so every
// consumer of PnL / PnL% (the AI prompt, the UI, the daily-loss halt) becomes
// correct without changing its formula.

// cashFlowMateriality is the smallest wallet drift treated as an external cash
// flow. Below it the drift is assumed to be unrecorded funding/rounding noise,
// which keeps noisy accounting from being mistaken for a deposit.
func cashFlowMateriality(wallet float64) float64 {
	return math.Max(1.0, math.Abs(wallet)*0.01)
}

// detectCashFlow returns the external cash flow implied by a wallet move,
// and false when the move is fully explained by realized PnL (or is below the
// materiality threshold).
func detectCashFlow(previousWallet, currentWallet, realizedSince float64) (float64, bool) {
	drift := currentWallet - (previousWallet + realizedSince)
	if math.Abs(drift) < cashFlowMateriality(currentWallet) {
		return 0, false
	}
	return drift, true
}

// cashFlowLedger is implemented by venue adapters that can report external cash
// flows exactly (a ledger of deposits/withdrawals/transfers).
type cashFlowLedger interface {
	CashFlowSince(sinceMs int64) (float64, bool)
}

// externalCashFlowSince resolves the external cash flow for the current window:
// the venue ledger when available, otherwise the wallet-move heuristic.
func (at *AutoTrader) externalCashFlowSince(realizedSince, wallet float64) (float64, bool) {
	if ledger, ok := at.trader.(cashFlowLedger); ok {
		if flow, exact := ledger.CashFlowSince(at.pnlBaselineAtMs); exact {
			return flow, math.Abs(flow) >= cashFlowMateriality(wallet)
		}
	}
	return detectCashFlow(at.pnlBaselineWallet, wallet, realizedSince)
}

// reconcilePnLBaseline detects and applies deposits/withdrawals for this cycle.
// It must run before the PnL math so the prompt and the UI use the corrected
// baseline. Paper traders are skipped (no external funding), and the first run
// just records the starting point.
func (at *AutoTrader) reconcilePnLBaseline(wallet float64) {
	if at.store == nil || at.userID == "" || wallet <= 0 {
		return
	}
	if at.config.PaperTrading {
		return
	}
	if at.pnlBaselineAtMs <= 0 {
		// Adopt a stored reconciliation point if one exists (process restart),
		// otherwise start from the current wallet.
		if wallet0, atMs0, err := at.store.Trader().GetPnLBaseline(at.userID, at.id); err != nil {
			at.logWarnf("⚠ Failed to load the PnL baseline: %v", err)
			return
		} else if atMs0 > 0 {
			at.pnlBaselineWallet, at.pnlBaselineAtMs = wallet0, atMs0
		}
	}
	if at.pnlBaselineAtMs <= 0 {
		// First reconciliation: adopt the current wallet as the starting point.
		at.pnlBaselineWallet = wallet
		at.pnlBaselineAtMs = time.Now().UTC().UnixMilli()
		if err := at.store.Trader().AdvancePnLBaseline(at.userID, at.id, wallet, at.pnlBaselineAtMs); err != nil {
			at.logWarnf("⚠ Failed to initialise the PnL baseline: %v", err)
		}
		return
	}

	realizedSince, err := at.store.Position().GetNetRealizedPnLSince(at.id, at.pnlBaselineAtMs)
	if err != nil {
		at.logWarnf("⚠ Failed to read realized PnL for cash-flow detection: %v", err)
		return
	}

	// Prefer the venue's own ledger: it reports deposits/withdrawals/transfers
	// exactly, unlike the wallet-move heuristic which has to tolerate
	// unrecorded funding and rounding. Venues without a ledger use the
	// heuristic.
	cashFlow, detected := at.externalCashFlowSince(realizedSince, wallet)

	nowMs := time.Now().UTC().UnixMilli()
	if !detected {
		at.pnlBaselineWallet = wallet
		at.pnlBaselineAtMs = nowMs
		if err := at.store.Trader().AdvancePnLBaseline(at.userID, at.id, wallet, nowMs); err != nil {
			at.logWarnf("⚠ Failed to advance the PnL baseline: %v", err)
		}
		return
	}

	kind := "deposit"
	if cashFlow < 0 {
		kind = "withdrawal"
	}
	previousBaseline := at.initialBalance
	at.initialBalance += cashFlow
	at.pnlBaselineWallet = wallet
	at.pnlBaselineAtMs = nowMs

	if err := at.store.Trader().ApplyCashFlow(at.userID, at.id, cashFlow, wallet, nowMs); err != nil {
		at.logWarnf("⚠ Failed to apply %s of %.2f to the PnL baseline: %v", kind, cashFlow, err)
		// Keep the in-memory adjustment: the next cycle retries the persistence
		// and the drift is recomputed from the stored (unchanged) baseline.
		at.initialBalance = previousBaseline
		at.pnlBaselineWallet = 0
		at.pnlBaselineAtMs = 0
		return
	}

	logger.Infof("💰 External %s detected: %+.2f USDT (wallet %.2f, realized since baseline %+.2f) — PnL baseline %.2f → %.2f",
		kind, cashFlow, wallet, realizedSince, previousBaseline, at.initialBalance)
	at.logInfof("💰 PnL baseline adjusted for %s of %+.2f USDT (now %.2f)", kind, cashFlow, at.initialBalance)
}
