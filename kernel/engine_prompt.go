package kernel

import (
	"fmt"
	"math"
	"nofx/market"
	"nofx/provider/hyperliquid"
	"nofx/provider/nofxos"
	"nofx/store"
	"strconv"
	"strings"
	"time"
)

// ============================================================================
// Prompt Building - System Prompt
// ============================================================================

// BuildSystemPrompt builds System Prompt according to strategy configuration
func (e *StrategyEngine) BuildSystemPrompt(accountEquity float64, variant string) string {
	var sb strings.Builder
	riskControl := e.config.RiskControl
	promptSections := e.config.PromptSections
	lang := e.GetLanguage()
	zh := lang == LangChinese
	singleSymbol, primarySymbol := e.singleSymbolInfo()

	// XYZ-only override: when the strategy trades a single Hyperliquid XYZ
	// asset (US stocks, commodities, forex), force the entire prompt to
	// English regardless of the strategy's stored language. Mixing Chinese
	// reasoning with US-equity analysis confuses the LLM (its US-stock
	// training is overwhelmingly English) and the user prompt sections
	// ended up looking incoherent because some sections respect the
	// language flag while legacy stored sections were always English.
	if singleSymbol && market.IsXyzDexAsset(primarySymbol) {
		zh = false
		lang = LangEnglish
	}

	// 0. Data Dictionary & Schema (ensure AI understands all fields)
	sb.WriteString(GetSchemaPrompt(lang))
	sb.WriteString("\n\n")
	sb.WriteString("---\n\n")

	// 1. Role definition (editable; falls back to a generic intro in the
	//    correct language so we don't mix EN headings with ZH custom text).
	if promptSections.RoleDefinition != "" {
		sb.WriteString(promptSections.RoleDefinition)
		sb.WriteString("\n\n")
	} else if zh {
		sb.WriteString("# 你是一名专业的 Hyperliquid USDC 多资产交易 AI\n\n")
		sb.WriteString("你的任务是基于提供的市场数据做出交易决策。\n\n")
	} else {
		sb.WriteString("# You are a professional Hyperliquid USDC multi-asset trading AI\n\n")
		sb.WriteString("Your task is to make trading decisions based on the provided market data.\n\n")
	}

	// 2. Trading mode variant
	writeModeVariant(&sb, variant, zh, riskControl.MinConfidence)

	// 3. Hard constraints (risk control).
	//
	// `singleSymbol` is true for strategies that deliberately trade just one
	// instrument (the quick-create flow, single-asset templates). For those,
	// the "BTC/ETH vs Altcoin" two-tier categorization is irrelevant and
	// actively misleading — we surface a single position-value limit instead.
	btcEthPosValueRatio := riskControl.BTCETHMaxPositionValueRatio
	if btcEthPosValueRatio <= 0 {
		btcEthPosValueRatio = 5.0
	}
	altcoinPosValueRatio := riskControl.AltcoinMaxPositionValueRatio
	if altcoinPosValueRatio <= 0 {
		altcoinPosValueRatio = 1.0
	}

	writeHardConstraints(&sb, accountEquity, riskControl, btcEthPosValueRatio, altcoinPosValueRatio, singleSymbol, primarySymbol, zh)

	// 4. Trading frequency (editable; the default text is mode-aware so the
	//    scalping variant is not contradicted by the day-trade guidance).
	writeTradingFrequency(&sb, variant, zh, promptSections.TradingFrequency)

	// 5. Entry standards (editable). The confidence threshold itself lives in
	//    the hard constraints; repeating the number here only risks drift.
	if promptSections.EntryStandards != "" {
		sb.WriteString(promptSections.EntryStandards)
		if zh {
			sb.WriteString("\n\n你拥有以下指标数据:\n")
		} else {
			sb.WriteString("\n\nYou have the following indicator data:\n")
		}
		e.writeAvailableIndicators(&sb, zh)
		e.writeIndicatorPlaybook(&sb, zh)
		sb.WriteString("\n")
	} else if zh {
		sb.WriteString("# 🎯 入场标准 (严格)\n\n")
		sb.WriteString("只有当多重信号共振时才开仓。你拥有:\n")
		e.writeAvailableIndicators(&sb, zh)
		e.writeIndicatorPlaybook(&sb, zh)
		sb.WriteString("\n请自由使用任何有效的分析方法, 但需达到上方硬约束中的置信度门槛; 避免低质量行为, 如单一指标、信号矛盾、横盘震荡、平仓后立刻再开等。\n\n")
	} else {
		sb.WriteString("# 🎯 Entry Standards (Strict)\n\n")
		sb.WriteString("Only open positions when multiple signals resonate. You have:\n")
		e.writeAvailableIndicators(&sb, zh)
		e.writeIndicatorPlaybook(&sb, zh)
		sb.WriteString("\nFeel free to use any effective analysis method, but the confidence threshold in the hard constraints applies; avoid low-quality behaviors such as single-indicator entries, contradictory signals, sideways chop, or re-entering immediately after a close.\n\n")
	}

	// 6. Decision process (editable)
	if promptSections.DecisionProcess != "" {
		sb.WriteString(promptSections.DecisionProcess)
		sb.WriteString("\n\n")
	} else if zh {
		sb.WriteString("# 📋 决策流程\n\n")
		sb.WriteString("1. 检查持仓 → 是否需要止盈止损\n")
		sb.WriteString("2. 扫描候选标的 + 多周期 → 是否有强信号\n")
		sb.WriteString("3. 先写思维链, 再输出结构化 JSON\n\n")
	} else {
		sb.WriteString("# 📋 Decision Process\n\n")
		sb.WriteString("1. Check positions → take profit / stop loss?\n")
		sb.WriteString("2. Scan candidates + multi-timeframe → are there strong signals?\n")
		sb.WriteString("3. Write chain of thought first, then output structured JSON\n\n")
	}

	// 6b. Exit discipline: the entry playbook above says how to get in; this
	//     says when getting out is allowed at all.
	writeExitDiscipline(&sb, zh, riskControl.MinHoldMinutes)

	// 7. Output format — schema spec stays in English (this is a parser
	//    contract; reasoning copy is localized below).
	writeOutputFormat(&sb, accountEquity, riskControl, btcEthPosValueRatio, altcoinPosValueRatio, singleSymbol, primarySymbol, zh)

	// 8. Custom Prompt.
	//
	// For single-symbol Hyperliquid XYZ assets (US equities, commodities,
	// forex), we replace any stored CustomPrompt with a built-in English
	// stock-trader template. This serves two purposes:
	//   1. The auto-generated CustomPrompt from the quick-create flow used
	//      to be Chinese (matching UI language), which produced an
	//      incoherent mixed-language final prompt that confused the LLM.
	//   2. It guarantees a stock-specific, US-equity-tuned briefing
	//      regardless of when the strategy was first created.
	customPrompt := e.config.CustomPrompt
	if singleSymbol && market.IsXyzDexAsset(primarySymbol) {
		customPrompt = buildXYZStockCustomPrompt(primarySymbol)
	}

	if customPrompt != "" {
		if zh {
			sb.WriteString("# 📌 个性化交易策略\n\n")
		} else {
			sb.WriteString("# 📌 Personalized Trading Strategy\n\n")
		}
		sb.WriteString(customPrompt)
		sb.WriteString("\n\n")
		if zh {
			sb.WriteString("说明: 上述个性化策略是基础规则的补充, 不能违反基础风控原则。\n")
		} else {
			sb.WriteString("Note: the above personalized strategy supplements the basic rules and may not violate the core risk controls.\n")
		}
	}

	return sb.String()
}

// buildXYZStockCustomPrompt returns the canonical English long-only stock
// briefing the agent uses for single-symbol Hyperliquid USDC perpetuals on
// the XYZ board. This is the quick-trade flow's stance: when the user clicks
// the lightning button they want exposure NOW, not a watch-and-wait analyst.
// We bias the AI toward opening a probing long on every flat cycle, with
// risk guardrails to keep the size sane. Symbol is inlined for LLM grounding
// so it never confuses the trading instrument.
func buildXYZStockCustomPrompt(symbol string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Trade ONLY the Hyperliquid USDC perpetual %s (US equity / xyz board).\n\n", symbol))
	sb.WriteString("Core stance: LONG-ONLY, ACTIVE. Do not short. Do not sit on the sidelines.\n\n")

	sb.WriteString("## ⚡ Flat-Account Rule (HIGHEST PRIORITY)\n")
	sb.WriteString("If `Current Positions` is None / empty AND no open exposure on this symbol, you MUST open a long position THIS cycle.\n")
	sb.WriteString("- The user is paying for the quick-trade flow specifically to be in the market — your default is OPEN, not WAIT.\n")
	sb.WriteString("- `wait` / `hold` are only acceptable when you ALREADY have an open position (then it's a hold decision).\n")
	sb.WriteString("- If you cannot find a clear breakout setup, take a probing entry at market with reduced size (40-60% of max position value) and a tight 2% stop. Probing > waiting.\n")
	sb.WriteString("- Set `confidence` >= 75 so backend risk control accepts the order. Justify the 75 in reasoning even if the technical picture is mixed — the strategic decision to be exposed IS the edge for this flow.\n\n")

	sb.WriteString("## Preferred Entry Conditions (use to size up, not to gate the entry)\n")
	sb.WriteString("Size up toward the 80-100% bracket when you see ANY of:\n")
	sb.WriteString("- Break of the prior session/intraday high on rising volume.\n")
	sb.WriteString("- Pullback to a clearly held intraday support (prior swing low, VWAP, EMA20/50) with a bullish reaction bar.\n")
	sb.WriteString("- Sector tape strength (broad US-equity bid, sympathy with peers in the same theme).\n")
	sb.WriteString("- Confirmed catalyst: earnings beat, guide up, sector rotation, macro tailwind.\n\n")

	sb.WriteString("## Risk Guardrails (non-negotiable)\n")
	sb.WriteString("- Per-trade stop-loss: 1.5-3% from entry. ALWAYS set a numeric `stop_loss`.\n")
	sb.WriteString("- Take-profit: target at least R/R 2:1; set a numeric `take_profit`.\n")
	sb.WriteString("- Per-trade notional: <= 25% of account equity (probing 10-15%, full 20-25%).\n")
	sb.WriteString("- Leverage: 2-3x default, never above 5x. Never go all-in.\n")
	sb.WriteString("- Once long, do NOT short the same cycle. Manage the open position first.\n\n")

	sb.WriteString("## Position Management (when already long)\n")
	sb.WriteString("- Trail stop to breakeven once +1R, take partial profits at +2R if momentum stalls.\n")
	sb.WriteString("- Cut quickly if price breaks the stop or the catalyst thesis fails.\n")
	sb.WriteString("- Holding past 30 minutes is fine; flipping in/out every cycle is not.\n\n")

	sb.WriteString("## Discipline\n")
	sb.WriteString(fmt.Sprintf("- Single-symbol mandate: never rotate into another ticker. The decision JSON `symbol` MUST be exactly \"%s\".\n", symbol))
	sb.WriteString("- Before every decision: check current price vs prior pivot, volume vs 5m/1h average, and the broader US-equity tape.\n")
	sb.WriteString("- If positions are open, prioritize managing them over piling on new ones.")
	return sb.String()
}

