package trader

import (
	"testing"

	"nofx/kernel"
	"nofx/market"
	"nofx/store"
)

// structureProfile states only the numbers the gate reads, so each case is
// obvious at a glance.
func structureProfile(placed int, mutate func(*market.PositionProfile)) *market.PositionProfile {
	profile := &market.PositionProfile{
		MarkPrice: 100,
		Coverage: market.PositionCoverage{
			AddressesSeen:   placed,
			PositionsSeen:   placed,
			PositionsPlaced: placed,
			WeightSource:    market.PositionWeightSourceNotional,
		},
	}
	if mutate != nil {
		mutate(profile)
	}
	return profile
}

// hostileLong: forced selling piled up just below the price.
func hostileLong() *market.PositionProfile {
	return structureProfile(200, func(p *market.PositionProfile) {
		p.LongLiqWithinDownPct = 40
		p.LiqCluster = 96
		p.LiqClusterDistancePct = -4
		p.LiqClusterSharePct = 20
	})
}

// hostileShort: the mirror, squeeze fuel just above the price.
func hostileShort() *market.PositionProfile {
	return structureProfile(200, func(p *market.PositionProfile) {
		p.ShortLiqWithinUpPct = 40
		p.LiqCluster = 104
		p.LiqClusterDistancePct = 4
		p.LiqClusterSharePct = 20
	})
}

func benign() *market.PositionProfile {
	return structureProfile(200, nil)
}

func TestEvaluateStructureGateFailsOpen(t *testing.T) {
	cases := []struct {
		name string
		in   StructureGateInput
	}{
		{"mode off", StructureGateInput{Mode: StructureGateOff, Action: "open_long", Profile: hostileLong()}},
		{"mode unset", StructureGateInput{Action: "open_long", Profile: hostileLong()}},
		{"no profile", StructureGateInput{Mode: StructureGateBlock, Action: "open_long"}},
		{"uncollected market", StructureGateInput{Mode: StructureGateBlock, Action: "open_long", Profile: &market.PositionProfile{}}},
		{"thin distribution", StructureGateInput{Mode: StructureGateBlock, Action: "open_long",
			Profile: structureProfile(structureMinPositionsPlaced-1, func(p *market.PositionProfile) { p.LongLiqWithinDownPct = 90 })}},
		{"close is never gated", StructureGateInput{Mode: StructureGateBlock, Action: "close_long", Profile: hostileLong()}},
		{"unknown action", StructureGateInput{Mode: StructureGateBlock, Action: "rebalance", Profile: hostileLong()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := EvaluateStructureGate(tc.in)
			if !result.Allowed || result.Reason != "" {
				t.Fatalf("must pass: %+v", result)
			}
		})
	}
}

func TestEvaluateStructureGateObjectsOnCascadeFuel(t *testing.T) {
	blocked := EvaluateStructureGate(StructureGateInput{Mode: StructureGateBlock, Action: "open_long", Profile: hostileLong()})
	if blocked.Allowed || blocked.Reason == "" {
		t.Fatalf("block mode must refuse and say why: %+v", blocked)
	}
	warned := EvaluateStructureGate(StructureGateInput{Mode: StructureGateWarn, Action: "open_long", Profile: hostileLong()})
	if !warned.Allowed || warned.Reason == "" {
		t.Fatalf("warn mode must report without enforcing: %+v", warned)
	}
	if warned.Reason != blocked.Reason {
		t.Fatalf("warn and block must give the same reason: %q vs %q", warned.Reason, blocked.Reason)
	}

	// Side sensitivity: the long reading says nothing about a short, and the short
	// reading nothing about a long.
	if result := EvaluateStructureGate(StructureGateInput{Mode: StructureGateBlock, Action: "open_short", Profile: hostileLong()}); !result.Allowed {
		t.Fatalf("long fuel must not object to a short: %+v", result)
	}
	if result := EvaluateStructureGate(StructureGateInput{Mode: StructureGateBlock, Action: "open_long", Profile: hostileShort()}); !result.Allowed {
		t.Fatalf("short fuel must not object to a long: %+v", result)
	}
	if result := EvaluateStructureGate(StructureGateInput{Mode: StructureGateBlock, Action: "open_short", Profile: hostileShort()}); result.Allowed {
		t.Fatalf("short squeeze fuel must object to a short: %+v", result)
	}
	if result := EvaluateStructureGate(StructureGateInput{Mode: StructureGateBlock, Action: "open_long", Profile: benign()}); !result.Allowed {
		t.Fatalf("a quiet distribution must not object: %+v", result)
	}
}

