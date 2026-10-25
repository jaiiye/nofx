package trader

import (
	"encoding/json"
	"fmt"
	"nofx/kernel"
	"nofx/logger"
	"nofx/market"
	"nofx/provider/hyperliquid"
	"nofx/store"
	"nofx/wallet"
	"strings"
	"time"
)

// runCycle runs one trading cycle (using AI full decision-making)
func (at *AutoTrader) runCycle() error {
	at.callCount++

	logger.Info("\n" + strings.Repeat("=", 70) + "\n")
	logger.Infof("⏰ %s - AI decision cycle #%d", time.Now().Format("2006-01-02 15:04:05"), at.callCount)
	logger.Info(strings.Repeat("=", 70))

	// 0. Check if trader is stopped (early exit to prevent trades after Stop() is called)
	at.isRunningMutex.RLock()
	running := at.isRunning
	at.isRunningMutex.RUnlock()
	if !running {
		at.logInfof("⏹ Trader is stopped, aborting cycle #%d", at.callCount)
		return nil
	}

	// Closes executed in this cycle offset the exchange's lagging position
	// count in the risk gate (close-then-open rebalances must not read as
	// "position full").
	at.successfulClosesInCycle = 0

	// Check USDC balance periodically for claw402 users (every 10 cycles)
	if at.callCount%10 == 0 && store.IsClaw402Config(at.config.AIModel) {
		at.checkClaw402Balance()
	}

	// Create decision record
	record := &store.DecisionRecord{
		ExecutionLog: []string{},
		Success:      true,
	}

	// 1. Check if trading needs to be stopped
	if time.Now().Before(at.stopUntil) {
		remaining := at.stopUntil.Sub(time.Now())
		at.logWarnf("⏸ Risk control: Trading paused, remaining %.0f minutes", remaining.Minutes())
		record.Success = false
		record.ErrorMessage = fmt.Sprintf("Risk control paused, remaining %.0f minutes", remaining.Minutes())
		if err := at.saveDecision(record); err != nil {
			at.logWarnf("⚠ Failed to save decision record: %v", err)
		}
		return nil
	}

	// 2. Daily P&L window + code-enforced daily loss halt. The AI prompt
	// promises a daily-loss stop; enforce it against the net realized PnL
	// recorded since the window opened.
	if time.Since(at.lastResetTime) > 24*time.Hour {
		at.lastResetTime = time.Now()
		logger.Info("📅 Daily P&L window reset")
	}
	if at.checkDailyLossHalt(record) {
		if err := at.saveDecision(record); err != nil {
			at.logWarnf("⚠ Failed to save decision record: %v", err)
		}
		return nil
	}

	// 4. Collect trading context
	ctx, err := at.buildTradingContext()
	if err != nil {
		at.logErrorf("failed to build trading context: %v", err)
		record.Success = false
		record.ErrorMessage = fmt.Sprintf("Failed to build trading context: %v", err)
		if saveErr := at.saveDecision(record); saveErr != nil {
			at.logWarnf("⚠ Failed to save decision record: %v", saveErr)
		}
		return fmt.Errorf("failed to build trading context: %w", err)
	}

	// Save equity snapshot independently (decoupled from AI decision, used for drawing profit curve)
	// NOTE: Must be called BEFORE candidate coins check to ensure equity is always recorded
	at.saveEquitySnapshot(ctx)

	// If no candidate coins available, log but do not error
	if len(ctx.CandidateCoins) == 0 {
		at.logInfof("ℹ️ No candidate coins available, skipping this cycle")
		record.Success = true // Not an error, just no candidate coins
		record.ExecutionLog = append(record.ExecutionLog, "No candidate coins available, cycle skipped")
		record.AccountState = store.AccountSnapshot{
			TotalBalance:          ctx.Account.TotalEquity,
			AvailableBalance:      ctx.Account.AvailableBalance,
			TotalUnrealizedProfit: ctx.Account.UnrealizedPnL,
			PositionCount:         ctx.Account.PositionCount,
			InitialBalance:        at.initialBalance,
		}
		if err := at.saveDecision(record); err != nil {
			at.logWarnf("⚠ Failed to save decision record: %v", err)
		}
		return nil
	}

	logger.Info(strings.Repeat("=", 70))
	for _, coin := range ctx.CandidateCoins {
		record.CandidateCoins = append(record.CandidateCoins, coin.Symbol)
	}

	at.logInfof("📊 Account equity: %.2f USDT | Available: %.2f USDT | Positions: %d",
		ctx.Account.TotalEquity, ctx.Account.AvailableBalance, ctx.Account.PositionCount)

	// 5. Use strategy engine to call AI for decision
	variant := ""
	if sc := at.GetStrategyConfig(); sc != nil {
		variant = kernel.NormalizeModeVariant(sc.TradingMode)
		if variant == "" && strings.TrimSpace(sc.TradingMode) != "" {
			at.logWarnf("⚠️ Unknown trading mode %q in strategy config — using balanced baseline", sc.TradingMode)
		}
	}
	modeLabel := variant
	if modeLabel == "" {
		modeLabel = "balanced"
	}
	at.logInfof("🤖 Requesting AI analysis and decision... [Strategy Engine] (mode: %s)", modeLabel)
	aiDecision, err := at.decisionMaker.Decide(ctx, variant)

	if aiDecision != nil && aiDecision.AIRequestDurationMs > 0 {
		record.AIRequestDurationMs = aiDecision.AIRequestDurationMs
		at.logInfof("⏱️ AI call duration: %.2f seconds", float64(record.AIRequestDurationMs)/1000)
		record.ExecutionLog = append(record.ExecutionLog,
			fmt.Sprintf("AI call duration: %d ms", record.AIRequestDurationMs))
	}

	// Save chain of thought, decisions, and input prompt even if there's an error (for debugging)
	if aiDecision != nil {
		record.SystemPrompt = aiDecision.SystemPrompt // Save system prompt
		record.InputPrompt = aiDecision.UserPrompt
		record.CoTTrace = aiDecision.CoTTrace
		record.RawResponse = aiDecision.RawResponse // Save raw AI response for debugging
		if len(aiDecision.Decisions) > 0 {
			decisionJSON, _ := json.MarshalIndent(aiDecision.Decisions, "", "  ")
			record.DecisionJSON = string(decisionJSON)
		}
	}

	// Record AI charge (track cost regardless of decision outcome)
	if aiDecision != nil && at.store != nil {
		if chargeErr := at.store.AICharge().Record(at.id, at.aiModel, at.config.AIModel); chargeErr != nil {
			at.logWarnf("⚠️ Failed to record AI charge: %v", chargeErr)
		}
	}

	if err != nil {
		at.consecutiveAIFailures++
		record.Success = false
		record.ErrorMessage = fmt.Sprintf("Failed to get AI decision: %v", err)

		// Activate safe mode after 3 consecutive failures
		if at.consecutiveAIFailures >= 3 && !at.safeMode {
			at.safeMode = true
			at.safeModeReason = fmt.Sprintf("AI failed %d consecutive times: %v", at.consecutiveAIFailures, err)
			at.logErrorf("🛡️ SAFE MODE ACTIVATED — AI failed %d times in a row. No new positions will be opened. Existing positions are protected with current stop-loss settings.", at.consecutiveAIFailures)
			at.logErrorf("🛡️ Reason: %v", err)
			at.logErrorf("🛡️ Action: Will keep trying AI each cycle. Safe mode auto-deactivates when AI recovers.")
		}

		// Print system prompt and AI chain of thought (output even with errors for debugging)
		if aiDecision != nil {
			logger.Info("\n" + strings.Repeat("=", 70) + "\n")
			logger.Infof("📋 System prompt (error case)")
			logger.Info(strings.Repeat("=", 70))
			logger.Info(aiDecision.SystemPrompt)
			logger.Info(strings.Repeat("=", 70))

			if aiDecision.CoTTrace != "" {
				logger.Info("\n" + strings.Repeat("-", 70) + "\n")
				logger.Info("💭 AI chain of thought analysis (error case):")
				logger.Info(strings.Repeat("-", 70))
				logger.Info(aiDecision.CoTTrace)
				logger.Info(strings.Repeat("-", 70))
			}
		}

		if saveErr := at.saveDecision(record); saveErr != nil {
			at.logWarnf("⚠ Failed to save decision record: %v", saveErr)
		}

		// In safe mode, don't return error — keep the loop running to retry next cycle
		if at.safeMode {
			at.logWarnf("🛡️ Safe mode: skipping this cycle, will retry in %v", at.config.ScanInterval)
			return nil
		}

		return fmt.Errorf("failed to get AI decision: %w", err)
	}

	// AI succeeded — reset failure counter and deactivate safe mode
	if at.consecutiveAIFailures > 0 {
		at.logInfof("✅ AI recovered after %d consecutive failures", at.consecutiveAIFailures)
	}
	at.consecutiveAIFailures = 0
	if at.safeMode {
		at.logInfof("🛡️ SAFE MODE DEACTIVATED — AI is working again. Resuming normal trading.")
		at.safeMode = false
		at.safeModeReason = ""
	}

	// // 5. Print system prompt
	// logger.Infof("\n" + strings.Repeat("=", 70))
	// logger.Infof("📋 System prompt [template: %s]", at.systemPromptTemplate)
	// logger.Info(strings.Repeat("=", 70))
	// logger.Info(decision.SystemPrompt)
	// logger.Infof(strings.Repeat("=", 70) + "\n")

	// 6. Print AI chain of thought
	// logger.Infof("\n" + strings.Repeat("-", 70))
	// logger.Info("💭 AI chain of thought analysis:")
	// logger.Info(strings.Repeat("-", 70))
	// logger.Info(decision.CoTTrace)
	// logger.Infof(strings.Repeat("-", 70) + "\n")

	// 7. Print AI decisions
	// logger.Infof("📋 AI decision list (%d items):\n", len(kernel.Decisions))
	// for i, d := range kernel.Decisions {
	//     logger.Infof("  [%d] %s: %s - %s", i+1, d.Symbol, d.Action, d.Reasoning)
	//     if d.Action == "open_long" || d.Action == "open_short" {
	//        logger.Infof("      Leverage: %dx | Position: %.2f USDT | Stop loss: %.4f | Take profit: %.4f",
	//           d.Leverage, d.PositionSizeUSD, d.StopLoss, d.TakeProfit)
	//     }
	// }
	logger.Info()
	logger.Info(strings.Repeat("-", 70))
	// 8. Sort decisions: ensure close positions first, then open positions (prevent position stacking overflow)
	logger.Info(strings.Repeat("-", 70))

	// 8. Sort decisions: ensure close positions first, then open positions (prevent position stacking overflow)
	sortedDecisions := sortDecisionsByPriority(aiDecision.Decisions)
	sortedDecisions = at.filterDecisionsToStrategyUniverse(sortedDecisions, ctx)

	logger.Info("🔄 Execution order (optimized): Close positions first → Open positions later")
	for i, d := range sortedDecisions {
		logger.Infof("  [%d] %s %s", i+1, d.Symbol, d.Action)
	}
	logger.Info()

	// Check if trader is stopped before executing any decisions (prevent trades after Stop())
	at.isRunningMutex.RLock()
	running = at.isRunning
	at.isRunningMutex.RUnlock()
	if !running {
		at.logInfof("⏹ Trader stopped before decision execution, aborting cycle #%d", at.callCount)
		return nil
	}

	// Safe mode: filter out open positions, only allow close/hold
	if at.safeMode {
		filtered := make([]kernel.Decision, 0)
		for _, d := range sortedDecisions {
			if d.Action == "open_long" || d.Action == "open_short" {
				at.logWarnf("🛡️ Safe mode: BLOCKED %s %s (no new positions allowed)", d.Action, d.Symbol)
				continue
			}
			filtered = append(filtered, d)
		}
		sortedDecisions = filtered
		if len(sortedDecisions) == 0 {
			at.logInfof("🛡️ Safe mode: all decisions were open positions, nothing to execute")
		}
	}

	// Execute decisions and record results
	executedOpens := make(map[string]bool)
	for _, d := range sortedDecisions {
		// Check if trader is stopped before each decision (allow immediate stop during execution)
		at.isRunningMutex.RLock()
		running = at.isRunning
		at.isRunningMutex.RUnlock()
		if !running {
			at.logInfof("⏹ Trader stopped during decision execution, aborting remaining decisions")
			break
		}

		actionRecord := store.DecisionAction{
			Action:     d.Action,
			Symbol:     d.Symbol,
			Quantity:   0,
			Leverage:   d.Leverage,
			Price:      0,
			StopLoss:   d.StopLoss,
			TakeProfit: d.TakeProfit,
			Confidence: d.Confidence,
			Reasoning:  d.Reasoning,
			Timestamp:  time.Now().UTC(),
			Success:    false,
		}

		// A stale positions cache can hide an order placed earlier in this
		// same cycle from the risk gate's duplicate check — never open the
		// same symbol+side twice in one cycle.
		openKey := ""
		if isOpenDecision(d.Action) {
			openKey = strings.ToUpper(d.Symbol) + ":" + sideLabel(d.Action)
			if executedOpens[openKey] {
				at.logWarnf("🚫 Skipping duplicate %s %s within the same cycle", d.Action, d.Symbol)
				continue
			}
		}

		if err := at.executeDecisionWithRecord(ctx, &d, &actionRecord); err != nil {
			at.logErrorf("❌ Failed to execute decision (%s %s): %v", d.Symbol, d.Action, err)
			actionRecord.Error = err.Error()
			record.ExecutionLog = append(record.ExecutionLog, fmt.Sprintf("❌ %s %s failed: %v", d.Symbol, d.Action, err))
		} else {
			actionRecord.Success = true
			record.ExecutionLog = append(record.ExecutionLog, fmt.Sprintf("✓ %s %s succeeded", d.Symbol, d.Action))
			if openKey != "" {
				executedOpens[openKey] = true
			}
			// Brief delay after successful execution
			time.Sleep(1 * time.Second)
		}

		record.Decisions = append(record.Decisions, actionRecord)
	}

	// 9. Save decision record
	if err := at.saveDecision(record); err != nil {
		at.logWarnf("⚠ Failed to save decision record: %v", err)
	}

	return nil
}

