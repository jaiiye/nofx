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

// BaselineFromWallet returns the cash-flow neutral PnL baseline implied by a
// wallet balance and the realized PnL booked up to the same moment: money in is
// whatever the wallet holds that trading did not produce. This single rule backs
// both the live baseline (all-time realized PnL) and the reconstructed baseline
// of a historical equity point (realized PnL up to that point).
func BaselineFromWallet(wallet, realized float64) float64 {
	return wallet - realized
}

// PnLAgainstBaseline returns the cash-flow neutral PnL amount and percentage of
// an equity figure measured against a PnL baseline. A baseline that is not a
// usable capital base reports no PnL rather than a meaningless ratio.
func PnLAgainstBaseline(totalEquity, baseline float64) (amount, pct float64) {
	if baseline <= 0 {
		return 0, 0
	}
	amount = totalEquity - baseline
	return amount, amount / baseline * 100
}

// deriveBaseline re-derives the cash-flow neutral baseline of a trader whose
// stored one predates this ledger — a deposit made before the ledger existed
// would otherwise stay booked as profit forever. It returns ok = false when the
// stored baseline already matches the wallet (nothing to repair) or when the
// derived value is not a usable capital base.
func deriveBaseline(wallet, netRealizedAll, storedBaseline float64) (float64, bool) {
	derived := BaselineFromWallet(wallet, netRealizedAll)
	if derived <= 0 {
		return 0, false
	}
	if math.Abs(derived-storedBaseline) < cashFlowMateriality(wallet) {
		return 0, false
	}
	return derived, true
}

// reconcilePnLBaseline detects and applies deposits/withdrawals for this cycle.
// It must run before the PnL math so the prompt and the UI use the corrected
// baseline. Paper traders are skipped (no external funding). Three cases share
// the stored reconciliation point: a first run derives the baseline (a ledger
// introduced after a deposit existed cannot repair it otherwise), a re-anchored
// account adopts the wallet it finds, and every other cycle folds the wallet move
// that the realized PnL on record does not explain.
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
	if at.pnlBaselineAtMs > 0 && at.pnlBaselineWallet <= 0 {
		// The account was re-anchored (it changed, or its balance was reset to 0)
		// and the starting wallet has not been observed since. Adopt this cycle's
		// wallet without folding the difference, so switching between two accounts
		// that hold different balances is not booked as a deposit.
		at.pnlBaselineWallet = wallet
		at.pnlBaselineAtMs = time.Now().UTC().UnixMilli()
		if err := at.store.Trader().AdvancePnLBaseline(at.userID, at.id, wallet, at.pnlBaselineAtMs); err != nil {
			at.logWarnf("⚠ Failed to re-anchor the PnL baseline: %v", err)
		}
		return
	}
	if at.pnlBaselineAtMs <= 0 {
		// No reconciliation point was ever stored, so the initial_balance on
		// record predates this ledger — and a deposit made before it stayed
		// booked as profit: adopting the wallet below records the starting point
		// but cannot undo the capital that is already inside it. Re-derive money
		// in from the wallet and the realized PnL on record instead.
		if netAll, err := at.store.Position().GetNetRealizedPnLSince(at.id, 0); err != nil {
			at.logWarnf("⚠ Failed to read realized PnL for the baseline repair: %v", err)
		} else if derived, ok := deriveBaseline(wallet, netAll, at.initialBalance); ok {
			nowMs := time.Now().UTC().UnixMilli()
			if err := at.store.Trader().SetPnLBaseline(at.userID, at.id, derived, wallet, nowMs); err != nil {
				at.logWarnf("⚠ Failed to repair the PnL baseline: %v", err)
			} else {
				logger.Infof("💰 PnL baseline repaired: %.2f → %.2f (wallet %.2f, realized on record %+.2f) — capital added before the cash-flow ledger is no longer reported as profit",
					at.initialBalance, derived, wallet, netAll)
				at.logInfof("💰 PnL baseline repaired from %.2f to %.2f", at.initialBalance, derived)
				at.initialBalance = derived
				at.pnlBaselineWallet, at.pnlBaselineAtMs = wallet, nowMs
				return
			}
		}

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

	// A wallet move the realized PnL on record does not explain is an external
	// transfer. Unrecorded funding and rounding below the materiality threshold
	// stay in the result instead of being folded into the baseline.
	cashFlow, detected := detectCashFlow(at.pnlBaselineWallet, wallet, realizedSince)

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
	reconciled := previousBaseline + cashFlow
	at.initialBalance = reconciled
	at.pnlBaselineWallet = wallet
	at.pnlBaselineAtMs = nowMs

	if err := at.store.Trader().SetPnLBaseline(at.userID, at.id, reconciled, wallet, nowMs); err != nil {
		at.logWarnf("⚠ Failed to apply %s of %.2f to the PnL baseline: %v", kind, cashFlow, err)
		// Roll the in-memory adjustment back: the next cycle recomputes the drift
		// from the stored (unchanged) baseline and retries the persistence.
		at.initialBalance = previousBaseline
		at.pnlBaselineWallet = 0
		at.pnlBaselineAtMs = 0
		return
	}

	logger.Infof("💰 External %s detected: %+.2f USDT (wallet %.2f, realized since baseline %+.2f) — PnL baseline %.2f → %.2f",
		kind, cashFlow, wallet, realizedSince, previousBaseline, at.initialBalance)
	at.logInfof("💰 PnL baseline adjusted for %s of %+.2f USDT (now %.2f)", kind, cashFlow, at.initialBalance)
}
