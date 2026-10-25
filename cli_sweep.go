// File: cli_sweep.go
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"nofx/internal/backtest"
	"nofx/internal/sweep"
	"nofx/market"
	"nofx/trader/connector"
)

// runSweep runs a parameter grid over a trader's recorded decisions and prints
// the net-PnL matrix, so risk/reward and exit settings can be compared offline.
func runSweep(args []string) {
	fs := flag.NewFlagSet("sweep", flag.ExitOnError)
	traderID := fs.String("trader", "", "trader ID to sweep (required)")
	fromStr := fs.String("from", "", "start time, RFC3339 or YYYY-MM-DD (required)")
	toStr := fs.String("to", "", "end time (default: now)")
	exitMode := fs.String("exit", "bracket", "exit simulation: bracket | giveback")
	stopPcts := fs.String("stop-pcts", "1,2,3,5", "comma-separated stop-loss percent grid")
	takePcts := fs.String("take-pcts", "2,4,6,10", "comma-separated take-profit percent grid (bracket)")
	armPcts := fs.String("arm-pcts", "1,2,3", "comma-separated giveback arm percent grid (giveback)")
	givebackPcts := fs.String("giveback-pcts", "20,30,50", "comma-separated giveback percent grid (giveback)")
	sizing := fs.String("sizing", "recorded", "position sizing: recorded | decision | risk")
	riskPct := fs.Float64("risk-pct", 0.5, "percent of equity risked per trade (risk sizing)")
	equity := fs.Float64("equity", 0, "fallback account equity when no snapshots exist (risk sizing)")
	timeframe := fs.String("timeframe", "15m", "candle timeframe used to simulate fills")
	exchange := fs.String("exchange", "binance", "exchange fee schedule to assume")
	dbPath := fs.String("db", "", "override the SQLite database path")
	_ = fs.Parse(args)

	if *traderID == "" || *fromStr == "" {
		fmt.Fprintln(os.Stderr, "usage: nofx sweep --trader <id> --from <date> [--to <date>] "+
			"[--exit bracket|giveback] [--stop-pcts 1,2,3] [--take-pcts 2,4,6] "+
			"[--arm-pcts 1,2,3] [--giveback-pcts 20,30,50] "+
			"[--sizing recorded|decision|risk] [--risk-pct 0.5] [--equity 0] "+
			"[--timeframe 15m] [--exchange binance]")
		os.Exit(2)
	}

	mode := strings.ToLower(strings.TrimSpace(*exitMode))
	grid := sweep.Grid{Mode: mode}
	var err error
	if grid.StopPcts, err = parseFloatList(*stopPcts); err != nil {
		fmt.Fprintf(os.Stderr, "invalid --stop-pcts: %v\n", err)
		os.Exit(2)
	}
	switch mode {
	case backtest.ExitGiveback:
		if grid.ArmPcts, err = parseFloatList(*armPcts); err != nil {
			fmt.Fprintf(os.Stderr, "invalid --arm-pcts: %v\n", err)
			os.Exit(2)
		}
		if grid.GivebackPcts, err = parseFloatList(*givebackPcts); err != nil {
			fmt.Fprintf(os.Stderr, "invalid --giveback-pcts: %v\n", err)
			os.Exit(2)
		}
	default:
		if grid.TakePcts, err = parseFloatList(*takePcts); err != nil {
			fmt.Fprintf(os.Stderr, "invalid --take-pcts: %v\n", err)
			os.Exit(2)
		}
	}

	from, err := parseTimeArg(*fromStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --from: %v\n", err)
		os.Exit(2)
	}
	to := time.Now().UTC()
	if *toStr != "" {
		if to, err = parseTimeArg(*toStr); err != nil {
			fmt.Fprintf(os.Stderr, "invalid --to: %v\n", err)
			os.Exit(2)
		}
	}

	st, err := openStoreBare(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open store: %v\n", err)
		os.Exit(1)
	}
	defer st.Close()

	records, err := backtest.LoadRecords(st, *traderID, from, to)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load decisions: %v\n", err)
		os.Exit(1)
	}

	base := backtest.Config{
		TraderID:   *traderID,
		From:       from,
		To:         to,
		Timeframe:  *timeframe,
		Fees:       connector.FeeModelFor(*exchange),
		Seed:       1,
		SizingMode: strings.ToLower(strings.TrimSpace(*sizing)),
		RiskPct:    *riskPct,
		Equity:     *equity,
	}
	if base.SizingMode == backtest.SizingRisk {
		base.EquitySource = backtest.StoreEquitySource(st, *traderID, from, to)
	}

	switch mode {
	case backtest.ExitGiveback:
		fmt.Printf("🔎 Sweeping %d stops × %d arms × %d givebacks over %d decision cycles (sizing=%s)\n",
			len(grid.StopPcts), len(grid.ArmPcts), len(grid.GivebackPcts), len(records), base.SizingMode)
	default:
		fmt.Printf("🔎 Sweeping %d stops × %d takes over %d decision cycles (sizing=%s)\n",
			len(grid.StopPcts), len(grid.TakePcts), len(records), base.SizingMode)
	}

	// Auto routes HIP-3 (xyz) symbols to Hyperliquid; Binance rejects them.
	res, err := sweep.Run(records, base, market.GetKlinesRangeAuto, grid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sweep failed: %v\n", err)
		os.Exit(1)
	}

	printSweep(res, grid)
}