func normalizeUniverseSymbol(symbol string) string {
	return market.Normalize(strings.TrimSpace(symbol))
}

// universeBaseKey returns the bare base ticker used for fuzzy candidate
// matching. The AI sometimes echoes a candidate as "QNTUSDC", "QNT-USDC",
// "QNTUSDT", or even just "QNT" instead of the canonical "xyz:QNT" we
// supplied in the prompt. All of those resolve to the same Hyperliquid
// instrument, so we accept any of them when the base matches an allowed
// candidate's base.
func universeBaseKey(symbol string) string {
	s := strings.ToUpper(strings.TrimSpace(symbol))
	if s == "" {
		return ""
	}
	// Drop any xyz:/XYZ: prefix.
	s = strings.TrimPrefix(s, "XYZ:")
	// Drop common quote suffixes in order of specificity.
	for _, suf := range []string{"-USDC", "-USDT", "USDC", "USDT", "USD"} {
		if strings.HasSuffix(s, suf) && len(s) > len(suf) {
			s = strings.TrimSuffix(s, suf)
			break
		}
	}
	// Normalize alias (TESLA -> TSLA, ROBINHOOD -> HOOD, etc.).
	return hyperliquid.NormalizeXYZAlias(s)
}

func isOpenDecision(action string) bool {
	a := strings.ToLower(strings.TrimSpace(action))
	return a == "open_long" || a == "open_short"
}

