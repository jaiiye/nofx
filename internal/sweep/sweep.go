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

// Grid is the parameter space: stop-loss and take-profit distances, as percent
// from the entry price.
type Grid struct {
	StopPcts []float64
	TakePcts []float64
}

// Cell is one grid point's outcome.
type Cell struct {
	StopPct float64
	TakePct float64
	Metrics backtest.Metrics
	PValue  float64
}

// Result is the full sweep.
type Result struct {
	Cells []Cell
	Best  Cell // highest net PnL
}

// Run evaluates every grid point over the same decisions.
func Run(records []*store.DecisionRecord, base backtest.Config, src backtest.KlineSource, g Grid) (*Result, error) {
	if len(g.StopPcts) == 0 || len(g.TakePcts) == 0 {
		return nil, fmt.Errorf("sweep: stop and take grids must be non-empty")
	}
	cached := cachingSource(src)
	res := &Result{}
	haveBest := false
	for _, sp := range g.StopPcts {
		for _, tp := range g.TakePcts {
			cfg := base
			cfg.OverrideStopPct = sp
			cfg.OverrideTakePct = tp
			r, err := backtest.Evaluate(records, cfg, cached)
			if err != nil {
				return nil, err
			}
			cell := Cell{StopPct: sp, TakePct: tp, Metrics: r.Metrics, PValue: r.Significance.PValue}
			res.Cells = append(res.Cells, cell)
			if !haveBest || cell.Metrics.NetPnL > res.Best.Metrics.NetPnL {
				res.Best = cell
				haveBest = true
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
