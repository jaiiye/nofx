package kernel

import (
	"strings"
	"testing"

	"nofx/market"
	"nofx/store"
)

// promptProfileBlockBudgetBytes mirrors store/strategy.go's
// promptPositionProfileChars: the estimator charges that many characters per coin
// for this block, so the rendered block has to stay inside it. The two numbers
// move together.
const promptProfileBlockBudgetBytes = 1100

// structureProfileFixture builds a realistic profile from a real bucket
// distribution, so the rendered block is measured rather than imagined: heavy
// cost above and below the price, liquidation fuel concentrated on both sides.
func structureProfileFixture(t *testing.T) *market.PositionProfile {
	t.Helper()

	bins, coverage := market.BinPositions(nil, 63000, 15, 40)
	for i := range bins {
		// Concentrated on purpose: long cost piles up above the price, short cost
		// below it, so both sides carry a bulk that clears the significance floor.
		bins[i].LongCost = float64((i+1)*(i+1)) * 1000
		bins[i].ShortCost = float64((len(bins)-i)*(len(bins)-i)) * 1000
		if i < 20 {
			bins[i].LongLiq = float64(20-i) * 15000
		} else {
			bins[i].ShortLiq = float64(i-20) * 11000
		}
	}
	coverage.AddressesSeen = 412
	coverage.PositionsSeen = 980
	coverage.PositionsPlaced = 959
	coverage.PositionsOutside = 21
	coverage.WeightSource = market.PositionWeightSourceNotional

	profile := market.BuildPositionProfile(bins, coverage, 63000, 15)
	if profile == nil {
		t.Fatal("fixture must produce a profile")
	}
	return profile
}

func countProfileTableRows(block string) int {
	rows := 0
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(line, "| ") && !strings.Contains(line, "Zone") && !strings.Contains(line, "---") {
			rows++
		}
	}
	return rows
}

func TestPositionProfilePromptBlock(t *testing.T) {
	cfg := store.GetDefaultStrategyConfig("en")
	engine := NewStrategyEngine(&cfg)
	ctx := &Context{PositionProfileMap: map[string]*market.PositionProfile{"BTC": structureProfileFixture(t)}}

	block := engine.formatPositionProfile(ctx, "BTC")
	for _, want := range []string{"HOLDER STRUCTURE", "Cost POC", "Cost walls", "In profit", "Liquidation fuel", "| Zone |"} {
		if !strings.Contains(block, want) {
			t.Fatalf("block is missing %q:\n%s", want, block)
		}
	}
	if rows := countProfileTableRows(block); rows != positionStructureMaxRows {
		t.Fatalf("table rows = %d, want the cap of %d:\n%s", rows, positionStructureMaxRows, block)
	}
	if len(block) > promptProfileBlockBudgetBytes {
		t.Fatalf("block is %d bytes while the estimator charges %d per coin (store/strategy.go promptPositionProfileChars): raise both together",
			len(block), promptProfileBlockBudgetBytes)
	}
}

// The block is purely additive: an uncollected market, a disabled feature and a
// stale snapshot all look like "no profile", and that must leave the prompt
// byte-identical to one that never had structure at all.
func TestPositionProfilePromptIsPurelyAdditive(t *testing.T) {
	cfg := store.GetDefaultStrategyConfig("en")
	engine := NewStrategyEngine(&cfg)
	coins := store.DefaultHyperRankLimit
	bars := cfg.Indicators.Klines.PrimaryCount

	without := engine.BuildUserPrompt(measurementContextWithPositions(&cfg, coins, bars, 1))
	if strings.Contains(without, "HOLDER STRUCTURE") {
		t.Fatal("no profile was attached, so no structure block may be rendered")
	}

	ctx := measurementContextWithPositions(&cfg, coins, bars, 1)
	if len(ctx.CandidateCoins) == 0 {
		t.Fatal("fixture must contain candidates")
	}
	symbol := ctx.CandidateCoins[0].Symbol
	ctx.PositionProfileMap = map[string]*market.PositionProfile{symbol: structureProfileFixture(t)}

	block := engine.formatPositionProfile(ctx, symbol)
	if block == "" {
		t.Fatal("an attached profile must render a block")
	}
	with := engine.BuildUserPrompt(ctx)
	if !strings.Contains(with, "HOLDER STRUCTURE") {
		t.Fatal("the attached profile did not reach the prompt")
	}
	if stripped := strings.Replace(with, block, "", 1); stripped != without {
		t.Fatal("the structure block must be the only difference in the user prompt")
	}
}

func TestPositionStructurePlaybookRuleFollowsTheFlag(t *testing.T) {
	offCfg := store.GetDefaultStrategyConfig("en")
	off := NewStrategyEngine(&offCfg).BuildSystemPrompt(1000, "balanced")
	if strings.Contains(off, "Holder structure:") {
		t.Fatal("the structure rule must stay out of the system prompt while the feature is off")
	}

	cfg := store.GetDefaultStrategyConfig("en")
	enabled := true
	cfg.Indicators.EnablePositionStructure = &enabled
	on := NewStrategyEngine(&cfg).BuildSystemPrompt(1000, "balanced")
	if !strings.Contains(on, "Holder structure:") {
		t.Fatal("the structure rule must appear once the feature is on")
	}
}