func (at *AutoTrader) filterDecisionsToStrategyUniverse(decisions []kernel.Decision, ctx *kernel.Context) []kernel.Decision {
	if ctx == nil || len(decisions) == 0 {
		return decisions
	}

	allowed := make(map[string]bool, len(ctx.CandidateCoins))
	allowedBases := make(map[string]bool, len(ctx.CandidateCoins))
	for _, coin := range ctx.CandidateCoins {
		allowed[normalizeUniverseSymbol(coin.Symbol)] = true
		if base := universeBaseKey(coin.Symbol); base != "" {
			allowedBases[base] = true
		}
	}

	positions := make(map[string]bool, len(ctx.Positions))
	positionBases := make(map[string]bool, len(ctx.Positions))
	for _, pos := range ctx.Positions {
		positions[normalizeUniverseSymbol(pos.Symbol)] = true
		if base := universeBaseKey(pos.Symbol); base != "" {
			positionBases[base] = true
		}
	}

	filtered := make([]kernel.Decision, 0, len(decisions))
	for _, d := range decisions {
		sym := normalizeUniverseSymbol(d.Symbol)
		if sym == "" || sym == "ALL" {
			filtered = append(filtered, d)
			continue
		}

		if allowed[sym] || positions[sym] {
			filtered = append(filtered, d)
			continue
		}

		// Fall back to base-level match so AI outputs like "QNTUSDC" still
		// resolve to candidate "xyz:QNT". Rewrite the decision's symbol to
		// the canonical form so downstream order placement works.
		if base := universeBaseKey(d.Symbol); base != "" {
			if allowedBases[base] || positionBases[base] {
				if canonical := canonicalUniverseSymbolForBase(ctx, base); canonical != "" {
					d.Symbol = canonical
				}
				filtered = append(filtered, d)
				continue
			}
		}

		if isOpenDecision(d.Action) {
			at.logWarnf("🚫 Blocked AI %s for %s: symbol is outside strategy candidate universe", d.Action, d.Symbol)
		} else {
			at.logWarnf("🚫 Dropped AI decision for %s: symbol is outside strategy candidate universe", d.Symbol)
		}
	}
	return filtered
}

