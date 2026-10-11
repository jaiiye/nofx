package trader

import (
	"fmt"
	"time"

	"nofx/logger"
	"nofx/market"
	"nofx/store"
)

// CheckOpenProtections exposes the code-enforced circuit breakers to callers
// outside the trading loop (chat/agent-initiated orders) so every open intent
// passes the same hard blocks. Empty string means allowed.
func (at *AutoTrader) CheckOpenProtections(symbol string) string {
	return at.checkOpenProtections(symbol)
}

// drawdownPeakCurrent returns the peak and latest value of the equity
// curve selected by the protection's DrawdownBasis ("equity" watches
// total equity incl. unrealized PnL, "balance" watches realized balance).
func (at *AutoTrader) drawdownPeakCurrent(basis string, since time.Time) (peak, current float64, ok bool, err error) {
	if basis == store.DrawdownBasisBalance {
		return at.store.Equity().PeakAndCurrentBalance(at.id, since)
	}
	return at.store.Equity().PeakAndCurrentEquity(at.id, since)
}

// checkOpenProtections evaluates the code-enforced circuit breakers
// before any opening order. It returns a non-empty reason when the
// open must be blocked; an empty string means "allowed".
//
// These are hard blocks (modelled on Freqtrade's protections) — unlike
// the AI-guided limits in the strategy prompt, the model cannot talk
// its way past them. All checks are stateless queries over the
// positions/equity tables, so they recover automatically once the
// condition clears (drawdown recovers, losing closes age out of the
// window, cooldown elapses).
func (at *AutoTrader) checkOpenProtections(symbol string) string {
	if at.store == nil || at.strategyEngine == nil {
		return "" // nothing to consult; don't block on missing wiring
	}
	cfg := at.strategyEngine.GetConfig()
	if cfg == nil {
		return ""
	}
	p := cfg.Protections.Normalized()
	now := time.Now()
	nowMs := now.UnixMilli()
	normSymbol := market.Normalize(symbol)

	// 1. Max drawdown halt — curve below (peak - MaxDrawdownPct%).
	if p.MaxDrawdownPct > 0 {
		since := now.Add(-time.Duration(p.MaxDrawdownLookbackH) * time.Hour)
		peak, current, ok, err := at.drawdownPeakCurrent(p.DrawdownBasis, since)
		if err != nil {
			logger.Warnf("⚠️ [protection] drawdown check failed: %v", err)
		} else if ok && peak > 0 {
			ddPct := (peak - current) / peak * 100
			if ddPct > p.MaxDrawdownPct {
				return fmt.Sprintf("max drawdown halt (%s): %.2f is %.1f%% below peak %.2f (limit %.1f%%)",
					p.DrawdownBasis, current, ddPct, peak, p.MaxDrawdownPct)
			}
		}
	}

	// 2. Losing-closes guard — too many losing closes recently.
	if p.LosingClosesGuardCount > 0 {
		sinceMs := now.Add(-time.Duration(p.LosingClosesGuardLookbackH) * time.Hour).UnixMilli()
		n, err := at.store.Position().CountLosingClosesSince(at.id, sinceMs)
		if err != nil {
			logger.Warnf("⚠️ [protection] losing-closes check failed: %v", err)
		} else if n >= p.LosingClosesGuardCount {
			return fmt.Sprintf("losing-streak halt: %d losing closes in the last %dh (limit %d)",
				n, p.LosingClosesGuardLookbackH, p.LosingClosesGuardCount)
		}
	}

	// 3. Per-symbol cooldown — no immediate re-entry after a close.
	if p.CooldownMinutes > 0 {
		lastClose, err := at.store.Position().LastCloseTime(at.id, normSymbol)
		if err != nil {
			logger.Warnf("⚠️ [protection] cooldown check failed: %v", err)
		} else if lastClose > 0 {
			elapsed := nowMs - lastClose
			cooldownMs := int64(p.CooldownMinutes) * 60 * 1000
			if elapsed < cooldownMs {
				remain := (cooldownMs - elapsed) / 60000
				return fmt.Sprintf("%s cooldown: %d min remaining (cooldown %d min)",
					normSymbol, remain, p.CooldownMinutes)
			}
		}
	}

	// 4. Low-profit pair block — symbol keeps bleeding over the window.
	if p.LowProfitMinTrades > 0 {
		sinceMs := now.Add(-time.Duration(p.LowProfitLookbackH) * time.Hour).UnixMilli()
		pnl, trades, err := at.store.Position().SymbolPnLSince(at.id, normSymbol, sinceMs)
		if err != nil {
			logger.Warnf("⚠️ [protection] low-profit check failed: %v", err)
		} else if trades >= p.LowProfitMinTrades && pnl <= 0 {
			return fmt.Sprintf("%s blocked: %d closes over %dh with cumulative PnL %.2f",
				normSymbol, trades, p.LowProfitLookbackH, pnl)
		}
	}

	return ""
}
