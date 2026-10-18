package trader

import (
	"fmt"
	"math"
	"strings"

	"nofx/kernel"
	"nofx/logger"
	"nofx/market"
)

// Holder-structure gate: a narrow, code-enforced objection to an opening order
// that the position structure says is badly placed — an entry sitting on top of a
// liquidation cascade, or one running straight into a dominant cost wall.
//
// It follows the same contract as the other data-quality guards:
//   - Fail open. No profile, a distribution too thin to read, or an unrecognised
//     action all pass. A data gap must never freeze trading.
//   - Fail closed only on a computed hostile read, and only in block mode.
//
// The live path ships in warn mode: it records the objection without acting on
// it, because there is no offline evidence yet that blocking pays. The block mode
// is implemented and tested so the replay can measure exactly what it would have
// done before it is ever switched on.
const (
	StructureGateOff   = "off"
	StructureGateWarn  = "warn"
	StructureGateBlock = "block"
)

const (
	// structureCascadeFuelPct is how much of a side's liquidation notional may sit
	// inside the near window before an entry on that side is called hostile: a
	// quarter of the band's forced flow within 5% of the price is fuel at hand.
	structureCascadeFuelPct = 25.0
	// structureWallProximityPct is how close a dominant cost wall may be before it
	// counts as being in the way of the entry.
	structureWallProximityPct = 1.0
	// structureWallDominancePct is the share of the cost distribution that makes a
	// cluster a wall rather than background cost.
	structureWallDominancePct = 30.0
	// structureMinPositionsPlaced guards the read: below it the distribution is too
	// thin to object on, so the gate passes.
	structureMinPositionsPlaced = 30
)

// StructureGateInput is the value-only input of the structure gate. Keeping it
// free of exchange/DB handles lets the same rules run live and in the offline
// replay.
type StructureGateInput struct {
	Mode    string // off | warn | block
	Action  string // "open_long" | "open_short"
	Symbol  string
	Profile *market.PositionProfile // nil when the market was never collected
}

// StructureGateResult is the verdict: Allowed reflects the mode (an objection is
// enforced only in block mode), and Reason carries the objection itself, empty
// when there is none.
type StructureGateResult struct {
	Allowed bool
	Reason  string
}

// EvaluateStructureGate objects to an opening order the holder structure says is
// badly placed, and reports the objection without enforcing it in warn mode. It is
// a pure function so it can be unit-tested and reused by the backtester.
func EvaluateStructureGate(in StructureGateInput) StructureGateResult {
	if in.Mode != StructureGateWarn && in.Mode != StructureGateBlock {
		// Off, unset or unrecognised all mean "not turned on": an unknown value
		// must never start objecting to orders.
		return StructureGateResult{Allowed: true}
	}
	if in.Profile == nil {
		return StructureGateResult{Allowed: true}
	}
	if in.Profile.Coverage.PositionsPlaced < structureMinPositionsPlaced {
		return StructureGateResult{Allowed: true}
	}

	action := strings.ToLower(strings.TrimSpace(in.Action))
	reason := ""
	switch action {
	case "open_long", "long":
		reason = structureObjectionLong(in.Profile)
	case "open_short", "short":
		reason = structureObjectionShort(in.Profile)
	}
	if reason == "" {
		return StructureGateResult{Allowed: true}
	}
	return StructureGateResult{Allowed: in.Mode != StructureGateBlock, Reason: reason}
}

// structureObjectionLong objects when a long would be opened on top of a
// liquidation cascade (forced selling within the near window below it) or straight
// into a dominant cost wall above it.
func structureObjectionLong(p *market.PositionProfile) string {
	if p.LongLiqWithinDownPct >= structureCascadeFuelPct {
		return fmt.Sprintf("long entry sits on a liquidation cascade: %.0f%% of long liquidation notional is within %.0f%% below the price (largest cluster %.4f at %+.2f%%, %.1f%% of all liquidation)",
			p.LongLiqWithinDownPct, market.PositionFuelWindowPct, p.LiqCluster, p.LiqClusterDistancePct, p.LiqClusterSharePct)
	}
	if wallInTheWay(p.CostWallAbove, p.CostWallAboveDistancePct, p.CostWallAboveSharePct, true) {
		return fmt.Sprintf("long entry runs into a cost wall at %.4f (%+.2f%%, %.1f%% of the cost distribution)",
			p.CostWallAbove, p.CostWallAboveDistancePct, p.CostWallAboveSharePct)
	}
	return ""
}

// structureObjectionShort is the mirror: a short opened under a squeeze cluster,
// or into a wall below it that it would have to break.
func structureObjectionShort(p *market.PositionProfile) string {
	if p.ShortLiqWithinUpPct >= structureCascadeFuelPct {
		return fmt.Sprintf("short entry sits under a squeeze cluster: %.0f%% of short liquidation notional is within %.0f%% above the price (largest cluster %.4f at %+.2f%%, %.1f%% of all liquidation)",
			p.ShortLiqWithinUpPct, market.PositionFuelWindowPct, p.LiqCluster, p.LiqClusterDistancePct, p.LiqClusterSharePct)
	}
	if wallInTheWay(p.CostWallBelow, p.CostWallBelowDistancePct, p.CostWallBelowSharePct, false) {
		return fmt.Sprintf("short entry runs into a cost wall at %.4f (%+.2f%%, %.1f%% of the cost distribution)",
			p.CostWallBelow, p.CostWallBelowDistancePct, p.CostWallBelowSharePct)
	}
	return ""
}

// wallInTheWay reports whether a dominant cost cluster sits right in the path of
// the entry: close enough to matter, on the side the move has to push through.
func wallInTheWay(price, distancePct, sharePct float64, above bool) bool {
	if price <= 0 || sharePct < structureWallDominancePct {
		return false
	}
	if above && distancePct < 0 {
		return false
	}
	if !above && distancePct > 0 {
		return false
	}
	return math.Abs(distancePct) <= structureWallProximityPct
}

// structureGateMode is the mode the live path runs in: warn while the feature is
// on, off when it is not. Blocking stays an offline experiment until the replay
// shows it pays.
func (at *AutoTrader) structureGateMode() string {
	if at.config.StrategyConfig == nil || !at.config.StrategyConfig.Indicators.PositionStructureEnabled() {
		return StructureGateOff
	}
	return StructureGateWarn
}

// checkStructureGate reports the structure's objection to an opening order. In the
// live path the objection is logged, not enforced: an error is returned only in
// block mode, which the offline replay drives.
func (at *AutoTrader) checkStructureGate(ctx *kernel.Context, symbol, action string) error {
	mode := at.structureGateMode()
	if mode == StructureGateOff {
		return nil
	}

	var profile *market.PositionProfile
	if ctx != nil && ctx.PositionProfileMap != nil {
		profile = ctx.PositionProfileMap[symbol]
	}

	result := EvaluateStructureGate(StructureGateInput{Mode: mode, Action: action, Symbol: symbol, Profile: profile})
	if result.Reason == "" {
		return nil
	}
	if !result.Allowed {
		logger.Infof("  🛡️ [STRUCTURE GATE] %s blocked — %s", symbol, result.Reason)
		return fmt.Errorf("❌ [STRUCTURE GATE] %s", result.Reason)
	}

	at.logInfof("  ℹ️ [STRUCTURE GATE warn] %s: %s", symbol, result.Reason)
	return nil
}