// canonicalUniverseSymbolForBase finds the canonical candidate or position
// symbol that has the given base. Used to rewrite an AI decision's symbol
// (e.g. "QNTUSDC") to the form our order pipeline expects ("xyz:QNT").
func canonicalUniverseSymbolForBase(ctx *kernel.Context, base string) string {
	for _, coin := range ctx.CandidateCoins {
		if universeBaseKey(coin.Symbol) == base {
			return coin.Symbol
		}
	}
	for _, pos := range ctx.Positions {
		if universeBaseKey(pos.Symbol) == base {
			return pos.Symbol
		}
	}
	return ""
}

// buildTradingContext builds trading context
func (at *AutoTrader) buildTradingContext() (*kernel.Context, error) {
	// 1. Get account information. The connector is the only authority on what
	// its venue's fields mean; consumers must not re-derive equity.
	snapshot, err := at.readAccountSnapshot()
	if err != nil {
		return nil, fmt.Errorf("failed to get account state: %w", err)
	}
	account := snapshot.Balance
	positions := snapshot.Positions

	if !account.Complete {
		at.logWarnf("⚠ Account data incomplete for %s — sizing and PnL may be off", account.Venue)
	}
	if err := account.Validate(); err != nil {
		at.logWarnf("⚠ %v", err)
	}

	totalWalletBalance := account.Wallet
	totalUnrealizedProfit := account.UnrealizedPnL
	availableBalance := account.AvailableMargin
	totalEquity := account.Equity

	// 2. Get position information
	// Reconcile the recorded leverage with the venue's figure: positions built
	// from the fill stream start with a placeholder, and a wrong leverage makes
	// every stored PnL% (net/margin) wrong by the same factor.
	at.reconcilePositionLeverage(positions)

	var positionInfos []kernel.PositionInfo
	totalMarginUsed := 0.0

	// Current position key set (for cleaning up closed position records)
	currentPositionKeys := make(map[string]bool)

	for _, pos := range positions {
		symbol := pos["symbol"].(string)
		side := pos["side"].(string)
		entryPrice := pos["entryPrice"].(float64)
		markPrice := pos["markPrice"].(float64)
		quantity := pos["positionAmt"].(float64)
		if quantity < 0 {
			quantity = -quantity // Short position quantity is negative, convert to positive
		}

		// Skip closed positions (quantity = 0), prevent "ghost positions" from being passed to AI
		if quantity == 0 {
			continue
		}

		unrealizedPnl := pos["unRealizedProfit"].(float64)
		liquidationPrice := pos["liquidationPrice"].(float64)

		// Calculate margin used (estimated)
		leverage := 10 // Default value, should actually be fetched from position info
		if lev, ok := pos["leverage"].(float64); ok {
			leverage = int(lev)
		}
		marginUsed := (quantity * markPrice) / float64(leverage)
		totalMarginUsed += marginUsed

		// Calculate P&L percentage (based on margin, considering leverage)
		pnlPct := calculatePnLPercentage(unrealizedPnl, marginUsed)

		// Get position open time from exchange (preferred) or fallback to local tracking
		posKey := symbol + "_" + side
		currentPositionKeys[posKey] = true

		var updateTime int64
		// Priority 1: Get from database (trader_positions table) - most accurate
		if at.store != nil {
			if dbPos, err := at.store.Position().GetOpenPositionBySymbol(at.id, symbol, side); err == nil && dbPos != nil {
				if dbPos.EntryTime > 0 {
					updateTime = dbPos.EntryTime
				}
			}
		}
		// Priority 2: Get from exchange API (Bybit: createdTime, OKX: createdTime)
		if updateTime == 0 {
			if createdTime, ok := pos["createdTime"].(int64); ok && createdTime > 0 {
				updateTime = createdTime
			}
		}
		// Priority 3: Fallback to local tracking
		if updateTime == 0 {
			if _, exists := at.positionFirstSeenTime[posKey]; !exists {
				at.positionFirstSeenTime[posKey] = time.Now().UnixMilli()
			}
			updateTime = at.positionFirstSeenTime[posKey]
		}

		// Get peak profit rate for this position. The cache stores the PRICE move
		// (the drawdown monitor's thresholds are price percentages); PnL% in the
		// prompt and the UI is measured against margin, so scale it to the same
		// unit here — otherwise the model sees a peak below the current PnL.
		at.peakPnLCacheMutex.RLock()
		peakPnlPct := at.peakPnLCache[posKey]
		at.peakPnLCacheMutex.RUnlock()
		peakPnlPct *= float64(leverage)

		positionInfos = append(positionInfos, kernel.PositionInfo{
			Symbol:           symbol,
			Side:             side,
			EntryPrice:       entryPrice,
			MarkPrice:        markPrice,
			Quantity:         quantity,
			Leverage:         leverage,
			UnrealizedPnL:    unrealizedPnl,
			UnrealizedPnLPct: pnlPct,
			PeakPnLPct:       peakPnlPct,
			LiquidationPrice: liquidationPrice,
			MarginUsed:       marginUsed,
			UpdateTime:       updateTime,
		})
	}

	// Clean up closed position records
	for key := range at.positionFirstSeenTime {
		if !currentPositionKeys[key] {
			delete(at.positionFirstSeenTime, key)
		}
	}

	// 3. Use strategy engine to get candidate coins (must have strategy engine)
	var candidateCoins []kernel.CandidateCoin
	if at.strategyEngine == nil {
		at.logWarnf("⚠️ No strategy engine configured, skipping candidate coins")
	} else {
		coins, err := at.candidateSelector.GetCandidateCoins()
		if err != nil {
			// Log warning but don't fail - equity snapshot should still be saved
			at.logWarnf("⚠️ Failed to get candidate coins: %v (will use empty list)", err)
		} else {
			candidateCoins = coins
			logger.Infof("📋 [%s] Strategy engine fetched candidate coins: %d", at.name, len(candidateCoins))
		}
	}

	// 4. Fold deposits/withdrawals into the PnL baseline before any PnL math, so
	//    the prompt and the UI do not report an external transfer as profit.
	at.reconcilePnLBaseline(totalWalletBalance)

	// Calculate total P&L against the cash-flow neutral baseline
	totalPnL, totalPnLPct := PnLAgainstBaseline(totalEquity, at.initialBalance)

	marginUsedPct := 0.0
	if totalEquity > 0 {
		marginUsedPct = (totalMarginUsed / totalEquity) * 100
	}

	// 5. Get leverage from strategy config
	strategyConfig := at.strategyEngine.GetConfig()
	btcEthLeverage := strategyConfig.RiskControl.BTCETHMaxLeverage
	altcoinLeverage := strategyConfig.RiskControl.AltcoinMaxLeverage
	logger.Infof("📋 [%s] Strategy leverage config: BTC/ETH=%dx, Altcoin=%dx", at.name, btcEthLeverage, altcoinLeverage)

	// 6. Build context
	ctx := &kernel.Context{
		GoCtx:           at.runCtx,
		CurrentTime:     time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
		RuntimeMinutes:  int(time.Since(at.startTime).Minutes()),
		CallCount:       at.callCount,
		BTCETHLeverage:  btcEthLeverage,
		AltcoinLeverage: altcoinLeverage,
		Account: kernel.AccountInfo{
			TotalEquity:      totalEquity,
			Wallet:           totalWalletBalance,
			AvailableBalance: availableBalance,
			UnrealizedPnL:    totalUnrealizedProfit,
			TotalPnL:         totalPnL,
			TotalPnLPct:      totalPnLPct,
			MarginUsed:       totalMarginUsed,
			MarginUsedPct:    marginUsedPct,
			PositionCount:    len(positionInfos),
		},
		Positions:      positionInfos,
		CandidateCoins: candidateCoins,
	}

	// 7. Add recent closed trades (if store is available)
	if at.store != nil {
		// Get recent 10 closed trades for AI context
		recentTrades, err := at.store.Position().GetRecentTrades(at.id, 10)
		if err != nil {
			at.logWarnf("⚠️ Failed to get recent trades: %v", err)
		} else {
			logger.Infof("📊 [%s] Found %d recent closed trades for AI context", at.name, len(recentTrades))
			for _, trade := range recentTrades {
				// Convert Unix timestamps to formatted strings for AI readability
				entryTimeStr := ""
				if trade.EntryTime > 0 {
					entryTimeStr = time.Unix(trade.EntryTime, 0).UTC().Format("01-02 15:04 UTC")
				}
				exitTimeStr := ""
				if trade.ExitTime > 0 {
					exitTimeStr = time.Unix(trade.ExitTime, 0).UTC().Format("01-02 15:04 UTC")
				}

				ctx.RecentOrders = append(ctx.RecentOrders, kernel.RecentOrder{
					Symbol:       trade.Symbol,
					Side:         trade.Side,
					EntryPrice:   trade.EntryPrice,
					ExitPrice:    trade.ExitPrice,
					RealizedPnL:  trade.RealizedPnL,
					PnLPct:       trade.PnLPct,
					EntryTime:    entryTimeStr,
					ExitTime:     exitTimeStr,
					HoldDuration: trade.HoldDuration,
				})
			}
		}
		// Get trading statistics for AI context
		stats, err := at.store.Position().GetFullStats(at.id)
		if err != nil {
			at.logWarnf("⚠️ Failed to get trading stats: %v", err)
		} else if stats == nil {
			at.logWarnf("⚠️ GetFullStats returned nil")
		} else if stats.TotalTrades == 0 {
			at.logWarnf("⚠️ GetFullStats returned 0 trades")
		} else {
			// The max drawdown comes from the account curve, not from a synthetic
			// one. The old figure grew a hard-coded 10000 starting equity by each
			// trade's net PnL, so it was independent of the account's size and
			// ignored unrealized PnL — the model was told "0.2%" while the breaker
			// was acting on 15%.
			maxDrawdownPct := stats.MaxDrawdownPct
			if dd, ddErr := at.store.Equity().MaxDrawdownNAVPct(at.id, time.Time{}); ddErr == nil {
				maxDrawdownPct = dd
			}
			ctx.TradingStats = &kernel.TradingStats{
				TotalTrades:  stats.TotalTrades,
				WinRate:      stats.WinRate,
				ProfitFactor: stats.ProfitFactor,
				SharpeRatio:  stats.SharpeRatio,
				// Net of fees/funding so the prompt's numbers share one
				// caliber with WinRate/ProfitFactor/AvgWin/AvgLoss above.
				TotalPnL:       stats.NetPnL,
				AvgWin:         stats.AvgWin,
				AvgLoss:        stats.AvgLoss,
				MaxDrawdownPct: maxDrawdownPct,
			}
			logger.Infof("📈 [%s] Trading stats: %d trades, %.1f%% win rate, PF=%.2f, Sharpe=%.2f, DD=%.1f%%",
				at.name, stats.TotalTrades, stats.WinRate, stats.ProfitFactor, stats.SharpeRatio, maxDrawdownPct)
		}
	} else {
		at.logWarnf("⚠️ Store is nil, cannot get recent trades")
	}

	// 8. Get quantitative data (if enabled in strategy config)
	if strategyConfig.Indicators.EnableQuantData {
		// Collect symbols to query (candidate coins + position coins)
		symbolsToQuery := make(map[string]bool)
		for _, coin := range candidateCoins {
			symbolsToQuery[coin.Symbol] = true
		}
		for _, pos := range positionInfos {
			symbolsToQuery[pos.Symbol] = true
		}

		symbols := make([]string, 0, len(symbolsToQuery))
		for sym := range symbolsToQuery {
			symbols = append(symbols, sym)
		}

		logger.Infof("📊 [%s] Fetching quantitative data for %d symbols...", at.name, len(symbols))
		ctx.QuantDataMap = at.strategyEngine.FetchQuantDataBatch(symbols)
		logger.Infof("📊 [%s] Successfully fetched quantitative data for %d symbols", at.name, len(ctx.QuantDataMap))
	}

	// 9. Get OI ranking data (market-wide position changes)
	if strategyConfig.Indicators.EnableOIRanking {
		logger.Infof("📊 [%s] Fetching OI ranking data...", at.name)
		ctx.OIRankingData = at.strategyEngine.FetchOIRankingData()
		if ctx.OIRankingData != nil {
			logger.Infof("📊 [%s] OI ranking data ready: %d top, %d low positions",
				at.name, len(ctx.OIRankingData.Top), len(ctx.OIRankingData.Low))
		}
	}

	// 10. Get Taker Flow ranking data (market-wide buy/sell imbalance)
	if strategyConfig.Indicators.EnableTakerFlowRanking {
		logger.Infof("💰 [%s] Fetching taker flow ranking data...", at.name)
		ctx.TakerFlowRanking = at.strategyEngine.FetchTakerFlowRankingData()
		if ctx.TakerFlowRanking != nil {
			logger.Infof("💰 [%s] Taker flow ranking data ready: %d top, %d low",
				at.name, len(ctx.TakerFlowRanking.Top), len(ctx.TakerFlowRanking.Low))
		}
	}

	// 11. Get Price ranking data (market-wide gainers/losers)
	if strategyConfig.Indicators.EnablePriceRanking {
		logger.Infof("📈 [%s] Fetching Price ranking data...", at.name)
		ctx.PriceRankingData = at.strategyEngine.FetchPriceRankingData()
		if ctx.PriceRankingData != nil {
			logger.Infof("📈 [%s] Price ranking data ready for %d durations",
				at.name, len(ctx.PriceRankingData.Durations))
		}
	}

	// 12. Holder cost/liquidation structure (opt-in, Hyperliquid only). It reads
	//     the snapshots the background holder index already stored, so this costs
	//     one indexed query rather than a venue call per symbol. The index is
	//     HL-native, so a trader on another venue must not read it as its own.
	if strategyConfig.Indicators.PositionStructureEnabled() && market.VenueFromExchange(at.exchange) == market.VenueHyperliquid {
		symbolsToQuery := make(map[string]bool)
		for _, coin := range candidateCoins {
			symbolsToQuery[coin.Symbol] = true
		}
		for _, pos := range positionInfos {
			symbolsToQuery[pos.Symbol] = true
		}
		symbols := make([]string, 0, len(symbolsToQuery))
		for sym := range symbolsToQuery {
			symbols = append(symbols, sym)
		}

		ctx.PositionProfileMap = at.loadPositionProfiles(symbols)
		logger.Infof("📊 [%s] Holder structure attached for %d of %d symbols", at.name, len(ctx.PositionProfileMap), len(symbols))
	}

	return ctx, nil
}

