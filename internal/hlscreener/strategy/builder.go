// Package strategy builds AI-decision prompts for the upstream nofx
// strategy engine. The point of this layer is: nofx prompts are
// string-based and accept a free-form "market context" blob. We inject
// three datasets the trader actually needs:
//
//   1. candidate_active  — the screener's current pick list (top N coins)
//   2. recent CVD panel   — 24h delta and last 12 5m bars per active coin
//   3. liquidation summary — nearest dense liq bins (top 3 above/below)
//
// The output is a single `MarketContext` struct that nofx's prompt
// builder can serialize into whatever format the upstream template
// expects (we keep it JSON for now, the prompt layer can choose).
//
// This is the actual bridge between our data plane and the nofx trader.
package strategy

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"nofx/internal/hlscreener/store"
)

// MarketContext is what the AI prompt sees. Stable JSON shape.
type MarketContext struct {
	GeneratedAt int64              `json:"generated_at"`
	Universe    []CandidateSummary `json:"universe"`
}

// CandidateSummary is one row of context for a coin.
type CandidateSummary struct {
	Coin          string             `json:"coin"`
	Score         float64            `json:"score"`
	InPool        bool               `json:"in_pool"`
	Reasons       map[string]any     `json:"reasons"`
	CVD24h        float64            `json:"cvd_24h"`
	CVDLast12Bars []CVDBarSummary    `json:"cvd_last_12_bars,omitempty"`
	NearestLiq    []LiqBinSummary    `json:"nearest_liq,omitempty"`
}

// CVDBarSummary is a short-form (t, delta) for prompt injection.
type CVDBarSummary struct {
	T     int64   `json:"t"`
	Delta float64 `json:"delta"`
	Cum   float64 `json:"cum"`
}

// LiqBinSummary describes one nearby liq cluster.
type LiqBinSummary struct {
	Side       string  `json:"side"`        // "above" or "below"
	DistancePct float64 `json:"distance_pct"`
	NotionalUSD float64 `json:"notional_usd"`
	BinStart   float64 `json:"bin_start"`
	BinEnd     float64 `json:"bin_end"`
}

// Builder assembles MarketContext.
type Builder struct {
	store *store.Store
	// Optional: if set, restricts universe to active pool only.
	// If false, builds context for every coin in DB.
	activeOnly bool
	// Number of last CVD bars to include.
	cvdBars int
}

func NewBuilder(st *store.Store) *Builder {
	return &Builder{store: st, activeOnly: true, cvdBars: 12}
}

func (b *Builder) WithActiveOnly(v bool) *Builder { b.activeOnly = v; return b }
func (b *Builder) WithCVDBars(n int) *Builder     { b.cvdBars = n; return b }

// Build reads the DB and produces a MarketContext snapshot.
func (b *Builder) Build(ctx context.Context) (*MarketContext, error) {
	mc := &MarketContext{GeneratedAt: time.Now().UnixMilli()}

	var coins []store.Candidate
	if b.activeOnly {
		active, err := b.store.GetActivePool(ctx)
		if err != nil {
			return nil, fmt.Errorf("active pool: %w", err)
		}
		coins = active
	}
	// (non-active mode would query ohlc_1m distinct coins; left as TODO)

	for _, c := range coins {
		cs := CandidateSummary{
			Coin:    c.Coin,
			Score:   c.Score,
			InPool:  c.InPool,
			Reasons: c.Reasons,
		}
		// 24h CVD delta
		now := time.Now().UnixMilli()
		panel, err := b.store.CVDPanel(ctx, c.Coin, now-24*3600*1000, now)
		if err == nil && len(panel) > 0 {
			cs.CVD24h = panel[len(panel)-1].C - panel[0].O
			// last N bars
			n := b.cvdBars
			if n > len(panel) {
				n = len(panel)
			}
			tail := panel[len(panel)-n:]
			cs.CVDLast12Bars = make([]CVDBarSummary, len(tail))
			for i, p := range tail {
				cs.CVDLast12Bars[i] = CVDBarSummary{T: p.T, Delta: p.Delta, Cum: p.C}
			}
		}

		// nearest liq bins
		levels, err := b.store.GetLiqHeatmap(ctx, c.Coin)
		if err == nil && len(levels) > 0 {
			cs.NearestLiq = b.nearestBins(levels, 3)
		}

		mc.Universe = append(mc.Universe, cs)
	}
	return mc, nil
}

