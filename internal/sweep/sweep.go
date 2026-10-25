// Package sweep runs a grid of backtest configurations over the same recorded
// decisions and reports the outcomes, so a strategy's risk/reward parameters
// can be compared at a glance — the offline analogue of a parameter sweep.
package sweep

import (
	"fmt"
	"time"

	"nofx/internal/backtest"
	"nofx/market"
	"nofx/store"
)

// Grid is the parameter space. Stop distances are always swept; the target
// applies to bracket mode, the arm/giveback pair to giveback mode.
type Grid struct {
	// Mode is the backtest exit mode: "bracket" (default) or "giveback".
	Mode         string
	StopPcts     []float64
	TakePcts     []float64 // bracket mode
	ArmPcts      []float64 // giveback mode: favorable move that arms the trail
	GivebackPcts []float64 // giveback mode: share of the peak given back
}

// Cell is one grid point's outcome.
type Cell struct {
	Mode        string
	StopPct     float64
	TakePct     float64
	ArmPct      float64
	GivebackPct float64
	Metrics     backtest.Metrics
	PValue      float64
}

// Result is the full sweep.
type Result struct {
	Mode  string
	Cells []Cell
	Best  Cell // highest net PnL
}

// Run evaluates every grid point over the same decisions.
func Run(records []*store.DecisionRecord, base backtest.Config, src backtest.KlineSource, g Grid) (*Result, error) {
	mode := g.Mode
	if mode == "" {
		mode = backtest.ExitBracket
	}
	if len(g.StopPcts) == 0 {
		return nil, fmt.Errorf("sweep: stop grid must be non-empty")
	}
	switch mode {
	case backtest.ExitBracket:
		if len(g.TakePcts) == 0 {
			return nil, fmt.Errorf("sweep: take grid must be non-empty in bracket mode")
		}
	case backtest.ExitGiveback:
		if len(g.ArmPcts) == 0 || len(g.GivebackPcts) == 0 {
			return nil, fmt.Errorf("sweep: arm and giveback grids must be non-empty in giveback mode")
		}
	default:
		return nil, fmt.Errorf("sweep: unknown exit mode %q", mode)
	}

	cached := cachingSource(src)
	res := &Result{Mode: mode}
	haveBest := false
	consider := func(cell Cell) {
		res.Cells = append(res.Cells, cell)
		if !haveBest || cell.Metrics.NetPnL > res.Best.Metrics.NetPnL {
			res.Best = cell
			haveBest = true
		}
	}

	for _, stopPct := range g.StopPcts {
		switch mode {
		case backtest.ExitBracket:
			for _, takePct := range g.TakePcts {
				cfg := base
				cfg.ExitMode = backtest.ExitBracket
				cfg.OverrideStopPct = stopPct
				cfg.OverrideTakePct = takePct
				r, err := backtest.Evaluate(records, cfg, cached)
				if err != nil {
					return nil, err
				}
				consider(Cell{Mode: mode, StopPct: stopPct, TakePct: takePct, Metrics: r.Metrics, PValue: r.Significance.PValue})
			}
		case backtest.ExitGiveback:
			for _, armPct := range g.ArmPcts {
				for _, givebackPct := range g.GivebackPcts {
					cfg := base
					cfg.ExitMode = backtest.ExitGiveback
					cfg.OverrideStopPct = stopPct
					cfg.OverrideTakePct = 0 // no fixed target in giveback mode
					cfg.GivebackArmPct = armPct
					cfg.GivebackPct = givebackPct
					r, err := backtest.Evaluate(records, cfg, cached)
					if err != nil {
						return nil, err
					}
					consider(Cell{
						Mode: mode, StopPct: stopPct, ArmPct: armPct, GivebackPct: givebackPct,
						Metrics: r.Metrics, PValue: r.Significance.PValue,
					})
				}
			}
		}
	}
	return res, nil
}

// cachingSource memoises kline fetches so a grid run hits the network once per
// symbol/range instead of once per cell.
func cachingSource(src backtest.KlineSource) backtest.KlineSource {
	type key struct {
		sym, tf    string
		start, end int64
	}
	cache := map[key][]market.Kline{}
	return func(symbol, tf string, start, end time.Time) ([]market.Kline, error) {
		k := key{symbol, tf, start.UnixMilli(), end.UnixMilli()}
		if v, ok := cache[k]; ok {
			return v, nil
		}
		v, err := src(symbol, tf, start, end)
		if err != nil {
			return nil, err
		}
		cache[k] = v
		return v, nil
	}
}