// positionProfileMaxAgeRefreshCount is how many index refreshes a stored
// holder-structure snapshot may be old before it is withheld from the prompt.
// Structure that no longer describes the market is worse than no structure at
// all, because nothing in it tells the model it has aged.
const positionProfileMaxAgeRefreshCount = 2

// loadPositionProfiles turns the newest stored holder-structure snapshot of each
// symbol into a profile. Snapshots that are missing, unreadable, empty or older
// than the freshness window are dropped, so the prompt section is simply omitted
// for those symbols.
//
// The keys of the result are the symbols as the caller passed them, not their
// normalized form: the prompt looks its sections up by the same symbol it renders.
func (at *AutoTrader) loadPositionProfiles(symbols []string) map[string]*market.PositionProfile {
	if at.store == nil || len(symbols) == 0 {
		return nil
	}

	normalized := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		if name := market.Normalize(symbol); name != "" {
			normalized = append(normalized, name)
		}
	}
	now := time.Now().UTC()
	snapshots, err := at.store.MarketStructure().LatestForSymbols(normalized, now)
	if err != nil {
		at.logWarnf("⚠ Failed to read holder-structure snapshots: %v", err)
		return nil
	}
	if len(snapshots) == 0 {
		return nil
	}

	maxAge := positionProfileMaxAgeRefreshCount * market.HLPositionRefreshInterval()
	profiles := make(map[string]*market.PositionProfile, len(symbols))
	stale := 0
	for _, symbol := range symbols {
		snapshot, ok := snapshots[market.Normalize(symbol)]
		if !ok {
			continue
		}
		if now.Sub(snapshot.Timestamp) > maxAge {
			stale++
			continue
		}
		var bins []market.PriceBin
		if err := json.Unmarshal([]byte(snapshot.BinsJSON), &bins); err != nil {
			at.logWarnf("⚠ Unreadable holder-structure snapshot for %s: %v", symbol, err)
			continue
		}
		profile := market.BuildPositionProfile(bins, market.PositionCoverage{
			AddressesSeen:    snapshot.AddressesSeen,
			PositionsSeen:    snapshot.PositionsSeen,
			PositionsPlaced:  snapshot.PositionsPlaced,
			PositionsOutside: snapshot.PositionsOutside,
			WeightSource:     snapshot.WeightSource,
		}, snapshot.MarkPrice, snapshot.BandPct)
		if profile == nil {
			continue
		}
		profiles[symbol] = profile
	}
	if stale > 0 {
		at.logWarnf("⚠ %d holder-structure snapshot(s) withheld as stale (older than %s)", stale, maxAge)
	}
	return profiles
}

