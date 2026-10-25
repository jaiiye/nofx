package trader

import (
	"fmt"
	"strings"

	"nofx/kernel"
	"nofx/market"
	"nofx/store"
	"nofx/trader/connector"
)

// RiskGateInput is the value-only input to the pre-trade risk gate. Keeping it
// free of exchange/DB handles lets the same rules run in live trading and in
// backtests.
type RiskGateInput struct {
	Action          string // "open_long" | "open_short"
	Symbol          string
	PositionSizeUSD float64 // requested notional, before adjustments
	Leverage        int
	Price           float64
	// Confidence is the model's own confidence for this opening decision. The
	// gate rejects it below the configured MinConfidence floor, so an opening
	// decision that omits it (zero value) is rejected as well.
	Confidence int

	PositionCount int
	// SuccessfulClosesInCycle counts positions successfully closed earlier in
	// the same decision cycle. Exchange position APIs lag the fills, so the
	// raw PositionCount still includes just-closed positions — the gate
	// judges max positions by the expected net count instead.
	SuccessfulClosesInCycle int
	HasSamePosition         bool
	Equity                  float64
	AvailableBalance        float64
	MarginUsedPct           float64 // current margin usage in percent (0-100)
	TakerFeeRate            float64 // taker fee as a fraction of notional; 0 = use default

	Config *store.StrategyConfig
}

// RiskGateResult is the verdict of the pre-trade risk gate. The adjusted
// fields are only meaningful when Allowed is true.
type RiskGateResult struct {
	Allowed          bool
	Reason           string
	AdjustedSizeUSD  float64
	AdjustedLeverage int
}