// singleSymbolInfo returns (true, "ARM-USDC") for static-coin strategies that
// trade exactly one instrument. Multi-symbol strategies return (false, "").
// The flag is used to drop crypto-specific "BTC/ETH vs Altcoin" labeling and
// to put the actual trading symbol into the JSON example.
func (e *StrategyEngine) singleSymbolInfo() (bool, string) {
	coinSource := e.config.CoinSource
	if coinSource.SourceType == "static" && len(coinSource.StaticCoins) == 1 {
		return true, strings.ToUpper(strings.TrimSpace(coinSource.StaticCoins[0]))
	}
	return false, ""
}

// NormalizeModeVariant returns the canonical form of v when it names a
// recognised trading-mode variant, and "" otherwise (balanced baseline).
func NormalizeModeVariant(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "aggressive", "conservative", "scalping":
		return strings.ToLower(strings.TrimSpace(v))
	default:
		return ""
	}
}

// writeTradingFrequency emits the frequency guidance. A user-supplied custom
// section always wins; otherwise the default text is mode-aware — the
// day-trade cadence ("hold ≥ 30-60 min", ">2 trades/hour = overtrading")
// directly contradicts scalping mode, which is built around fast exits.
func writeTradingFrequency(sb *strings.Builder, variant string, zh bool, custom string) {
	if custom != "" {
		sb.WriteString(custom)
		sb.WriteString("\n\n")
		return
	}
	if NormalizeModeVariant(variant) == "scalping" {
		if zh {
			sb.WriteString("# ⏱️ 交易频率提醒 (短线模式)\n\n")
			sb.WriteString("- 短线模式以快进快出为设计目标: 单笔持仓通常数分钟到数十分钟\n")
			sb.WriteString("- 频率可以更高, 但每一单都必须有新信号支撑; 没有信号就不交易\n")
			sb.WriteString("- 动量失效立即离场, 不要对亏损的短线单加仓\n\n")
		} else {
			sb.WriteString("# ⏱️ Trading Frequency Awareness (Scalping)\n\n")
			sb.WriteString("- Scalping is built for quick round trips: positions typically last minutes to tens of minutes\n")
			sb.WriteString("- Higher trade frequency is expected, but every entry needs a fresh signal — no signal, no trade\n")
			sb.WriteString("- Exit as soon as the momentum fails; never add to a losing scalp\n\n")
		}
		return
	}
	if zh {
		sb.WriteString("# ⏱️ 交易频率提醒\n\n")
		sb.WriteString("- 优秀交易员: 每日 2-4 单 ≈ 每小时 0.1-0.2 单\n")
		sb.WriteString("- 每小时 > 2 单 = 过度交易\n")
		sb.WriteString("- 单笔持仓时长 ≥ 30-60 分钟\n")
		sb.WriteString("如果你发现自己每个周期都在交易 → 入场标准过低; 如果不到 30 分钟就平仓 → 太冲动。\n\n")
	} else {
		sb.WriteString("# ⏱️ Trading Frequency Awareness\n\n")
		sb.WriteString("- Excellent traders: 2-4 trades/day ≈ 0.1-0.2 trades/hour\n")
		sb.WriteString("- >2 trades/hour = overtrading\n")
		sb.WriteString("- Single position hold time ≥ 30-60 minutes\n")
		sb.WriteString("If you find yourself trading every cycle → standards too low; if closing positions < 30 minutes → too impulsive.\n\n")
	}
}

// singleSymbolPositionRatio returns the position-value ratio that applies
// when the strategy trades exactly one instrument: the larger of the two
// configured ratios, so the limit is not accidentally clamped to the
// altcoin cap for a stock. Shared by the hard constraints and the output
// example so both quote the same number.
func singleSymbolPositionRatio(btcEthPosValueRatio, altcoinPosValueRatio float64) float64 {
	if btcEthPosValueRatio > altcoinPosValueRatio {
		return btcEthPosValueRatio
	}
	return altcoinPosValueRatio
}

// writeModeVariant writes the mode section. Confidence figures always come
// from the configured MinConfidence — a hardcoded value here previously
// contradicted the hard-constraints section.
func writeModeVariant(sb *strings.Builder, variant string, zh bool, minConfidence int) {
	// Balanced ("") intentionally has no case: the baseline prompt omits the
	// mode section entirely.
	switch NormalizeModeVariant(variant) {
	case "aggressive":
		if zh {
			sb.WriteString(fmt.Sprintf("## 模式: 激进\n- 优先捕捉趋势突破, 达到置信度门槛 (≥ %d) 后可分批建仓\n- 允许更高仓位, 但必须严格止损并说明风险回报比\n\n", minConfidence))
		} else {
			sb.WriteString(fmt.Sprintf("## Mode: Aggressive\n- Prioritize capturing trend breakouts; may scale into a position once it clears the confidence threshold (≥ %d)\n- Allow larger positions, but must strictly set stop-loss and explain the risk-reward ratio\n\n", minConfidence))
		}
	case "conservative":
		if zh {
			sb.WriteString("## 模式: 保守\n- 只有当多重信号共振时才开仓\n- 优先保本, 连亏后必须暂停多个周期\n\n")
		} else {
			sb.WriteString("## Mode: Conservative\n- Open positions only when multiple signals resonate\n- Prioritize capital preservation; pause for multiple periods after consecutive losses\n\n")
		}
	case "scalping":
		if zh {
			sb.WriteString("## 模式: 短线\n- 关注短期动量, 利润目标较小但要求迅速行动\n- 价格两根 K 线内未按预期走 → 立即减仓或止损\n\n")
		} else {
			sb.WriteString("## Mode: Scalping\n- Focus on short-term momentum, smaller profit targets but require quick action\n- If price doesn't move as expected within two bars, immediately reduce position or stop-loss\n\n")
		}
	}
}

func writeHardConstraints(sb *strings.Builder, accountEquity float64, riskControl store.RiskControlConfig, btcEthPosValueRatio, altcoinPosValueRatio float64, singleSymbol bool, primarySymbol string, zh bool) {
	if zh {
		sb.WriteString("# 风控硬约束\n\n")
		sb.WriteString("## 代码强制 (后端校验, 无法绕过):\n")
		sb.WriteString(fmt.Sprintf("- 最大持仓数: 同时 %d 个标的\n", riskControl.MaxPositions))
	} else {
		sb.WriteString("# Hard Constraints (Risk Control)\n\n")
		sb.WriteString("## CODE ENFORCED (backend validation, cannot be bypassed):\n")
		sb.WriteString(fmt.Sprintf("- Max Positions: %d instruments simultaneously\n", riskControl.MaxPositions))
	}

	if singleSymbol {
		// One symbol — use the same combined ratio the hard-constraints
		// section quotes, so the example can never contradict the limit.
		ratio := singleSymbolPositionRatio(btcEthPosValueRatio, altcoinPosValueRatio)
		maxVal := accountEquity * ratio
		symLabel := primarySymbol
		if zh {
			sb.WriteString(fmt.Sprintf("- 单仓最大价值 (%s): %.0f USDT (= 权益 %.0f × %.1fx)\n", symLabel, maxVal, accountEquity, ratio))
		} else {
			sb.WriteString(fmt.Sprintf("- Position Value Limit (%s): max %.0f USDT (= equity %.0f × %.1fx)\n", symLabel, maxVal, accountEquity, ratio))
		}
	} else {
		if zh {
			sb.WriteString(fmt.Sprintf("- 单仓最大价值 (山寨币/股票): %.0f USDT (= 权益 %.0f × %.1fx)\n", accountEquity*altcoinPosValueRatio, accountEquity, altcoinPosValueRatio))
			sb.WriteString(fmt.Sprintf("- 单仓最大价值 (BTC/ETH): %.0f USDT (= 权益 %.0f × %.1fx)\n", accountEquity*btcEthPosValueRatio, accountEquity, btcEthPosValueRatio))
		} else {
			sb.WriteString(fmt.Sprintf("- Position Value Limit (Altcoin/Stock): max %.0f USDT (= equity %.0f × %.1fx)\n", accountEquity*altcoinPosValueRatio, accountEquity, altcoinPosValueRatio))
			sb.WriteString(fmt.Sprintf("- Position Value Limit (BTC/ETH): max %.0f USDT (= equity %.0f × %.1fx)\n", accountEquity*btcEthPosValueRatio, accountEquity, btcEthPosValueRatio))
		}
	}

	if zh {
		sb.WriteString(fmt.Sprintf("- 最大保证金占用: ≤%.0f%%\n", riskControl.MaxMarginUsage*100))
		sb.WriteString(fmt.Sprintf("- 最小下单金额: ≥%.0f USDT\n\n", riskControl.MinPositionSize))
		sb.WriteString("## AI 建议 (推荐遵循):\n")
	} else {
		sb.WriteString(fmt.Sprintf("- Max Margin Usage: ≤%.0f%%\n", riskControl.MaxMarginUsage*100))
		sb.WriteString(fmt.Sprintf("- Min Position Size: ≥%.0f USDT\n\n", riskControl.MinPositionSize))
		sb.WriteString("## AI GUIDED (recommended):\n")
	}

	if singleSymbol {
		lev := riskControl.AltcoinMaxLeverage
		if riskControl.BTCETHMaxLeverage > lev {
			lev = riskControl.BTCETHMaxLeverage
		}
		if zh {
			sb.WriteString(fmt.Sprintf("- 交易杠杆 (%s): 最高 %dx\n", primarySymbol, lev))
		} else {
			sb.WriteString(fmt.Sprintf("- Trading Leverage (%s): max %dx\n", primarySymbol, lev))
		}
	} else {
		if zh {
			sb.WriteString(fmt.Sprintf("- 交易杠杆: 山寨币/股票 最高 %dx | BTC/ETH 最高 %dx\n", riskControl.AltcoinMaxLeverage, riskControl.BTCETHMaxLeverage))
		} else {
			sb.WriteString(fmt.Sprintf("- Trading Leverage: Altcoin/Stock max %dx | BTC/ETH max %dx\n", riskControl.AltcoinMaxLeverage, riskControl.BTCETHMaxLeverage))
		}
	}
	if zh {
		sb.WriteString(fmt.Sprintf("- 风险回报比: ≥1:%.1f (take_profit / stop_loss)\n", riskControl.MinRiskRewardRatio))
		sb.WriteString(fmt.Sprintf("- 最小置信度: ≥%d 才开仓\n\n", riskControl.MinConfidence))
	} else {
		sb.WriteString(fmt.Sprintf("- Risk-Reward Ratio: ≥1:%.1f (take_profit / stop_loss)\n", riskControl.MinRiskRewardRatio))
		sb.WriteString(fmt.Sprintf("- Min Confidence: ≥%d to open position\n\n", riskControl.MinConfidence))
	}

	// Position sizing guidance
	exampleRatio := btcEthPosValueRatio
	if singleSymbol {
		exampleRatio = singleSymbolPositionRatio(btcEthPosValueRatio, altcoinPosValueRatio)
	}
	if zh {
		sb.WriteString("## 仓位大小指引\n")
		sb.WriteString("根据置信度和上面的单仓最大价值算出 `position_size_usd`:\n")
		sb.WriteString("- 高置信 (≥85): 用最大价值的 80-100%%\n")
		sb.WriteString("- 中置信 (70-84): 用最大价值的 50-80%%\n")
		sb.WriteString("- 低置信 (60-69): 用最大价值的 30-50%%\n")
		sb.WriteString(fmt.Sprintf("- 示例: 权益 %.0f × %.1fx = 最大 %.0f USDT\n", accountEquity, exampleRatio, accountEquity*exampleRatio))
		sb.WriteString("- **不要**直接拿 available_balance 当 position_size_usd, 用上面的单仓最大价值!\n\n")
	} else {
		sb.WriteString("## Position Sizing Guidance\n")
		sb.WriteString("Calculate `position_size_usd` from your confidence and the Position Value Limits above:\n")
		sb.WriteString("- High confidence (≥85): use 80-100%% of the position value limit\n")
		sb.WriteString("- Medium confidence (70-84): use 50-80%% of the position value limit\n")
		sb.WriteString("- Low confidence (60-69): use 30-50%% of the position value limit\n")
		sb.WriteString(fmt.Sprintf("- Example: equity %.0f × %.1fx = max %.0f USDT\n", accountEquity, exampleRatio, accountEquity*exampleRatio))
		sb.WriteString("- **DO NOT** just use available_balance as position_size_usd. Use the Position Value Limit!\n\n")
	}
}