// The thresholds are decisions, so they are pinned here: exactly at the limit
// objects, just inside it does not.
func TestEvaluateStructureGateFuelThresholdIsPinned(t *testing.T) {
	atLimit := structureProfile(200, func(p *market.PositionProfile) { p.LongLiqWithinDownPct = structureCascadeFuelPct })
	if result := EvaluateStructureGate(StructureGateInput{Mode: StructureGateBlock, Action: "open_long", Profile: atLimit}); result.Allowed {
		t.Fatalf("fuel exactly at %.0f%% must object: %+v", structureCascadeFuelPct, result)
	}
	justUnder := structureProfile(200, func(p *market.PositionProfile) { p.LongLiqWithinDownPct = structureCascadeFuelPct - 0.1 })
	if result := EvaluateStructureGate(StructureGateInput{Mode: StructureGateBlock, Action: "open_long", Profile: justUnder}); !result.Allowed {
		t.Fatalf("fuel just under the limit must pass: %+v", result)
	}
}

func TestEvaluateStructureGateObjectsOnWallInTheWay(t *testing.T) {
	wallAbove := structureProfile(200, func(p *market.PositionProfile) {
		p.CostWallAbove = 101
		p.CostWallAboveDistancePct = 1
		p.CostWallAboveSharePct = 35
	})
	if result := EvaluateStructureGate(StructureGateInput{Mode: StructureGateBlock, Action: "open_long", Profile: wallAbove}); result.Allowed {
		t.Fatalf("a long into a dominant wall must object: %+v", result)
	}
	// Far wall: still a wall, but not in the way.
	farWall := structureProfile(200, func(p *market.PositionProfile) {
		p.CostWallAbove = 120
		p.CostWallAboveDistancePct = 20
		p.CostWallAboveSharePct = 35
	})
	if result := EvaluateStructureGate(StructureGateInput{Mode: StructureGateBlock, Action: "open_long", Profile: farWall}); !result.Allowed {
		t.Fatalf("a distant wall must not object: %+v", result)
	}
	// Weak cluster: too small to be a wall at all.
	weakWall := structureProfile(200, func(p *market.PositionProfile) {
		p.CostWallAbove = 101
		p.CostWallAboveDistancePct = 1
		p.CostWallAboveSharePct = structureWallDominancePct - 1
	})
	if result := EvaluateStructureGate(StructureGateInput{Mode: StructureGateBlock, Action: "open_long", Profile: weakWall}); !result.Allowed {
		t.Fatalf("a cluster below the dominance floor is not a wall: %+v", result)
	}

	// The mirror for shorts: the wall below is what a short has to break.
	wallBelow := structureProfile(200, func(p *market.PositionProfile) {
		p.CostWallBelow = 99
		p.CostWallBelowDistancePct = -1
		p.CostWallBelowSharePct = 35
	})
	if result := EvaluateStructureGate(StructureGateInput{Mode: StructureGateBlock, Action: "open_short", Profile: wallBelow}); result.Allowed {
		t.Fatalf("a short into a dominant wall must object: %+v", result)
	}
	if result := EvaluateStructureGate(StructureGateInput{Mode: StructureGateBlock, Action: "open_long", Profile: wallBelow}); !result.Allowed {
		t.Fatalf("a wall below is not in the way of a long: %+v", result)
	}
}

// The live path runs in warn mode: the objection is recorded, never enforced.
func TestCheckStructureGateWarnsWithoutBlocking(t *testing.T) {
	cfg := store.GetDefaultStrategyConfig("en")
	enabled := true
	cfg.Indicators.EnablePositionStructure = &enabled

	at := &AutoTrader{id: "t1", name: "gate-test", config: AutoTraderConfig{StrategyConfig: &cfg}}
	ctx := &kernel.Context{PositionProfileMap: map[string]*market.PositionProfile{"BTCUSDT": hostileLong()}}

	if err := at.checkStructureGate(ctx, "BTCUSDT", "open_long"); err != nil {
		t.Fatalf("warn mode must not block the order: %v", err)
	}

	// Fail-open at the call site as well: no context, no profile.
	if err := at.checkStructureGate(nil, "BTCUSDT", "open_long"); err != nil {
		t.Fatalf("a missing context must pass: %v", err)
	}
	if err := at.checkStructureGate(&kernel.Context{}, "BTCUSDT", "open_long"); err != nil {
		t.Fatalf("an empty profile map must pass: %v", err)
	}

	// With the feature off the gate does not look at anything.
	offCfg := store.GetDefaultStrategyConfig("en")
	off := &AutoTrader{id: "t2", name: "gate-off", config: AutoTraderConfig{StrategyConfig: &offCfg}}
	if err := off.checkStructureGate(ctx, "BTCUSDT", "open_long"); err != nil {
		t.Fatalf("a disabled feature must pass: %v", err)
	}
	if mode := off.structureGateMode(); mode != StructureGateOff {
		t.Fatalf("mode = %q, want off while the feature is disabled", mode)
	}
	if mode := at.structureGateMode(); mode != StructureGateWarn {
		t.Fatalf("mode = %q, want warn for the live path", mode)
	}
}