// nearestBins picks the top N densest liq bins above and below the
// current mark. "Mark" is estimated as the WEIGHTED-CENTROID of all
// bins (notional-weighted average of bin mids) — this is more stable
// than "max notional bin" when the densest bin happens to be near a
// bin edge.
//
// Caller should pass the actual HL mid in a future iteration; this is
// a fallback for prompt-level context.
// NearestBinsForTest is the public form of nearestBins, used by
// localstack's --mode=context output. Kept separate to avoid
// changing the lowercase private signature used by Build() itself.
func (b *Builder) NearestBinsForTest(levels []store.LiqLevel, n int) []LiqBinSummary {
	return b.nearestBins(levels, n)
}

func (b *Builder) nearestBins(levels []store.LiqLevel, n int) []LiqBinSummary {
	if len(levels) == 0 {
		return nil
	}
	// Weighted-centroid mark
	var totalNotional, weightedSum float64
	for _, l := range levels {
		mid := (l.BinStart + l.BinEnd) / 2
		totalNotional += l.NotionalUSD
		weightedSum += mid * l.NotionalUSD
	}
	if totalNotional == 0 {
		return nil
	}
	mark := weightedSum / totalNotional
	if mark == 0 {
		return nil
	}
	// Pick top N above and below by notional
	type kv struct {
		level   store.LiqLevel
		distPct float64
	}
	above := []kv{}
	below := []kv{}
	for _, l := range levels {
		mid := (l.BinStart + l.BinEnd) / 2
		if mid > mark {
			above = append(above, kv{level: l, distPct: (mid - mark) / mark * 100})
		} else if mid < mark {
			below = append(below, kv{level: l, distPct: (mark - mid) / mark * 100})
		}
	}
	// sort by notional desc
	sortByNotional := func(s []kv) {
		for i := 1; i < len(s); i++ {
			for j := i; j > 0 && s[j-1].level.NotionalUSD < s[j].level.NotionalUSD; j-- {
				s[j-1], s[j] = s[j], s[j-1]
			}
		}
	}
	sortByNotional(above)
	sortByNotional(below)

	out := []LiqBinSummary{}
	for i := 0; i < n && i < len(above); i++ {
		k := above[i]
		out = append(out, LiqBinSummary{
			Side:        "above",
			DistancePct: k.distPct,
			NotionalUSD: k.level.NotionalUSD,
			BinStart:    k.level.BinStart,
			BinEnd:      k.level.BinEnd,
		})
	}
	for i := 0; i < n && i < len(below); i++ {
		k := below[i]
		out = append(out, LiqBinSummary{
			Side:        "below",
			DistancePct: k.distPct,
			NotionalUSD: k.level.NotionalUSD,
			BinStart:    k.level.BinStart,
			BinEnd:      k.level.BinEnd,
		})
	}
	return out
}

// ToJSON serializes for transport.
func (mc *MarketContext) ToJSON() ([]byte, error) {
	return json.MarshalIndent(mc, "", "  ")
}

// AsPrompt renders a human-readable form for the AI prompt. Kept simple;
// the upstream nofx prompt template likely does its own formatting.
func (mc *MarketContext) AsPrompt() string {
	out := fmt.Sprintf("Market context generated at %d\n\n", mc.GeneratedAt)
	out += "=== Active candidates ===\n"
	for _, c := range mc.Universe {
		mark := " "
		if c.InPool {
			mark = "*"
		}
		out += fmt.Sprintf("%s %s  score=%.1f  cvd24h=%.0f\n",
			mark, c.Coin, c.Score, c.CVD24h)
		if len(c.NearestLiq) > 0 {
			out += "  nearest liq:\n"
			for _, lb := range c.NearestLiq {
				out += fmt.Sprintf("    %s  %+.2f%%  $%.0f  bin [%.2f, %.2f]\n",
					lb.Side, lb.DistancePct, lb.NotionalUSD, lb.BinStart, lb.BinEnd)
			}
		}
	}
	return out
}