func writeOutputFormat(sb *strings.Builder, accountEquity float64, riskControl store.RiskControlConfig, btcEthPosValueRatio, altcoinPosValueRatio float64, singleSymbol bool, primarySymbol string, zh bool) {
	// Output format schema MUST stay English/structural; parser depends on it.
	sb.WriteString("# Output Format (Strictly Follow)\n\n")
	if zh {
		sb.WriteString("**必须使用 XML 标签 <reasoning> 和 <decision> 分隔思维链和决策 JSON, 避免解析错误**\n\n")
	} else {
		sb.WriteString("**Must use XML tags <reasoning> and <decision> to separate chain of thought and decision JSON, avoiding parsing errors**\n\n")
	}
	sb.WriteString("## Format Requirements\n\n")
	sb.WriteString("<reasoning>\n")
	if zh {
		sb.WriteString("你的思维链分析...\n- 简明分析你的思考过程\n")
	} else {
		sb.WriteString("Your chain of thought analysis...\n- Briefly analyze your thinking process\n")
	}
	sb.WriteString("</reasoning>\n\n")
	sb.WriteString("<decision>\n")
	if zh {
		sb.WriteString("步骤 2: JSON 决策数组\n\n")
	} else {
		sb.WriteString("Step 2: JSON decision array\n\n")
	}
	sb.WriteString("```json\n[\n")

	// Build a JSON example using the actual trading symbol when the strategy
	// is single-symbol. Falls back to the legacy BTC/ETH two-line example
	// only for multi-symbol strategies that genuinely have BTC/ETH on tap.
	if singleSymbol {
		lev := riskControl.AltcoinMaxLeverage
		if riskControl.BTCETHMaxLeverage > lev {
			lev = riskControl.BTCETHMaxLeverage
		}
		ratio := singleSymbolPositionRatio(btcEthPosValueRatio, altcoinPosValueRatio) // same combined cap as the hard constraints
		size := accountEquity * ratio
		sb.WriteString(fmt.Sprintf("  {\"symbol\": \"%s\", \"action\": \"open_long\", \"leverage\": %d, \"position_size_usd\": %.0f, \"stop_loss\": 0, \"take_profit\": 0, \"confidence\": 85, \"risk_usd\": 0},\n", primarySymbol, lev, size))
		sb.WriteString(fmt.Sprintf("  {\"symbol\": \"%s\", \"action\": \"wait\"}\n", primarySymbol))
	} else {
		examplePositionSize := accountEquity * btcEthPosValueRatio
		sb.WriteString(fmt.Sprintf("  {\"symbol\": \"BTCUSDT\", \"action\": \"open_short\", \"leverage\": %d, \"position_size_usd\": %.0f, \"stop_loss\": 97000, \"take_profit\": 91000, \"confidence\": 85, \"risk_usd\": 300},\n",
			riskControl.BTCETHMaxLeverage, examplePositionSize))
		sb.WriteString("  {\"symbol\": \"ETHUSDT\", \"action\": \"close_long\"}\n")
	}
	sb.WriteString("]\n```\n")
	sb.WriteString("</decision>\n\n")

	if zh {
		sb.WriteString("## 字段说明\n\n")
		sb.WriteString("- `action`: open_long | open_short | close_long | close_short | hold | wait\n")
		sb.WriteString(fmt.Sprintf("- `confidence`: 0-100 (开仓建议 ≥ %d)\n", riskControl.MinConfidence))
		sb.WriteString("- 开仓时必填: leverage, position_size_usd, stop_loss, take_profit, confidence, risk_usd\n")
		sb.WriteString("- **重要**: 所有数值必须是算好的数字, 不能是公式/表达式 (例如写 `27.76`, 不要写 `3000 * 0.01`)\n")
		if singleSymbol {
			sb.WriteString(fmt.Sprintf("- **本策略只交易 %s**, JSON 中的 `symbol` 必须**完全等于** `%s`, 不要写成 `%s` 去掉后缀或加 USDT 的变体。\n", primarySymbol, primarySymbol, primarySymbol))
		}
		sb.WriteString("\n")
	} else {
		sb.WriteString("## Field Description\n\n")
		sb.WriteString("- `action`: open_long | open_short | close_long | close_short | hold | wait\n")
		sb.WriteString(fmt.Sprintf("- `confidence`: 0-100 (opening recommended ≥ %d)\n", riskControl.MinConfidence))
		sb.WriteString("- Required when opening: leverage, position_size_usd, stop_loss, take_profit, confidence, risk_usd\n")
		sb.WriteString("- **IMPORTANT**: all numeric values must be calculated numbers, NOT formulas/expressions (e.g. use `27.76`, not `3000 * 0.01`)\n")
		if singleSymbol {
			sb.WriteString(fmt.Sprintf("- **This strategy trades only %s.** The JSON `symbol` MUST match `%s` exactly — do not add USDT/USDC suffix variants.\n", primarySymbol, primarySymbol))
		}
		sb.WriteString("\n")
	}
}

func (e *StrategyEngine) writeAvailableIndicators(sb *strings.Builder, zh bool) {
	indicators := e.config.Indicators
	kline := indicators.Klines

	label := func(en, zhStr string) string {
		if zh {
			return zhStr
		}
		return en
	}

	if zh {
		sb.WriteString(fmt.Sprintf("- %s 价格序列", kline.PrimaryTimeframe))
		if kline.EnableMultiTimeframe {
			sb.WriteString(fmt.Sprintf(" + %s K 线序列\n", kline.LongerTimeframe))
		} else {
			sb.WriteString("\n")
		}
	} else {
		sb.WriteString(fmt.Sprintf("- %s price series", kline.PrimaryTimeframe))
		if kline.EnableMultiTimeframe {
			sb.WriteString(fmt.Sprintf(" + %s K-line series\n", kline.LongerTimeframe))
		} else {
			sb.WriteString("\n")
		}
	}

	if indicators.EnableEMA {
		sb.WriteString("- " + label("EMA indicators", "EMA 指标"))
		if len(indicators.EMAPeriods) > 0 {
			sb.WriteString(fmt.Sprintf(" (%s: %v)", label("periods", "周期"), indicators.EMAPeriods))
		}
		sb.WriteString("\n")
	}
	if indicators.EnableMACD {
		sb.WriteString("- " + label("MACD indicators", "MACD 指标"))
		fast, slow := indicators.MACDFastPeriod, indicators.MACDSlowPeriod
		if fast > 0 || slow > 0 {
			sb.WriteString(fmt.Sprintf(" (%s: %d/%d)", label("fast/slow", "快/慢"), fast, slow))
		}
		sb.WriteString("\n")
	}
	if indicators.EnableRSI {
		sb.WriteString("- " + label("RSI indicators", "RSI 指标"))
		if len(indicators.RSIPeriods) > 0 {
			sb.WriteString(fmt.Sprintf(" (%s: %v)", label("periods", "周期"), indicators.RSIPeriods))
		}
		sb.WriteString("\n")
	}
	if indicators.EnableATR {
		sb.WriteString("- " + label("ATR indicators", "ATR 指标"))
		if len(indicators.ATRPeriods) > 0 {
			sb.WriteString(fmt.Sprintf(" (%s: %v)", label("periods", "周期"), indicators.ATRPeriods))
		}
		sb.WriteString("\n")
	}
	if indicators.EnableBOLL {
		sb.WriteString("- " + label("Bollinger Bands (BOLL) - Upper/Middle/Lower bands", "布林带 (BOLL) - 上/中/下轨"))
		if len(indicators.BOLLPeriods) > 0 {
			sb.WriteString(fmt.Sprintf(" (%s: %v)", label("periods", "周期"), indicators.BOLLPeriods))
		}
		if indicators.BOLLMultiplier > 0 {
			sb.WriteString(fmt.Sprintf(" (%s: %.1f)", label("std-dev multiplier", "标准差倍数"), indicators.BOLLMultiplier))
		}
		sb.WriteString("\n")
	}
	if indicators.EnableADX {
		sb.WriteString("- " + label("ADX (trend strength) with DI+ / DI- (direction)", "ADX (趋势强度) 含 DI+ / DI- (方向)"))
		if len(indicators.ADXPeriods) > 0 {
			sb.WriteString(fmt.Sprintf(" (%s: %v)", label("periods", "周期"), indicators.ADXPeriods))
		}
		sb.WriteString("\n")
	}
	if indicators.EnableSupertrend {
		sb.WriteString("- " + label("Supertrend (trailing band + direction)", "Supertrend 趋势带 (含方向)"))
		if indicators.SupertrendPeriod > 0 {
			sb.WriteString(fmt.Sprintf(" (%s: %d, %s: %.1f)",
				label("period", "周期"), indicators.SupertrendPeriod,
				label("multiplier", "倍数"), indicators.SupertrendMultiplier))
		}
		sb.WriteString("\n")
	}
	if indicators.EnableVolume {
		sb.WriteString("- " + label("Volume data", "成交量数据") + "\n")
	}
	if indicators.EnableOI {
		sb.WriteString("- " + label("Open Interest (OI) data", "持仓量 (OI) 数据") + "\n")
	}
	if indicators.EnableFundingRate {
		sb.WriteString("- " + label("Funding rate", "资金费率") + "\n")
	}
	if len(e.config.CoinSource.StaticCoins) > 0 || e.config.CoinSource.UseAI500 || e.config.CoinSource.UseOITop {
		sb.WriteString("- " + label("AI500 / OI_Top filter tags (if available)", "AI500 / OI_Top 过滤标记 (如有)") + "\n")
	}
	if indicators.EnableQuantData {
		sb.WriteString("- " + label("Quantitative data (institutional/retail fund flow, position changes, multi-period price changes)", "量化数据 (机构/散户资金流, 持仓变化, 多周期价格变动)") + "\n")
	}
}