// reconcilePositionLeverage aligns the leverage recorded on open positions with
// the venue's own figure. Positions created by OrderSync (which consumes a fill
// stream that carries no leverage) start from a placeholder, and everything
// derived from the stored value — the positions UI and the net/margin PnL% the
// AI sees — would otherwise be off by the real leverage factor.
func (at *AutoTrader) reconcilePositionLeverage(exchangePositions []map[string]interface{}) {
	if at.store == nil || len(exchangePositions) == 0 {
		return
	}

	type positionKey struct{ symbol, side string }
	venueLeverage := make(map[positionKey]int, len(exchangePositions))
	for _, p := range exchangePositions {
		symbol, _ := p["symbol"].(string)
		side, _ := p["side"].(string)
		leverage, _ := p["leverage"].(float64)
		if symbol == "" || leverage <= 0 {
			continue
		}
		venueLeverage[positionKey{market.Normalize(symbol), strings.ToUpper(side)}] = int(leverage)
	}

	stored, err := at.store.Position().GetOpenPositions(at.id)
	if err != nil {
		at.logWarnf("⚠ Failed to load stored positions for leverage reconciliation: %v", err)
		return
	}
	for _, pos := range stored {
		want, ok := venueLeverage[positionKey{market.Normalize(pos.Symbol), strings.ToUpper(pos.Side)}]
		if !ok || want == pos.Leverage {
			continue
		}
		if err := at.store.Position().UpdateLeverage(at.id, pos.Symbol, pos.Side, want); err != nil {
			at.logWarnf("⚠ Failed to reconcile leverage for %s %s: %v", pos.Symbol, pos.Side, err)
			continue
		}
		at.logInfof("🔧 Leverage reconciled from the venue for %s %s: %dx → %dx", pos.Symbol, pos.Side, pos.Leverage, want)
	}
}