// EvaluateRiskGate applies the code-enforced sizing and leverage limits that
// guard every opening order. Checks run in this order: confidence floor →
// max positions → duplicate position → leverage clamp → position-value cap →
// balance sufficiency (auto-reduce) → minimum size → max margin usage. It is a
// pure function so it can be unit-tested and reused by the backtester.
func EvaluateRiskGate(in RiskGateInput) RiskGateResult {
	res := RiskGateResult{
		Allowed:          true,
		AdjustedSizeUSD:  in.PositionSizeUSD,
		AdjustedLeverage: in.Leverage,
	}
	if in.Config == nil {
		return res // no strategy config: don't second-guess the decision
	}
	rc := in.Config.RiskControl

	// Confidence floor. The prompt has always claimed a minimum confidence was
	// required, but nothing enforced it: Decision.Confidence was only copied into
	// the record, so the model could open at any confidence as long as size and
	// R:R passed. The floor is normalized here (a zero config means the product
	// minimum, not "off"), so the gate stays authoritative for legacy configs too.
	minConfidence := rc.MinConfidence
	if minConfidence < store.MinConfidence {
		minConfidence = store.MinConfidence
	} else if minConfidence > store.MaxConfidence {
		minConfidence = store.MaxConfidence
	}
	if in.Confidence < minConfidence {
		return riskReject(fmt.Sprintf("confidence %d is below the configured minimum of %d — no position opened", in.Confidence, minConfidence))
	}

	// 1. Max concurrent positions, judged by the expected net count: closes
	// executed earlier in this cycle may not be reflected in PositionCount
	// yet (exchange API lag), and a close-then-open rebalance must not be
	// misread as "position full".
	maxPositions := rc.MaxPositions
	if maxPositions <= 0 {
		maxPositions = 3
	}
	expectedNet := in.PositionCount - in.SuccessfulClosesInCycle
	if expectedNet < 0 {
		expectedNet = 0
	}
	if expectedNet >= maxPositions {
		return riskReject(fmt.Sprintf("already at max positions (%d/%d, %d closed this cycle not yet reflected)",
			in.PositionCount, maxPositions, in.SuccessfulClosesInCycle))
	}

	// 2. No duplicate position on the same symbol+side.
	if in.HasSamePosition {
		return riskReject(fmt.Sprintf("%s already has an open %s position, close it first",
			in.Symbol, sideLabel(in.Action)))
	}

	// 3. Leverage clamp against the symbol tier's configured maximum.
	if maxLev := maxLeverageFor(in.Symbol, rc); maxLev > 0 && res.AdjustedLeverage > maxLev {
		res.AdjustedLeverage = maxLev
	}
	if res.AdjustedLeverage < 1 {
		res.AdjustedLeverage = 1
	}

	// 4. Position value cap: notional <= equity × ratio.
	if ratio := positionValueRatioFor(in.Symbol, rc); in.Equity > 0 && ratio > 0 {
		if maxValue := in.Equity * ratio; res.AdjustedSizeUSD > maxValue {
			res.AdjustedSizeUSD = maxValue
		}
	}

	// 5. Balance sufficiency: reduce to what the available margin can cover.
	if res.AdjustedLeverage > 0 {
		feeRate := in.TakerFeeRate
		if feeRate <= 0 {
			feeRate = takerFeeRate // legacy default when no venue model is supplied
		}
		marginFactor := marginOverheadFactor/float64(res.AdjustedLeverage) + feeRate
		if maxAffordable := in.AvailableBalance / marginFactor; res.AdjustedSizeUSD > maxAffordable {
			// Zero available margin is not a sizing problem: name the empty pool
			// instead of reporting a position of 0.00 USDT (which reads as if the
			// model had asked for nothing).
			if in.AvailableBalance <= 0 {
				return riskReject(noMarginReason(in.Symbol, in.AvailableBalance))
			}
			res.AdjustedSizeUSD = maxAffordable * positionSizeSafetyFactor
		}
	}

	// 6. Minimum position size. Three different causes land on this check and each
	// needs a different fix, so name the one that applies instead of always
	// reporting "0.00 USDT below minimum".
	minSize := rc.MinPositionSize
	if minSize <= 0 {
		minSize = 12
	}
	if res.AdjustedSizeUSD < minSize {
		switch {
		case in.PositionSizeUSD <= 0:
			return riskReject(fmt.Sprintf("the decision did not specify a position size for %s (0 USDT, minimum %.2f USDT) — the model must size the trade", in.Symbol, minSize))
		case in.AvailableBalance <= 0:
			return riskReject(noMarginReason(in.Symbol, in.AvailableBalance))
		case res.AdjustedSizeUSD < in.PositionSizeUSD:
			return riskReject(fmt.Sprintf("position reduced to %.2f USDT (from %.2f requested) by %.2f USDT of available margin, below the %.2f minimum — free up margin or lower min_position_size",
				res.AdjustedSizeUSD, in.PositionSizeUSD, in.AvailableBalance, minSize))
		default:
			return riskReject(fmt.Sprintf("position %.2f USDT below minimum (%.2f USDT)", res.AdjustedSizeUSD, minSize))
		}
	}

	// 7. Max margin usage: projected margin must stay within the configured
	// fraction of equity. The limit is normalized here too — the gate has to
	// stay authoritative even for configs written by a path that skipped
	// ClampLimits, where a zero would otherwise skip this check entirely.
	if in.Equity > 0 {
		limit := store.NormalizeMarginUsageLimit(rc.MaxMarginUsage)
		marginNeeded := res.AdjustedSizeUSD / float64(res.AdjustedLeverage)
		currentMargin := in.MarginUsedPct / 100 * in.Equity
		projectedPct := (currentMargin + marginNeeded) / in.Equity * 100
		if projectedPct > limit*100 {
			return riskReject(fmt.Sprintf("projected margin usage %.1f%% exceeds limit %.0f%%",
				projectedPct, limit*100))
		}
	}

	return res
}

func riskReject(reason string) RiskGateResult {
	return RiskGateResult{Allowed: false, Reason: reason}
}

func maxLeverageFor(symbol string, rc store.RiskControlConfig) int {
	if isMajorAsset(symbol) {
		return rc.BTCETHMaxLeverage
	}
	return rc.AltcoinMaxLeverage
}