// writeIndicatorPlaybook emits role-based combination rules for the ENABLED
// indicators only. Rules must never reference data the model does not
// receive: a prompt that tells the model to "confirm with ADX" while ADX is
// disabled produces hallucinated confirmation. Roles are fixed (trend gate →
// momentum → timing → risk → confirmation → execution quality) so the model
// weighs a signal instead of acting on one indicator alone.
func (e *StrategyEngine) writeIndicatorPlaybook(sb *strings.Builder, zh bool) {
	ind := e.config.Indicators
	microEnabled := ind.EnableMicrostructure == nil || *ind.EnableMicrostructure
	l := func(en, zhStr string) string {
		if zh {
			return zhStr
		}
		return en
	}

	type group struct {
		title string
		lines []string
	}
	var groups []group
	addGroup := func(title string, lines []string) {
		if len(lines) > 0 {
			groups = append(groups, group{title: title, lines: lines})
		}
	}

	var gate []string
	if ind.EnableADX {
		gate = append(gate, l(
			"ADX measures trend STRENGTH — it is context, not a permission gate: above ~25 the market is trending (trade with the DI+/DI- side), 20-25 is a tradeable transition, below 20 it is ranging and breakout continuation is the weak play — there prefer the range edges or a fresh reversal. DI+ above DI- = bullish side, below = bearish. A low ADX on its own is not a reason to stay flat.",
			"ADX 衡量趋势「强度」，是判断背景的输入，不是开仓许可：约 25 以上为趋势市（顺 DI+/DI- 方向做），20-25 为可交易的过渡区，20 以下为震荡市——此时突破延续是最弱的打法，优先在区间边缘或出现明确反转时进场。DI+ 高于 DI- 为多头方向，反之空头。ADX 偏低本身不构成观望理由。"))
	}
	if ind.EnableSupertrend {
		gate = append(gate, l(
			"Supertrend direction (+1 up / -1 down) sets the allowed side; a fresh flip is an entry/exit reference and the band trails as a stop.",
			"Supertrend 方向（+1 上行 / -1 下行）决定允许的持仓方向；刚翻转可作为入场/离场参考，带值可当移动止损。"))
	}
	if ind.EnableEMA {
		gate = append(gate, l(
			"Price above the EMA set = bullish structure, below = bearish; a cross against your position argues for reducing.",
			"价格位于 EMA 组之上为多头结构，之下为空头结构；EMA 反向交叉应减仓。"))
	}
	addGroup(l("Trend gate — decides side / stand-aside", "趋势判断 — 决定方向 / 观望"), gate)

	var momentum []string
	if ind.EnableMACD {
		momentum = append(momentum, l(
			"MACD crossing its signal/zero line in your direction with an expanding histogram confirms momentum; a shrinking histogram or price/MACD divergence against you argues for restraint.",
			"MACD 同向穿越信号线/零轴且柱体放大，确认动能；柱体收缩或价格与 MACD 背离时应克制。"))
	}
	if ind.EnableRSI {
		momentum = append(momentum, l(
			"RSI extremes persist in strong trends — never fade a trend on RSI alone; RSI divergence against your position is a warning, not a reversal signal.",
			"强趋势中 RSI 可长期处于极值：不要仅凭 RSI 逆势；RSI 与价格背离是警告，不是反转信号。"))
	}
	addGroup(l("Momentum confirmation", "动能确认"), momentum)

	var timing []string
	if ind.EnableBOLL {
		timing = append(timing, l(
			"Bollinger Bands time entries: in an uptrend a pullback toward the middle/lower band is an entry zone (mirror for shorts); a squeeze (narrowing bands) precedes expansion — only trade the breakout in the trend gate's direction.",
			"布林带用于择时：上升趋势中回调至中轨/下轨是入场区（做空镜像）；带宽收窄（挤压）预示变盘——只顺势交易突破方向。"))
	}
	addGroup(l("Entry timing", "入场时机"), timing)

	var risk []string
	if ind.EnableATR {
		risk = append(risk, l(
			"Size stops from ATR (roughly 1.5-2x ATR beyond entry) and cut size when ATR expands sharply.",
			"以 ATR 设置止损距离（约入场价外 1.5-2 倍 ATR）；ATR 急剧放大时降低仓位。"))
	}
	addGroup(l("Risk / sizing", "风险与仓位"), risk)

	var confirmation []string
	if ind.EnableVolume {
		confirmation = append(confirmation, l(
			"Breakouts need volume expansion; low-volume moves fade.",
			"突破需要成交量放大确认；缩量突破易失败。"))
	}
	if ind.EnableOI {
		confirmation = append(confirmation, l(
			"Rising OI with rising price = new money supporting the trend; OI spiking while price stalls = crowded, reduce.",
			"持仓量随价格同涨为增量资金入场；价格滞涨而持仓量骤增说明拥挤，应减仓。"))
	}
	if ind.EnableFundingRate {
		confirmation = append(confirmation, l(
			"Extreme funding marks crowded positioning — avoid chasing in that direction. Where the percentile against the coin's own 7d funding is available (top/bottom decile), that is the crowding read to use.",
			"资金费率极端代表持仓拥挤——避免在该方向追单。若给出了相对该币自身 7 日资金费的分位（高位/低位十分位），以该分位为拥挤度依据。"))
	}
	addGroup(l("Confirmation", "信号确认"), confirmation)

	var execution []string
	if microEnabled {
		execution = append(execution, l(
			"Microstructure: a wide spread or low liquidity score argues against market chasing; book imbalance is short-term pressure only, never the trend reason.",
			"微观结构：价差过宽或流动性评分过低时不要市价追单；盘口失衡只代表短期压力，不构成趋势理由。"))
	}
	addGroup(l("Execution quality", "执行质量"), execution)

	// Positioning: what the venue's own open interest and trade flow say about
	// who is entering or leaving. Hyperliquid exposes no account long/short
	// ratio, so these read as the reason behind a move, not as entry triggers.
	var positioning []string
	if ind.PositioningEnabled() {
		positioning = append(positioning,
			l("Open interest × price separates new money from closed positions: OI up + price up = new longs, OI up + price down = new shorts, OI down + price up = short covering, OI down + price down = long capitulation. Read it as the reason behind the candle, not as an entry trigger.",
				"持仓量 × 价格可区分『新资金入场』与『头寸平仓』：持仓↑价↑=新多、持仓↑价↓=新空、持仓↓价↑=空头回补、持仓↓价↓=多头割肉。它用来解释 K 线背后的原因，而不是当作入场触发。"),
			l("Taker flow ratio (buy/sell notional) confirms momentum when it aligns with the move; a large ratio against a flat price means someone is absorbing supply, so prefer confirmation before entering.",
				"主动成交比（买卖名义额）与价格同向放大时可作动量确认；若比值很大而价格不动，说明有对手在承接/派发，入场前应等待确认。"))
	}
	addGroup(l("Positioning / crowding", "持仓与拥挤度"), positioning)

	if len(groups) == 0 {
		return
	}

	sb.WriteString(l("# 🔗 Indicator Confluence Rules (enabled indicators only)", "# 🔗 指标协同规则（仅针对已启用指标）") + "\n\n")
	for gi, g := range groups {
		if gi > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("**" + g.title + "**\n")
		for _, line := range g.lines {
			sb.WriteString("- " + line + "\n")
		}
	}
	sb.WriteString(l(
		"\nPrinciple: a single indicator is never a reason to enter. Alignment means the enabled signals do not contradict each other and the trend gate agrees — it does not require every indicator to sit at an extreme, and a quiet, low-volatility tape is not by itself a reason to stay flat: size the position to the opportunity instead. When the enabled indicators genuinely conflict, stand aside or take the smallest position.\n\n",
		"\n原则：单一指标不构成入场理由。所谓共振，指已启用信号之间不互相矛盾、且与趋势判断同向——并不要求每个指标都处于极值；行情安静、波动偏低本身也不构成观望理由，应按机会大小调整仓位。只有当已启用指标确实相互矛盾时，才观望或取最小仓位。\n\n"))
}

// writeExitDiscipline states when a position may be closed. Without it the
// model improvises exits: a live run showed 81% of exits were discretionary and
// taken before either level was touched, on moves far smaller than the planned
// risk — churn that paid round-trip fees for noise.
func writeExitDiscipline(sb *strings.Builder, zh bool, minHoldMinutes int) {
	if zh {
		sb.WriteString("# 🚪 出场纪律（仅在下列条件之一成立时平仓）\n\n")
		sb.WriteString("1. 触发止损或止盈（交易所端自动执行）\n")
		sb.WriteString("2. 趋势结构反转: Supertrend 翻转, 或主时间帧收盘跌破/站上 EMA50, 或 ADX 跌破 20（趋势消失）\n")
		sb.WriteString("3. 利润保护: 浮盈 ≥2% 后从峰值回吐 30%（由代码监控执行）\n\n")
		sb.WriteString("其余情况保持持仓：浮亏但未触及止损、指标间分歧、行情震荡都不构成平仓理由。\n")
		sb.WriteString("不要在多个决策周期内反复开平同一标的——每次往返都支付双边手续费。\n")
		if minHoldMinutes > 0 {
			sb.WriteString(fmt.Sprintf("**代码强制**: 持仓不足 %d 分钟的主动平仓会被忽略（止损/止盈不受影响）。\n", minHoldMinutes))
		}
		sb.WriteString("\n")
		return
	}

	sb.WriteString("# 🚪 Exit Discipline (close only when one of these holds)\n\n")
	sb.WriteString("1. Stop-loss or take-profit is hit (executed exchange-side)\n")
	sb.WriteString("2. The trend structure reverses: Supertrend flips, or the primary timeframe closes back through EMA50, or ADX drops below 20 (trend gone)\n")
	sb.WriteString("3. Profit protection: after a peak of ≥2%, giving back 30% of it (enforced by the monitor)\n\n")
	sb.WriteString("Otherwise hold: an unrealized loss that has not reached the stop, disagreement between indicators, or choppy price action are not reasons to close.\n")
	sb.WriteString("Do not open and close the same symbol across consecutive cycles — every round trip pays fees on both sides.\n")
	if minHoldMinutes > 0 {
		sb.WriteString(fmt.Sprintf("**CODE ENFORCED**: discretionary closes before %d minutes of holding are ignored (stop-loss / take-profit are unaffected).\n", minHoldMinutes))
	}
	sb.WriteString("\n")
}

