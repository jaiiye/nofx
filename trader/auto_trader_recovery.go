package trader

import "strings"

// RecoverState rebuilds in-memory trading state from persisted records after a
// restart. This follows crash-only design: no in-flight state is resumed —
// everything is reconstructed from the database and the exchange. It is
// best-effort: each step logs and returns on error rather than failing startup.
func (at *AutoTrader) RecoverState() {
	if at.store == nil {
		return
	}
	at.recoverSafeMode()
	at.recoverPeakPnLCache()
	at.reconcileOpenPositions()
}

// recoverSafeMode restores the consecutive-AI-failure counter and safe mode
// from the tail of the decision history.
func (at *AutoTrader) recoverSafeMode() {
	const lookback = 3
	records, err := at.store.Decision().GetLatestRecords(at.id, lookback)
	if err != nil {
		at.logWarnf("⚠️ [recover] decision history query failed: %v", err)
		return
	}
	failures := 0
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Success {
			break
		}
		// Only model-call failures count toward safe mode. Context-build
		// failures and other errors also set Success=false but say nothing
		// about the AI being down.
		if !strings.HasPrefix(records[i].ErrorMessage, "Failed to get AI decision") {
			break
		}
		failures++
	}
	at.consecutiveAIFailures = failures
	if failures >= lookback {
		at.safeMode = true
		at.safeModeReason = "restored after restart: 3+ consecutive AI failures before shutdown"
		at.logWarnf("🛡️ [recover] safe mode restored (%d consecutive AI failures)", failures)
	}
}

// recoverPeakPnLCache seeds the per-position peak P&L cache from the current
// mark price so the drawdown monitor keeps working after a restart.
func (at *AutoTrader) recoverPeakPnLCache() {
	positions, err := at.trader.GetPositions()
	if err != nil {
		at.logWarnf("⚠️ [recover] positions query failed: %v", err)
		return
	}
	seeded := 0
	for _, p := range positions {
		symbol, _ := p["symbol"].(string)
		side, _ := p["side"].(string)
		entry, _ := p["entryPrice"].(float64)
		mark, _ := p["markPrice"].(float64)
		if symbol == "" || side == "" || entry <= 0 {
			continue
		}
		lev := 10
		if l, ok := p["leverage"].(float64); ok && l > 0 {
			lev = int(l)
		}
		var pct float64
		if side == "long" {
			pct = (mark - entry) / entry * float64(lev) * 100
		} else {
			pct = (entry - mark) / entry * float64(lev) * 100
		}
		at.peakPnLCacheMutex.Lock()
		// Match the monitor's key format ("SYMBOL_side") — normalizePosKey
		// uses a different layout the drawdown monitor never reads.
		at.peakPnLCache[symbol+"_"+strings.ToLower(side)] = pct
		at.peakPnLCacheMutex.Unlock()
		seeded++
	}
	at.logInfof("🔄 [recover] peak P&L cache seeded for %d open positions", seeded)
}

// reconcileOpenPositions compares locally tracked open positions against the
// exchange and logs any discrepancy. It reports only — it never auto-closes,
// to avoid acting on transient exchange state.
func (at *AutoTrader) reconcileOpenPositions() {
	local, err := at.store.Position().GetOpenPositions(at.id)
	if err != nil {
		at.logWarnf("⚠️ [recover] local positions query failed: %v", err)
		return
	}
	ex, err := at.trader.GetPositions()
	if err != nil {
		at.logWarnf("⚠️ [recover] exchange positions query failed: %v", err)
		return
	}

	exchangeSet := make(map[string]bool)
	for _, p := range ex {
		sym, _ := p["symbol"].(string)
		side, _ := p["side"].(string)
		qty, _ := p["positionAmt"].(float64)
		if qty < 0 {
			qty = -qty
		}
		if qty == 0 || sym == "" {
			continue
		}
		exchangeSet[normalizePosKey(sym, side)] = true
	}
	localSet := make(map[string]bool)
	for _, p := range local {
		localSet[normalizePosKey(p.Symbol, p.Side)] = true
	}

	for k := range localSet {
		if !exchangeSet[k] {
			at.logWarnf("⚠️ [recover] %s tracked as OPEN locally but not on the exchange", k)
		}
	}
	for k := range exchangeSet {
		if !localSet[k] {
			at.logWarnf("⚠️ [recover] %s open on the exchange but not tracked locally", k)
		}
	}
	at.logInfof("🔄 [recover] position reconciliation done (local=%d exchange=%d)",
		len(localSet), len(exchangeSet))
}
