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

// runSweep runs a stop-loss / take-profit grid over a trader's recorded
// decisions and prints the net-PnL matrix, so risk/reward settings can be
// compared offline.
func runSweep(args []string) {
	fs := flag.NewFlagSet("sweep", flag.ExitOnError)
	traderID := fs.String("trader", "", "trader ID to sweep (required)")
	fromStr := fs.String("from", "", "start time, RFC3339 or YYYY-MM-DD (required)")
	toStr := fs.String("to", "", "end time (default: now)")
	stopPcts := fs.String("stop-pcts", "1,2,3,5", "comma-separated stop-loss percent grid")
	takePcts := fs.String("take-pcts", "2,4,6,10", "comma-separated take-profit percent grid")
	timeframe := fs.String("timeframe", "15m", "candle timeframe used to simulate fills")
	exchange := fs.String("exchange", "binance", "exchange fee schedule to assume")
	dbPath := fs.String("db", "", "override the SQLite database path")
	_ = fs.Parse(args)

	if *traderID == "" || *fromStr == "" {
		fmt.Fprintln(os.Stderr, "usage: nofx sweep --trader <id> --from <date> [--to <date>] "+
			"[--stop-pcts 1,2,3] [--take-pcts 2,4,6] [--timeframe 15m] [--exchange binance]")
		os.Exit(2)
	}

	stopGrid, err := parseFloatList(*stopPcts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --stop-pcts: %v\n", err)
		os.Exit(2)
	}
	takeGrid, err := parseFloatList(*takePcts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --take-pcts: %v\n", err)
		os.Exit(2)
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
	fmt.Printf("🔎 Sweeping %d×%d grid over %d decision cycles\n",
		len(stopGrid), len(takeGrid), len(records))

	res, err := sweep.Run(records, backtest.Config{
		TraderID:  *traderID,
		From:      from,
		To:        to,
		Timeframe: *timeframe,
		Fees:      connector.FeeModelFor(*exchange),
		Seed:      1,
	}, market.GetKlinesRange, sweep.Grid{StopPcts: stopGrid, TakePcts: takeGrid})
	if err != nil {
		fmt.Fprintf(os.Stderr, "sweep failed: %v\n", err)
		os.Exit(1)
	}

	printSweep(res, stopGrid, takeGrid)
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

// netPnLAt returns the net PnL for a grid point, or NaN if absent.
func netPnLAt(res *sweep.Result, stop, take float64) float64 {
	for _, c := range res.Cells {
		if c.StopPct == stop && c.TakePct == take {
			return c.Metrics.NetPnL
		}
	}
	return 0
}

func printSweep(res *sweep.Result, stopGrid, takeGrid []float64) {
	fmt.Println("\nnet PnL  (rows = stop %, cols = take %)")
	fmt.Printf("%-8s", "")
	for _, tp := range takeGrid {
		fmt.Printf("%10.0f%%", tp)
	}
	fmt.Println()
	for _, sp := range stopGrid {
		fmt.Printf("%-8.0f", sp)
		for _, tp := range takeGrid {
			fmt.Printf("%11.2f", netPnLAt(res, sp, tp))
		}
		fmt.Println()
	}

	b := res.Best
	fmt.Printf("\nbest: stop=%.1f%% take=%.1f%%  net=%.2f  win=%.1f%%  PF=%.2f  trades=%d  p=%.3f\n",
		b.StopPct, b.TakePct, b.Metrics.NetPnL, b.Metrics.WinRate, b.Metrics.ProfitFactor, b.Metrics.Trades, b.PValue)
	fmt.Println("note: parameters are scored on the same recorded decisions (selection is held fixed).")
}