// ============================================================================
// Prompt Building - User Prompt
// ============================================================================

// BuildUserPrompt builds User Prompt based on strategy configuration
func (e *StrategyEngine) BuildUserPrompt(ctx *Context) string {
	var sb strings.Builder

	// System status
	sb.WriteString(fmt.Sprintf("Time: %s | Period: #%d | Runtime: %d minutes\n\n",
		ctx.CurrentTime, ctx.CallCount, ctx.RuntimeMinutes))

	// BTC market
	if btcData, hasBTC := ctx.MarketDataMap["BTCUSDT"]; hasBTC {
		// Deliberately price/change only: the fixed-period MACD/RSI readouts
		// that used to sit here contradicted the configured-period series the
		// model receives in the symbol's own data block.
		sb.WriteString(fmt.Sprintf("BTC: %.2f (1h: %+.2f%%, 4h: %+.2f%%)\n\n",
			btcData.CurrentPrice, btcData.PriceChange1h, btcData.PriceChange4h))
	}

	// Account information
	sb.WriteString(fmt.Sprintf("Account: Equity %.2f | Balance %.2f (%.1f%%) | PnL %+.2f%% | Margin %.1f%% | Positions %d\n\n",
		ctx.Account.TotalEquity,
		ctx.Account.AvailableBalance,
		(ctx.Account.AvailableBalance/ctx.Account.TotalEquity)*100,
		ctx.Account.TotalPnLPct,
		ctx.Account.MarginUsedPct,
		ctx.Account.PositionCount))

	// Recently completed orders (placed before positions to ensure visibility)
	if len(ctx.RecentOrders) > 0 {
		sb.WriteString("## Recent Completed Trades\n")
		for i, order := range ctx.RecentOrders {
			resultStr := "Profit"
			if order.RealizedPnL < 0 {
				resultStr = "Loss"
			}
			sb.WriteString(fmt.Sprintf("%d. %s %s | Entry %.4f Exit %.4f | %s: %+.2f USDT (%+.2f%%) | %s→%s (%s)\n",
				i+1, order.Symbol, order.Side,
				order.EntryPrice, order.ExitPrice,
				resultStr, order.RealizedPnL, order.PnLPct,
				order.EntryTime, order.ExitTime, order.HoldDuration))
		}
		sb.WriteString("\n")
	}

	// Historical trading statistics (helps AI understand past performance)
	if ctx.TradingStats != nil && ctx.TradingStats.TotalTrades > 0 {
		// Get language from strategy config
		lang := e.GetLanguage()

		// Win/Loss ratio
		var winLossRatio float64
		if ctx.TradingStats.AvgLoss > 0 {
			winLossRatio = ctx.TradingStats.AvgWin / ctx.TradingStats.AvgLoss
		}

		if lang == LangChinese {
			sb.WriteString("## 历史交易统计\n")
			sb.WriteString(fmt.Sprintf("总交易: %d 笔 | 盈利因子: %.2f | 夏普比率: %.2f | 盈亏比: %.2f\n",
				ctx.TradingStats.TotalTrades,
				ctx.TradingStats.ProfitFactor,
				ctx.TradingStats.SharpeRatio,
				winLossRatio))
			sb.WriteString(fmt.Sprintf("总盈亏: %+.2f USDT | 平均盈利: +%.2f | 平均亏损: -%.2f | 最大回撤: %.1f%%\n",
				ctx.TradingStats.TotalPnL,
				ctx.TradingStats.AvgWin,
				ctx.TradingStats.AvgLoss,
				ctx.TradingStats.MaxDrawdownPct))

			// Performance hints based on profit factor, sharpe, and drawdown
			if ctx.TradingStats.ProfitFactor >= 1.5 && ctx.TradingStats.SharpeRatio >= 1 {
				sb.WriteString("表现: 良好 - 保持当前策略\n")
			} else if ctx.TradingStats.ProfitFactor < 1 {
				sb.WriteString("表现: 需改进 - 提高盈亏比，优化止盈止损\n")
			} else if ctx.TradingStats.MaxDrawdownPct > 30 {
				sb.WriteString("表现: 风险偏高 - 减少仓位，控制回撤\n")
			} else {
				sb.WriteString("表现: 正常 - 有优化空间\n")
			}
		} else {
			sb.WriteString("## Historical Trading Statistics\n")
			sb.WriteString(fmt.Sprintf("Total Trades: %d | Profit Factor: %.2f | Sharpe: %.2f | Win/Loss Ratio: %.2f\n",
				ctx.TradingStats.TotalTrades,
				ctx.TradingStats.ProfitFactor,
				ctx.TradingStats.SharpeRatio,
				winLossRatio))
			sb.WriteString(fmt.Sprintf("Total PnL: %+.2f USDT | Avg Win: +%.2f | Avg Loss: -%.2f | Max Drawdown: %.1f%%\n",
				ctx.TradingStats.TotalPnL,
				ctx.TradingStats.AvgWin,
				ctx.TradingStats.AvgLoss,
				ctx.TradingStats.MaxDrawdownPct))

			// Performance hints based on profit factor, sharpe, and drawdown
			if ctx.TradingStats.ProfitFactor >= 1.5 && ctx.TradingStats.SharpeRatio >= 1 {
				sb.WriteString("Performance: GOOD - maintain current strategy\n")
			} else if ctx.TradingStats.ProfitFactor < 1 {
				sb.WriteString("Performance: NEEDS IMPROVEMENT - improve win/loss ratio, optimize TP/SL\n")
			} else if ctx.TradingStats.MaxDrawdownPct > 30 {
				sb.WriteString("Performance: HIGH RISK - reduce position size, control drawdown\n")
			} else {
				sb.WriteString("Performance: NORMAL - room for optimization\n")
			}
		}
		sb.WriteString("\n")
	}

	// Position information
	if len(ctx.Positions) > 0 {
		sb.WriteString("## Current Positions\n")
		for i, pos := range ctx.Positions {
			sb.WriteString(e.formatPositionInfo(i+1, pos, ctx))
		}
	} else {
		sb.WriteString("Current Positions: None\n\n")
	}

	// Candidate coins (exclude coins already in positions to avoid duplicate data)
	positionSymbols := make(map[string]bool)
	for _, pos := range ctx.Positions {
		// Normalize symbol to handle both "ETH" and "ETHUSDT" formats
		normalizedSymbol := market.Normalize(pos.Symbol)
		positionSymbols[normalizedSymbol] = true
	}

	// Build the body first so the header can carry the ACTUAL displayed count
	// (the old header used len(MarketDataMap), which counts position symbols
	// and coins skipped here).
	var candidateBody strings.Builder
	displayedCount := 0
	for _, coin := range ctx.CandidateCoins {
		// Skip if this coin is already a position (data already shown in positions section)
		normalizedCoinSymbol := market.Normalize(coin.Symbol)
		if positionSymbols[normalizedCoinSymbol] {
			continue
		}

		marketData, hasData := ctx.MarketDataMap[coin.Symbol]
		if !hasData {
			continue
		}
		displayedCount++

		sourceTags := e.formatCoinSourceTag(coin.Sources)
		candidateBody.WriteString(fmt.Sprintf("### %d. %s%s\n\n", displayedCount, coin.Symbol, sourceTags))
		candidateBody.WriteString(e.formatMarketData(marketData, false))

		if ctx.QuantDataMap != nil {
			if quantData, hasQuant := ctx.QuantDataMap[coin.Symbol]; hasQuant {
				candidateBody.WriteString(e.formatQuantData(quantData))
			}
		}
		candidateBody.WriteString(e.formatMicrostructure(ctx, coin.Symbol))
		candidateBody.WriteString(e.formatPositioning(ctx, coin.Symbol))
		candidateBody.WriteString("\n")
	}
	sb.WriteString(fmt.Sprintf("## Candidate Coins (%d coins)\n\n", displayedCount))
	sb.WriteString(candidateBody.String())
	sb.WriteString("\n")

	// Get language for market data formatting
	nofxosLang := nofxos.LangEnglish
	if e.GetLanguage() == LangChinese {
		nofxosLang = nofxos.LangChinese
	}

	// OI Ranking data (market-wide open interest changes)
	if ctx.OIRankingData != nil {
		sb.WriteString(nofxos.FormatOIRankingForAI(ctx.OIRankingData, nofxosLang))
	}

	// NetFlow Ranking data (market-wide fund flow)
	if ctx.NetFlowRankingData != nil {
		sb.WriteString(nofxos.FormatNetFlowRankingForAI(ctx.NetFlowRankingData, nofxosLang))
	}

	// Price Ranking data (market-wide gainers/losers)
	if ctx.PriceRankingData != nil {
		sb.WriteString(nofxos.FormatPriceRankingForAI(ctx.PriceRankingData, nofxosLang))
	}

	sb.WriteString("---\n\n")
	sb.WriteString("Now please analyze and output your decision (Chain of Thought + JSON)\n")

	return sb.String()
}

// formatMicrostructure writes the decision-time order book metrics for one
// symbol. Returns an empty string when the symbol has no analysis (fetch
// failure, disabled, or xyz asset) so the prompt section is simply omitted.
func (e *StrategyEngine) formatMicrostructure(ctx *Context, symbol string) string {
	if ctx == nil || ctx.MicrostructureMap == nil {
		return ""
	}
	m, ok := ctx.MicrostructureMap[symbol]
	if !ok || m == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("**MICROSTRUCTURE (decision-time order book)**\n")
	sb.WriteString(fmt.Sprintf("- Spread %.1f bps | Imbalance %+.2f (%s) | Top-10 depth: bid %.0f / ask %.0f USDT | Liquidity %d/100 (thin books and wide spreads argue against market chasing)\n",
		m.SpreadBps, m.Imbalance, m.ImbalanceSide, m.BidDepthUSD, m.AskDepthUSD, m.LiquidityScore))
	if len(m.SupportLevels) > 0 || len(m.ResistanceLevels) > 0 {
		sb.WriteString("- Largest resting bids (support): " + formatFloatSlice(m.SupportLevels) +
			" | largest resting asks (resistance): " + formatFloatSlice(m.ResistanceLevels) + "\n")
	}
	if m.VWAP > 0 {
		sb.WriteString(fmt.Sprintf("- VWAP %.4f (price %+.2f%% vs VWAP)\n", m.VWAP, m.VWAPDeviationPct))
	}
	sb.WriteString("\n")
	return sb.String()
}