func parseFloatList(s string) ([]float64, error) {
	parts := strings.Split(s, ",")
	out := make([]float64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not a number", p)
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty list")
	}
	return out, nil
}

// netPnLAt returns the net PnL for a grid point, or 0 if absent. Fields that do
// not apply to the sweep mode are zero.
func netPnLAt(res *sweep.Result, stop, take, arm, giveback float64) float64 {
	for _, c := range res.Cells {
		if c.StopPct == stop && c.TakePct == take && c.ArmPct == arm && c.GivebackPct == giveback {
			return c.Metrics.NetPnL
		}
	}
	return 0
}

func printSweep(res *sweep.Result, g sweep.Grid) {
	if res.Mode == backtest.ExitGiveback {
		fmt.Println("\nnet PnL  (rows = stop %, cols = giveback %)")
		for _, arm := range g.ArmPcts {
			fmt.Printf("\n-- arm ≥ %.1f%% --\n", arm)
			fmt.Printf("%-8s", "")
			for _, gb := range g.GivebackPcts {
				fmt.Printf("%10.0f%%", gb)
			}
			fmt.Println()
			for _, sp := range g.StopPcts {
				fmt.Printf("%-8.1f", sp)
				for _, gb := range g.GivebackPcts {
					fmt.Printf("%11.2f", netPnLAt(res, sp, 0, arm, gb))
				}
				fmt.Println()
			}
		}
	} else {
		fmt.Println("\nnet PnL  (rows = stop %, cols = take %)")
		fmt.Printf("%-8s", "")
		for _, tp := range g.TakePcts {
			fmt.Printf("%10.1f%%", tp)
		}
		fmt.Println()
		for _, sp := range g.StopPcts {
			fmt.Printf("%-8.1f", sp)
			for _, tp := range g.TakePcts {
				fmt.Printf("%11.2f", netPnLAt(res, sp, tp, 0, 0))
			}
			fmt.Println()
		}
	}

	b := res.Best
	if res.Mode == backtest.ExitGiveback {
		fmt.Printf("\nbest: stop=%.1f%% arm=%.1f%% giveback=%.0f%%  net=%.2f  win=%.1f%%  PF=%.2f  trades=%d  p=%.3f\n",
			b.StopPct, b.ArmPct, b.GivebackPct, b.Metrics.NetPnL, b.Metrics.WinRate, b.Metrics.ProfitFactor, b.Metrics.Trades, b.PValue)
	} else {
		fmt.Printf("\nbest: stop=%.1f%% take=%.1f%%  net=%.2f  win=%.1f%%  PF=%.2f  trades=%d  p=%.3f\n",
			b.StopPct, b.TakePct, b.Metrics.NetPnL, b.Metrics.WinRate, b.Metrics.ProfitFactor, b.Metrics.Trades, b.PValue)
	}
	fmt.Println("note: parameters are scored on the same recorded decisions (selection is held fixed).")
}