func positionValueRatioFor(symbol string, rc store.RiskControlConfig) float64 {
	if isMajorAsset(symbol) {
		if rc.BTCETHMaxPositionValueRatio > 0 {
			return rc.BTCETHMaxPositionValueRatio
		}
		return 5.0
	}
	if rc.AltcoinMaxPositionValueRatio > 0 {
		return rc.AltcoinMaxPositionValueRatio
	}
	return 1.0
}

func sideLabel(action string) string {
	if strings.HasSuffix(action, "short") {
		return "short"
	}
	return "long"
}

// noMarginReason explains a zero available margin. For HIP-3 assets the account's
// abstraction mode decides where the collateral lives, so the message names both
// possibilities instead of asserting the wrong one.
func noMarginReason(symbol string, available float64) string {
	if market.IsXyzDexAsset(symbol) {
		return fmt.Sprintf("no available margin for %s (%.2f USDC) — unified/portfolio accounts trade %s from the single USDC balance, while standard-mode accounts keep a separate xyz dex balance that must be funded first; check the account's abstraction mode", symbol, available, symbol)
	}
	return fmt.Sprintf("no available margin for %s (%.2f USDC available) — free up margin or reduce open positions", symbol, available)
}

// checkOpenRiskGate runs the full pre-trade gate for an opening decision:
// DB-backed protections first, then the value-only risk gate.
func (at *AutoTrader) checkOpenRiskGate(decision *kernel.Decision, positions []map[string]interface{}, equity, availableBalance float64) RiskGateResult {
	if reason := at.checkOpenProtections(decision.Symbol); reason != "" {
		return RiskGateResult{Allowed: false, Reason: "blocked by protection: " + reason}
	}
	return EvaluateRiskGate(RiskGateInput{
		Action:                  decision.Action,
		Symbol:                  decision.Symbol,
		PositionSizeUSD:         decision.PositionSizeUSD,
		Leverage:                decision.Leverage,
		Confidence:              decision.Confidence,
		PositionCount:           len(positions),
		SuccessfulClosesInCycle: at.successfulClosesInCycle,
		HasSamePosition:         hasPosition(positions, decision.Symbol, decision.Action),
		Equity:                  equity,
		AvailableBalance:        availableBalance,
		MarginUsedPct:           marginUsagePct(positions, equity),
		TakerFeeRate:            at.takerFeeRate(),
		Config:                  at.config.StrategyConfig,
	})
}

// takerFeeRate prefers the venue's real rate for this account — fees decide the
// balance-sufficiency sizing, and the static per-venue model can differ from
// the account's actual tier. Falls back to the model when the venue cannot be
// queried.
func (at *AutoTrader) takerFeeRate() float64 {
	if provider, ok := at.trader.(interface{ TakerFeeRate() float64 }); ok {
		if rate := provider.TakerFeeRate(); rate > 0 {
			return rate
		}
	}
	return connector.FeeModelFor(at.exchange).TakerRate()
}

// hasPosition reports whether positions already contains symbol on the side
// implied by action.
func hasPosition(positions []map[string]interface{}, symbol, action string) bool {
	side := sideLabel(action)
	for _, p := range positions {
		s, _ := p["symbol"].(string)
		ps, _ := p["side"].(string)
		if s == symbol && ps == side {
			return true
		}
	}
	return false
}

// marginUsagePct sums the margin used by open positions as a percentage of
// equity, matching the context builder's estimate.
func marginUsagePct(positions []map[string]interface{}, equity float64) float64 {
	if equity <= 0 {
		return 0
	}
	var used float64
	for _, p := range positions {
		qty, _ := p["positionAmt"].(float64)
		if qty < 0 {
			qty = -qty
		}
		mark, _ := p["markPrice"].(float64)
		lev := 10.0
		if l, ok := p["leverage"].(float64); ok && l > 0 {
			lev = l
		}
		used += qty * mark / lev
	}
	return used / equity * 100
}

// normalizePosKey builds a case-insensitive symbol+side key for reconciliation.
func normalizePosKey(symbol, side string) string {
	return strings.ToUpper(side) + ":" + market.Normalize(symbol)
}