// formatPositioning writes the Hyperliquid-native positioning reading for one
// symbol: hourly funding with its own-history percentile, open interest against
// price, and the aggressor-side flow ratio. Hyperliquid exposes no account
// long/short ratio, so the header says outright that this is a venue-native
// proxy. Clauses without enough samples are omitted — never printed as zero,
// which the model would read as "no positioning" rather than "unknown".
func (e *StrategyEngine) formatPositioning(ctx *Context, symbol string) string {
	if ctx == nil || ctx.PositioningMap == nil {
		return ""
	}
	p, ok := ctx.PositioningMap[symbol]
	if !ok || p == nil {
		return ""
	}

	zh := e.GetLanguage() == LangChinese
	pick := func(en, cn string) string {
		if zh {
			return cn
		}
		return en
	}

	var sb strings.Builder
	wrote := false
	ensureHeader := func() {
		if wrote {
			return
		}
		sb.WriteString(pick("**HL POSITIONING (venue-native proxy, not an account long/short ratio)**\n",
			"**HL 持仓信号（交易所原生代理指标，不是账户多空比）**\n"))
		wrote = true
	}

	// Snapshot line: funding (+ percentile and crowding), premium, open interest.
	var clauses []string
	if p.Funding.RateHourlyPct != 0 || p.Funding.PercentileOK {
		funding := fmt.Sprintf(pick("funding %+.4f%%/h ≈ %+.0f%% APR", "资金费 %+.4f%%/小时 ≈ 年化 %+.0f%%"),
			p.Funding.RateHourlyPct, p.Funding.AnnualPct)
		if p.Funding.RateHourlyPct > 0 {
			funding += pick(" (longs pay shorts)", "（多头付空头）")
		} else if p.Funding.RateHourlyPct < 0 {
			funding += pick(" (shorts pay longs)", "（空头付多头）")
		}
		if p.Funding.PercentileOK {
			if zh {
				funding += fmt.Sprintf(" | 处于该币自身 7 日资金费的第 %.0f 百分位", p.Funding.Percentile)
			} else {
				funding += fmt.Sprintf(" | %s pct of the coin's own 7d funding", formatOrdinal(p.Funding.Percentile))
			}
		}
		if crowding := fundingCrowding(p.Funding.Percentile, p.Funding.PercentileOK, zh); crowding != "" {
			funding += " | " + crowding
		}
		clauses = append(clauses, funding)
	}
	if p.PremiumPct != 0 {
		clauses = append(clauses, fmt.Sprintf(pick("premium %+.3f%%", "溢价 %+.3f%%"), p.PremiumPct))
	}
	if p.OIUSD > 0 {
		clauses = append(clauses, fmt.Sprintf(pick("OI $%s", "持仓 $%s"), formatUSDCompact(p.OIUSD)))
	}
	if len(clauses) > 0 {
		ensureHeader()
		sb.WriteString("- " + strings.Join(clauses, " | ") + "\n")
	}

	// Open interest against price: separates new money from closed positions.
	var oiParts []string
	for _, r := range p.OI {
		if r.Quadrant == "" && r.OIChangePct == 0 {
			continue
		}
		part := fmt.Sprintf(pick("OI %s %+.2f%% / price %+.2f%%", "持仓 %s %+.2f%% / 价格 %+.2f%%"),
			windowLabel(r.Window), r.OIChangePct, r.PriceChangePct)
		if label := oiQuadrantLabel(r.Quadrant, zh); label != "" {
			part += " → " + label
		}
		oiParts = append(oiParts, part)
	}
	if len(oiParts) > 0 {
		ensureHeader()
		sb.WriteString("- " + strings.Join(oiParts, " | ") + "\n")
	}

	// Aggressor-side flow: who is crossing the spread.
	if f := p.TakerFlow; f != nil {
		ensureHeader()
		ratio := fmt.Sprintf("%.2fx", f.Ratio)
		if math.IsInf(f.Ratio, 1) {
			ratio = pick("all buys", "全为主动买")
		} else if f.Ratio == 0 && f.BuyUSD == 0 {
			ratio = pick("all sells", "全为主动卖")
		}
		sb.WriteString(fmt.Sprintf(pick("- Taker flow %s: buy $%s / sell $%s = %s (%d prints)\n",
			"- 主动成交 %s：买 $%s / 卖 $%s = %s（%d 笔）"),
			windowLabel(f.Window), formatUSDCompact(f.BuyUSD), formatUSDCompact(f.SellUSD), ratio, f.Samples))
	}

	if !wrote {
		return ""
	}

	// Flag a cold venue for stock symbols: the code will refuse new entries there
	// (the cash market is most likely closed), so say so instead of letting the
	// model propose an entry that cannot be executed.
	if note := e.stockVenueNote(symbol); note != "" {
		sb.WriteString(note)
	}

	sb.WriteString("\n")
	return sb.String()
}

// stockVenueNote describes a stock whose venue has gone quiet. Returns an empty
// string for non-stocks, for a healthy feed, or when the session gate is off.
func (e *StrategyEngine) stockVenueNote(symbol string) string {
	if e.config == nil || !hyperliquid.IsStockAsset(symbol) {
		return ""
	}
	enabled, quietMinutes := e.config.RiskControl.StockEntryGate()
	feedActive, age := market.VenueActivity(symbol)
	return stockVenueNoteText(true, enabled, feedActive, age, quietMinutes, e.GetLanguage() == LangChinese)
}

// stockVenueNoteText is the pure form of stockVenueNote: it decides whether the
// prompt should warn that a stock's venue has gone cold. The notice matters
// because the execution path refuses new entries there, so a proposal would be
// wasted.
func stockVenueNoteText(isStock, gateEnabled, feedActive bool, age time.Duration, quietMinutes int, zh bool) string {
	if !isStock || !gateEnabled || !feedActive {
		return ""
	}
	if quietMinutes <= 0 {
		quietMinutes = store.DefaultStockQuietMinutes
	}
	if age > 0 && age <= time.Duration(quietMinutes)*time.Minute {
		return ""
	}
	if zh {
		return "- ⚠️ 交易所活跃度偏低（该标的近期无成交）——现货市场很可能已闭市，代码会拒绝新开仓；不要在此情形下提出开仓建议。\n"
	}
	return "- ⚠️ Venue activity is cold (no recent trades) — the cash market is most likely closed, the code will refuse new entries here; do not propose opening one.\n"
}

// formatUSDCompact renders a notional amount compactly (12.3K / 1.24M / 850).
func formatUSDCompact(v float64) string {
	abs := math.Abs(v)
	switch {
	case abs >= 1_000_000_000:
		return fmt.Sprintf("%.2fB", v/1_000_000_000)
	case abs >= 1_000_000:
		return fmt.Sprintf("%.2fM", v/1_000_000)
	case abs >= 1_000:
		return fmt.Sprintf("%.1fK", v/1_000)
	default:
		return fmt.Sprintf("%.0f", v)
	}
}

// formatOrdinal renders a 0-100 percentile as an English ordinal (92 -> "92nd").
func formatOrdinal(pct float64) string {
	n := int(math.Round(pct))
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return fmt.Sprintf("%d%s", n, suffix)
}

// windowLabel renders a lookback compactly (1h / 4h / 3d).
func windowLabel(d time.Duration) string {
	switch {
	case d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	default:
		return d.String()
	}
}

// fundingCrowding labels a funding percentile that sits at the extremes, which
// is where funding stops being noise and starts being information.
func fundingCrowding(percentile float64, ok, zh bool) string {
	if !ok {
		return ""
	}
	switch {
	case percentile >= 90:
		return pickLang(zh, "funding near its 7d high → crowded long, do not chase this side", "资金费接近 7 日高位 → 多头拥挤，不要在该方向追单")
	case percentile <= 10:
		return pickLang(zh, "funding near its 7d low → crowded short, do not chase this side", "资金费接近 7 日低位 → 空头拥挤，不要在该方向追单")
	default:
		return ""
	}
}

// oiQuadrantLabel explains an open-interest/price quadrant in words.
func oiQuadrantLabel(quadrant string, zh bool) string {
	switch quadrant {
	case market.QuadrantNewLongs:
		return pickLang(zh, "new longs entering", "新多入场")
	case market.QuadrantNewShorts:
		return pickLang(zh, "new shorts entering", "新空入场")
	case market.QuadrantShortCovering:
		return pickLang(zh, "short covering", "空头回补")
	case market.QuadrantLongCapitulation:
		return pickLang(zh, "long capitulation", "多头割肉")
	default:
		return ""
	}
}

func pickLang(zh bool, en, cn string) string {
	if zh {
		return cn
	}
	return en
}

func (e *StrategyEngine) formatPositionInfo(index int, pos PositionInfo, ctx *Context) string {
	var sb strings.Builder

	holdingDuration := ""
	if pos.UpdateTime > 0 {
		durationMs := time.Now().UnixMilli() - pos.UpdateTime
		durationMin := durationMs / (1000 * 60)
		if durationMin < 60 {
			holdingDuration = fmt.Sprintf(" | Holding Duration %d min", durationMin)
		} else {
			durationHour := durationMin / 60
			durationMinRemainder := durationMin % 60
			holdingDuration = fmt.Sprintf(" | Holding Duration %dh %dm", durationHour, durationMinRemainder)
		}
	}

	positionValue := pos.Quantity * pos.MarkPrice
	if positionValue < 0 {
		positionValue = -positionValue
	}

	sb.WriteString(fmt.Sprintf("%d. %s %s | Entry %.4f Current %.4f | Qty %.4f | Position Value %.2f USDT | PnL%+.2f%% | PnL Amount%+.2f USDT | Peak PnL%.2f%% | Leverage %dx | Margin %.0f | Liq Price %.4f%s\n\n",
		index, pos.Symbol, strings.ToUpper(pos.Side),
		pos.EntryPrice, pos.MarkPrice, pos.Quantity, positionValue, pos.UnrealizedPnLPct, pos.UnrealizedPnL, pos.PeakPnLPct,
		pos.Leverage, pos.MarginUsed, pos.LiquidationPrice, holdingDuration))

	if marketData, ok := ctx.MarketDataMap[pos.Symbol]; ok {
		sb.WriteString(e.formatMarketData(marketData, true))

		if ctx.QuantDataMap != nil {
			if quantData, hasQuant := ctx.QuantDataMap[pos.Symbol]; hasQuant {
				sb.WriteString(e.formatQuantData(quantData))
			}
		}
		sb.WriteString(e.formatMicrostructure(ctx, pos.Symbol))
		sb.WriteString(e.formatPositioning(ctx, pos.Symbol))
		sb.WriteString("\n")
	}

	return sb.String()
}

