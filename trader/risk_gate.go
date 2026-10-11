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

	PositionCount    int
	HasSamePosition  bool
	Equity           float64
	AvailableBalance float64
	MarginUsedPct    float64 // current margin usage in percent (0-100)
	TakerFeeRate     float64 // taker fee as a fraction of notional; 0 = use default

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
// guard every opening order. Checks run in this order: max positions →
// duplicate position → leverage clamp → position-value cap → balance
// sufficiency (auto-reduce) → minimum size → max margin usage. It is a pure
// function so it can be unit-tested and reused by the backtester.
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

	// 1. Max concurrent positions.
	maxPositions := rc.MaxPositions
	if maxPositions <= 0 {
		maxPositions = 3
	}
	if in.PositionCount >= maxPositions {
		return riskReject(fmt.Sprintf("already at max positions (%d/%d)", in.PositionCount, maxPositions))
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
			res.AdjustedSizeUSD = maxAffordable * positionSizeSafetyFactor
		}
	}

	// 6. Minimum position size.
	minSize := rc.MinPositionSize
	if minSize <= 0 {
		minSize = 12
	}
	if res.AdjustedSizeUSD < minSize {
		return riskReject(fmt.Sprintf("position %.2f USDT below minimum (%.2f USDT)", res.AdjustedSizeUSD, minSize))
	}

	// 7. Max margin usage: projected margin must stay within the configured
	// fraction of equity.
	if limit := rc.MaxMarginUsage; limit > 0 && in.Equity > 0 {
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

// checkOpenRiskGate runs the full pre-trade gate for an opening decision:
// DB-backed protections first, then the value-only risk gate.
func (at *AutoTrader) checkOpenRiskGate(decision *kernel.Decision, positions []map[string]interface{}, equity, availableBalance float64) RiskGateResult {
	if reason := at.checkOpenProtections(decision.Symbol); reason != "" {
		return RiskGateResult{Allowed: false, Reason: "blocked by protection: " + reason}
	}
	return EvaluateRiskGate(RiskGateInput{
		Action:           decision.Action,
		Symbol:           decision.Symbol,
		PositionSizeUSD:  decision.PositionSizeUSD,
		Leverage:         decision.Leverage,
		PositionCount:    len(positions),
		HasSamePosition:  hasPosition(positions, decision.Symbol, decision.Action),
		Equity:           equity,
		AvailableBalance: availableBalance,
		MarginUsedPct:    marginUsagePct(positions, equity),
		TakerFeeRate:     connector.FeeModelFor(at.exchange).TakerRate(),
		Config:           at.config.StrategyConfig,
	})
}

// accountEquityAndAvailable extracts equity (with the same fallbacks the
// trading context uses) and available balance from an exchange balance map.
func accountEquityAndAvailable(balance map[string]interface{}) (equity, available float64) {
	if avail, ok := balance["availableBalance"].(float64); ok {
		available = avail
	}
	if eq, ok := balance["totalEquity"].(float64); ok && eq > 0 {
		equity = eq
	} else if eq, ok := balance["totalWalletBalance"].(float64); ok && eq > 0 {
		equity = eq
	} else {
		equity = available
	}
	return equity, available
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