// sortDecisionsByPriority sorts decisions: close positions first, then open positions, finally hold/wait
// This avoids position stacking overflow when changing positions
func sortDecisionsByPriority(decisions []kernel.Decision) []kernel.Decision {
	if len(decisions) <= 1 {
		return decisions
	}

	// Define priority
	getActionPriority := func(action string) int {
		switch action {
		case "close_long", "close_short":
			return 1 // Highest priority: close positions first
		case "open_long", "open_short":
			return 2 // Second priority: open positions later
		case "hold", "wait":
			return 3 // Lowest priority: wait
		default:
			return 999 // Unknown actions at the end
		}
	}

	// Copy decision list
	sorted := make([]kernel.Decision, len(decisions))
	copy(sorted, decisions)

	// Sort by priority
	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if getActionPriority(sorted[i].Action) > getActionPriority(sorted[j].Action) {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	return sorted
}

// checkClaw402Balance checks USDC balance and logs warnings if low
func (at *AutoTrader) checkClaw402Balance() {
	scanMinutes := int(at.config.ScanInterval.Minutes())
	if scanMinutes <= 0 {
		scanMinutes = 3
	}
	dailyCost, _ := store.EstimateRunway(1.0, at.config.CustomModelName, scanMinutes)
	logger.Infof("💰 [%s] Estimated daily AI cost: ~$%.2f (model: %s, interval: %dm)",
		at.name, dailyCost, at.config.CustomModelName, scanMinutes)

	if at.claw402WalletAddr != "" {
		balance, err := wallet.QueryUSDCBalance(at.claw402WalletAddr)
		if err != nil {
			at.logWarnf("⚠️ Failed to query USDC balance: %v", err)
			return
		}

		if balance < 1.0 {
			at.logWarnf("⚠️ Low USDC balance: $%.2f — AI may stop soon!", balance)
		}
		if balance <= 0 {
			at.logErrorf("🚨 USDC balance is ZERO — AI calls will fail!")
		}

		runway := float64(0)
		if dailyCost > 0 {
			runway = balance / dailyCost
		}
		logger.Infof("💰 [%s] USDC Balance: $%.2f | Daily AI cost: ~$%.2f | Runway: ~%.1f days",
			at.name, balance, dailyCost, runway)
	}
}