func (e *StrategyEngine) formatCoinSourceTag(sources []string) string {
	if len(sources) > 1 {
		// Multiple signal source combination
		hasAI500 := false
		hasOITop := false
		hasOILow := false
		hasHyperAll := false
		hasHyperMain := false
		for _, s := range sources {
			switch s {
			case "ai500":
				hasAI500 = true
			case "oi_top":
				hasOITop = true
			case "oi_low":
				hasOILow = true
			case "hyper_all":
				hasHyperAll = true
			case "hyper_main":
				hasHyperMain = true
			}
		}
		if hasAI500 && hasOITop {
			return " (AI500+OI_Top dual signal)"
		}
		if hasAI500 && hasOILow {
			return " (AI500+OI_Low dual signal)"
		}
		if hasOITop && hasOILow {
			return " (OI_Top+OI_Low)"
		}
		if hasHyperMain && hasAI500 {
			return " (HyperMain+AI500)"
		}
		if hasHyperAll || hasHyperMain {
			return " (Hyperliquid)"
		}
		return " (Multiple sources)"
	} else if len(sources) == 1 {
		switch sources[0] {
		case "ai500":
			return " (AI500)"
		case "oi_top":
			return " (OI_Top OI increase)"
		case "oi_low":
			return " (OI_Low OI decrease)"
		case "static":
			return " (Manual selection)"
		case "hyper_all":
			return " (Hyperliquid All)"
		case "hyper_main":
			return " (Hyperliquid Top20)"
		}
	}
	return ""
}

// ============================================================================
// Market Data Formatting
// ============================================================================

// formatMarketData renders one symbol's data block. fullKlines controls
// whether every timeframe carries its full candle table: positions get the
// full picture, while candidates trim secondary timeframes to their
// indicator series (the primary timeframe keeps its table) to keep the prompt
// from growing linearly with candidate count.
func (e *StrategyEngine) formatMarketData(data *market.Data, fullKlines bool) string {
	var sb strings.Builder
	indicators := e.config.Indicators
	limit := resolveDisplayLimit(indicators)

	// Clearly label the coin symbol
	sb.WriteString(fmt.Sprintf("=== %s Market Data ===\n\n", data.Symbol))
	// current_price only: the fixed-period current_ema20/current_macd/
	// current_rsi7 readouts that used to follow contradicted the
	// configured-period series printed below (e.g. EMA(30) when the strategy
	// set ema_periods=[30, 100]), so the model saw two different values for
	// the "same" indicator.
	sb.WriteString(fmt.Sprintf("current_price = %.4f\n\n", data.CurrentPrice))

	if indicators.EnableOI || indicators.EnableFundingRate {
		sb.WriteString(fmt.Sprintf("Additional data for %s:\n\n", data.Symbol))

		if indicators.EnableOI && data.OpenInterest != nil {
			sb.WriteString(fmt.Sprintf("Open Interest: Latest: %.2f Average: %.2f\n\n",
				data.OpenInterest.Latest, data.OpenInterest.Average))
		}

		if indicators.EnableFundingRate {
			// Per-day normalization: the venue charges funding hourly, so the
			// raw rate is 24x the daily carry cost the AI should reason about.
			sb.WriteString(fmt.Sprintf("Funding Rate: %.4f%%/day\n\n", data.FundingRatePerDay*100))
		}

		// Venue context that Binance cannot provide (HIP-3 stocks/commodities
		// included): premium against the oracle price and 24h traded notional.
		if data.PremiumPct != 0 || data.DayNotionalVolume > 0 {
			if data.DayNotionalVolume > 0 {
				sb.WriteString(fmt.Sprintf("24h Notional Volume: $%.0f", data.DayNotionalVolume))
			}
			if data.PremiumPct != 0 {
				if data.DayNotionalVolume > 0 {
					sb.WriteString(" | ")
				}
				sb.WriteString(fmt.Sprintf("Premium vs oracle: %+.4f%%", data.PremiumPct))
			}
			sb.WriteString("\n\n")
		}
	}

	if len(data.TimeframeData) > 0 {
		primaryTimeframe := strings.TrimSpace(indicators.Klines.PrimaryTimeframe)
		timeframeOrder := []string{"1m", "3m", "5m", "15m", "30m", "1h", "2h", "4h", "6h", "8h", "12h", "1d", "3d", "1w"}
		for _, tf := range timeframeOrder {
			if tfData, ok := data.TimeframeData[tf]; ok {
				includeKlines := fullKlines || primaryTimeframe == "" || tf == primaryTimeframe
				if includeKlines {
					sb.WriteString(fmt.Sprintf("=== %s Timeframe (oldest → latest) ===\n\n", strings.ToUpper(tf)))
				} else {
					sb.WriteString(fmt.Sprintf("=== %s Timeframe (indicator series, oldest → latest) ===\n\n", strings.ToUpper(tf)))
				}
				e.formatTimeframeSeriesData(&sb, tfData, indicators, includeKlines)
			}
		}
	} else {
		// Compatible with old data format
		if data.IntradaySeries != nil {
			klineConfig := indicators.Klines
			sb.WriteString(fmt.Sprintf("Intraday series (%s intervals, oldest → latest):\n\n", klineConfig.PrimaryTimeframe))

			if len(data.IntradaySeries.MidPrices) > 0 {
				sb.WriteString(fmt.Sprintf("Mid prices: %s\n\n", formatSeriesTail(data.IntradaySeries.MidPrices, limit)))
			}

			if indicators.EnableEMA {
				if !writeConfiguredIndicators(&sb, data.IntradaySeries.ConfiguredIndicators, "EMA", "", "\n\n", limit) && len(data.IntradaySeries.EMA20Values) > 0 {
					sb.WriteString(fmt.Sprintf("EMA indicators (20-period): %s\n\n", formatSeriesTail(data.IntradaySeries.EMA20Values, limit)))
				}
			}

			if indicators.EnableMACD {
				if !writeConfiguredIndicators(&sb, data.IntradaySeries.ConfiguredIndicators, "MACD", "", "\n\n", limit) && len(data.IntradaySeries.MACDValues) > 0 {
					sb.WriteString(fmt.Sprintf("MACD indicators: %s\n\n", formatSeriesTail(data.IntradaySeries.MACDValues, limit)))
				}
			}

			if indicators.EnableRSI {
				if !writeConfiguredIndicators(&sb, data.IntradaySeries.ConfiguredIndicators, "RSI", "", "\n\n", limit) {
					if len(data.IntradaySeries.RSI7Values) > 0 {
						sb.WriteString(fmt.Sprintf("RSI indicators (7-Period): %s\n\n", formatSeriesTail(data.IntradaySeries.RSI7Values, limit)))
					}
					if len(data.IntradaySeries.RSI14Values) > 0 {
						sb.WriteString(fmt.Sprintf("RSI indicators (14-Period): %s\n\n", formatSeriesTail(data.IntradaySeries.RSI14Values, limit)))
					}
				}
			}

			if indicators.EnableVolume && len(data.IntradaySeries.Volume) > 0 {
				sb.WriteString(fmt.Sprintf("Volume: %s\n\n", formatSeriesTail(data.IntradaySeries.Volume, limit)))
			}

			if indicators.EnableATR {
				if !writeConfiguredIndicators(&sb, data.IntradaySeries.ConfiguredIndicators, "ATR", "%.3f", "\n\n", limit) {
					sb.WriteString(fmt.Sprintf("3m ATR (14-period): %.3f\n\n", data.IntradaySeries.ATR14))
				}
			}
		}

		if data.LongerTermContext != nil && indicators.Klines.EnableMultiTimeframe {
			sb.WriteString(fmt.Sprintf("Longer-term context (%s timeframe):\n\n", indicators.Klines.LongerTimeframe))

			if indicators.EnableEMA {
				if !writeConfiguredIndicators(&sb, data.LongerTermContext.ConfiguredIndicators, "EMA", "%.3f", "\n\n", limit) {
					sb.WriteString(fmt.Sprintf("20-Period EMA: %.3f vs. 50-Period EMA: %.3f\n\n",
						data.LongerTermContext.EMA20, data.LongerTermContext.EMA50))
				}
			}

			if indicators.EnableATR {
				if !writeConfiguredIndicators(&sb, data.LongerTermContext.ConfiguredIndicators, "ATR", "%.3f", "\n\n", limit) {
					sb.WriteString(fmt.Sprintf("3-Period ATR: %.3f vs. 14-Period ATR: %.3f\n\n",
						data.LongerTermContext.ATR3, data.LongerTermContext.ATR14))
				}
			}

			if indicators.EnableVolume {
				sb.WriteString(fmt.Sprintf("Current Volume: %.3f vs. Average Volume: %.3f\n\n",
					data.LongerTermContext.CurrentVolume, data.LongerTermContext.AverageVolume))
			}

			if indicators.EnableMACD {
				if !writeConfiguredIndicators(&sb, data.LongerTermContext.ConfiguredIndicators, "MACD", "", "\n\n", limit) && len(data.LongerTermContext.MACDValues) > 0 {
					sb.WriteString(fmt.Sprintf("MACD indicators: %s\n\n", formatSeriesTail(data.LongerTermContext.MACDValues, limit)))
				}
			}

			if indicators.EnableRSI {
				if !writeConfiguredIndicators(&sb, data.LongerTermContext.ConfiguredIndicators, "RSI", "", "\n\n", limit) && len(data.LongerTermContext.RSI14Values) > 0 {
					sb.WriteString(fmt.Sprintf("RSI indicators (14-Period): %s\n\n", formatSeriesTail(data.LongerTermContext.RSI14Values, limit)))
				}
			}
		}
	}

	return sb.String()
}

// writeConfiguredIndicators prints strategy-configured indicator series whose
// label starts with prefix (e.g. "EMA"), one line per series with the real
// period label — so adjusted periods actually reach the model. Single-value
// series use singleFmt ("" = always slice format). Returns true when at
// least one series was written, letting callers fall back to the legacy
// fixed-period fields.
func writeConfiguredIndicators(sb *strings.Builder, series []market.IndicatorSeries, prefix, singleFmt, lineSuffix string, limit int) bool {
	wrote := false
	for _, s := range series {
		if !strings.HasPrefix(s.Label, prefix) || len(s.Values) == 0 {
			continue
		}
		values := s.Values
		if limit <= 0 {
			limit = promptDisplayLimit
		}
		if len(values) > limit {
			values = values[len(values)-limit:]
		}
		if singleFmt != "" && len(values) == 1 {
			sb.WriteString(fmt.Sprintf("%s: %s%s", s.Label, fmt.Sprintf(singleFmt, values[0]), lineSuffix))
		} else {
			sb.WriteString(fmt.Sprintf("%s: %s%s", s.Label, formatFloatSlice(values), lineSuffix))
		}
		wrote = true
	}
	return wrote
}

// promptDisplayLimit caps how many bars / series values are printed per
// timeframe. The analysis window can be far longer (an hourly strategy needs
// 200 bars for EMA200 / multi-day structure), but printing all of it would
// inflate every prompt; the model needs the recent shape plus the current
// values, and the long lookback is already reflected in those values.
const promptDisplayLimit = 40

// resolveDisplayLimit returns the configured prompt display cap, falling back
// to the product default (see KlineConfig.PromptDisplayBars).
func resolveDisplayLimit(indicators store.IndicatorConfig) int {
	if n := indicators.Klines.PromptDisplayBars; n > 0 {
		return n
	}
	return promptDisplayLimit
}

func (e *StrategyEngine) formatTimeframeSeriesData(sb *strings.Builder, data *market.TimeframeSeriesData, indicators store.IndicatorConfig, includeKlines bool) {
	limit := resolveDisplayLimit(indicators)
	if len(data.Klines) > 0 {
		// Compact CSV rows instead of fixed-width columns: same data, roughly
		// 40% fewer characters, and enough significant digits for low-priced
		// coins (the old %-9.4f printed 0.0000 below 0.0001).
		if includeKlines {
			bars := data.Klines
			omitted := 0
			if len(bars) > promptDisplayLimit {
				omitted = len(bars) - promptDisplayLimit
				bars = bars[omitted:]
			}
			sb.WriteString("Time(UTC),Open,High,Low,Close,Volume\n")
			for i, k := range bars {
				marker := ""
				if i == len(bars)-1 {
					marker = ",<- current"
				}
				sb.WriteString(fmt.Sprintf("%s,%s,%s,%s,%s,%.2f%s\n",
					time.Unix(k.Time/1000, 0).UTC().Format("01-02 15:04"),
					formatCompactPrice(k.Open), formatCompactPrice(k.High),
					formatCompactPrice(k.Low), formatCompactPrice(k.Close),
					k.Volume, marker))
			}
			if omitted > 0 {
				sb.WriteString(fmt.Sprintf("(%d earlier bars omitted from the table; the indicators below cover the full window)\n", omitted))
			}
			sb.WriteString("\n")
		}
	} else if len(data.MidPrices) > 0 {
		sb.WriteString(fmt.Sprintf("Mid prices: %s\n\n", formatSeriesTail(data.MidPrices, limit)))
		if indicators.EnableVolume && len(data.Volume) > 0 {
			sb.WriteString(fmt.Sprintf("Volume: %s\n\n", formatSeriesTail(data.Volume, limit)))
		}
	}

	if indicators.EnableEMA {
		if !writeConfiguredIndicators(sb, data.ConfiguredIndicators, "EMA", "", "\n", limit) {
			if len(data.EMA20Values) > 0 {
				sb.WriteString(fmt.Sprintf("EMA20: %s\n", formatSeriesTail(data.EMA20Values, limit)))
			}
			if len(data.EMA50Values) > 0 {
				sb.WriteString(fmt.Sprintf("EMA50: %s\n", formatSeriesTail(data.EMA50Values, limit)))
			}
		}
	}

	if indicators.EnableMACD {
		if !writeConfiguredIndicators(sb, data.ConfiguredIndicators, "MACD", "", "\n", limit) && len(data.MACDValues) > 0 {
			sb.WriteString(fmt.Sprintf("MACD: %s\n", formatSeriesTail(data.MACDValues, limit)))
		}
	}

	if indicators.EnableRSI {
		if !writeConfiguredIndicators(sb, data.ConfiguredIndicators, "RSI", "", "\n", limit) {
			if len(data.RSI7Values) > 0 {
				sb.WriteString(fmt.Sprintf("RSI7: %s\n", formatSeriesTail(data.RSI7Values, limit)))
			}
			if len(data.RSI14Values) > 0 {
				sb.WriteString(fmt.Sprintf("RSI14: %s\n", formatSeriesTail(data.RSI14Values, limit)))
			}
		}
	}

	if indicators.EnableATR {
		if !writeConfiguredIndicators(sb, data.ConfiguredIndicators, "ATR", "%.4f", "\n", limit) && data.ATR14 > 0 {
			sb.WriteString(fmt.Sprintf("ATR14: %.4f\n", data.ATR14))
		}
	}

	if indicators.EnableBOLL {
		if !writeConfiguredIndicators(sb, data.ConfiguredIndicators, "BOLL", "", "\n", limit) {
			if len(data.BOLLUpper) > 0 {
				sb.WriteString(fmt.Sprintf("BOLL Upper: %s\n", formatSeriesTail(data.BOLLUpper, limit)))
				sb.WriteString(fmt.Sprintf("BOLL Middle: %s\n", formatSeriesTail(data.BOLLMiddle, limit)))
				sb.WriteString(fmt.Sprintf("BOLL Lower: %s\n", formatSeriesTail(data.BOLLLower, limit)))
			}
		}
	}

	// ADX / Supertrend are regime gauges: printed for the primary timeframe
	// only, to keep the prompt budget in check.
	if data.Timeframe == "" || indicators.Klines.PrimaryTimeframe == "" || data.Timeframe == indicators.Klines.PrimaryTimeframe {
		if indicators.EnableADX {
			writeConfiguredIndicators(sb, data.ConfiguredIndicators, "ADX", "%.1f", "\n", limit)
			writeConfiguredIndicators(sb, data.ConfiguredIndicators, "DI+", "%.1f", "\n", limit)
			writeConfiguredIndicators(sb, data.ConfiguredIndicators, "DI-", "%.1f", "\n", limit)
		}
		if indicators.EnableSupertrend {
			writeConfiguredIndicators(sb, data.ConfiguredIndicators, "SUPERTREND(", "%.4f", "\n", limit)
			writeConfiguredIndicators(sb, data.ConfiguredIndicators, "SUPERTREND_DIR", "%.0f", "\n", limit)
		}
	}

	sb.WriteString("\n")
}

// formatCompactPrice renders a price without padding, with enough
// significant digits for low-priced coins.
func formatCompactPrice(v float64) string {
	switch {
	case v == 0:
		return "0"
	case math.Abs(v) >= 1:
		return strconv.FormatFloat(v, 'f', 4, 64)
	case math.Abs(v) >= 0.01:
		return strconv.FormatFloat(v, 'f', 5, 64)
	default:
		return strconv.FormatFloat(v, 'f', 8, 64)
	}
}

// formatSeriesTail prints only the most recent values of a series (see
// promptDisplayLimit) so a long analysis window never inflates the prompt,
// whatever path produced the series.
func formatSeriesTail(values []float64, limit int) string {
	if limit <= 0 {
		limit = promptDisplayLimit
	}
	if len(values) > limit {
		values = values[len(values)-limit:]
	}
	return formatFloatSlice(values)
}

func (e *StrategyEngine) formatQuantData(data *QuantData) string {
	if data == nil {
		return ""
	}

	indicators := e.config.Indicators
	if !indicators.EnableQuantOI && !indicators.EnableQuantNetflow {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📊 %s Quantitative Data:\n", data.Symbol))

	if len(data.PriceChange) > 0 {
		sb.WriteString("Price Change: ")
		timeframes := []string{"5m", "15m", "1h", "4h", "12h", "24h"}
		parts := []string{}
		for _, tf := range timeframes {
			if v, ok := data.PriceChange[tf]; ok {
				parts = append(parts, fmt.Sprintf("%s: %+.4f%%", tf, v*100))
			}
		}
		sb.WriteString(strings.Join(parts, " | "))
		sb.WriteString("\n")
	}

	if indicators.EnableQuantNetflow && data.Netflow != nil {
		sb.WriteString("Fund Flow (Netflow):\n")
		timeframes := []string{"5m", "15m", "1h", "4h", "12h", "24h"}

		if data.Netflow.Institution != nil {
			if data.Netflow.Institution.Future != nil && len(data.Netflow.Institution.Future) > 0 {
				sb.WriteString("  Institutional Futures:\n")
				for _, tf := range timeframes {
					if v, ok := data.Netflow.Institution.Future[tf]; ok {
						sb.WriteString(fmt.Sprintf("    %s: %s\n", tf, formatFlowValue(v)))
					}
				}
			}
			if data.Netflow.Institution.Spot != nil && len(data.Netflow.Institution.Spot) > 0 {
				sb.WriteString("  Institutional Spot:\n")
				for _, tf := range timeframes {
					if v, ok := data.Netflow.Institution.Spot[tf]; ok {
						sb.WriteString(fmt.Sprintf("    %s: %s\n", tf, formatFlowValue(v)))
					}
				}
			}
		}

		if data.Netflow.Personal != nil {
			if data.Netflow.Personal.Future != nil && len(data.Netflow.Personal.Future) > 0 {
				sb.WriteString("  Retail Futures:\n")
				for _, tf := range timeframes {
					if v, ok := data.Netflow.Personal.Future[tf]; ok {
						sb.WriteString(fmt.Sprintf("    %s: %s\n", tf, formatFlowValue(v)))
					}
				}
			}
			if data.Netflow.Personal.Spot != nil && len(data.Netflow.Personal.Spot) > 0 {
				sb.WriteString("  Retail Spot:\n")
				for _, tf := range timeframes {
					if v, ok := data.Netflow.Personal.Spot[tf]; ok {
						sb.WriteString(fmt.Sprintf("    %s: %s\n", tf, formatFlowValue(v)))
					}
				}
			}
		}
	}

	if indicators.EnableQuantOI && len(data.OI) > 0 {
		for exchange, oiData := range data.OI {
			if len(oiData.Delta) > 0 {
				sb.WriteString(fmt.Sprintf("Open Interest (%s):\n", exchange))
				for _, tf := range []string{"5m", "15m", "1h", "4h", "12h", "24h"} {
					if d, ok := oiData.Delta[tf]; ok {
						sb.WriteString(fmt.Sprintf("    %s: %+.4f%% (%s)\n", tf, d.OIDeltaPercent, formatFlowValue(d.OIDeltaValue)))
					}
				}
			}
		}
	}

	return sb.String()
}

func formatFlowValue(v float64) string {
	sign := ""
	if v >= 0 {
		sign = "+"
	}
	absV := v
	if absV < 0 {
		absV = -absV
	}
	if absV >= 1e9 {
		return fmt.Sprintf("%s%.2fB", sign, v/1e9)
	} else if absV >= 1e6 {
		return fmt.Sprintf("%s%.2fM", sign, v/1e6)
	} else if absV >= 1e3 {
		return fmt.Sprintf("%s%.2fK", sign, v/1e3)
	}
	return fmt.Sprintf("%s%.2f", sign, v)
}

func formatFloatSlice(values []float64) string {
	strValues := make([]string, len(values))
	for i, v := range values {
		strValues[i] = fmt.Sprintf("%.4f", v)
	}
	return "[" + strings.Join(strValues, ", ") + "]"
}
