package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"time"

	"nofx/internal/backtest"
	"nofx/market"
	"nofx/trader/connector"
)

// runEvaluate replays a trader's recorded AI decisions offline and prints a
// performance report. It makes no model calls: it scores what the AI decided
// against the candles that followed.
func runEvaluate(args []string) {
	fs := flag.NewFlagSet("evaluate", flag.ExitOnError)
	traderID := fs.String("trader", "", "trader ID to evaluate (required)")
	fromStr := fs.String("from", "", "start time, RFC3339 or YYYY-MM-DD (required)")
	toStr := fs.String("to", "", "end time, RFC3339 or YYYY-MM-DD (default: now)")
	timeframe := fs.String("timeframe", "15m", "candle timeframe used to simulate fills")
	exchange := fs.String("exchange", "binance", "exchange fee schedule to assume")
	dbPath := fs.String("db", "", "override the SQLite database path")
	regime := fs.Bool("regime", false, "label trades with stored cascade/crowding state (attribution only)")
	_ = fs.Parse(args)

	if *traderID == "" || *fromStr == "" {
		fmt.Fprintln(os.Stderr, "usage: nofx evaluate --trader <id> --from <date> [--to <date>] "+
			"[--timeframe 15m] [--exchange binance] [--db path] [--regime]")
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
	fmt.Printf("📼 Replaying %d decision cycles for trader %s (%s → %s)\n",
		len(records), *traderID, from.Format("2006-01-02"), to.Format("2006-01-02"))

	cfg := backtest.Config{
		TraderID:  *traderID,
		From:      from,
		To:        to,
		Timeframe: *timeframe,
		Fees:      connector.FeeModelFor(*exchange),
		Seed:      1,
	}
	// Attribution only: the label is read from stored point-in-time rows and
	// never changes entry, exit or sizing.
	if *regime {
		cfg.RegimeSource = backtest.StoreRegimeSource(st)
	}

	res, err := backtest.Evaluate(records, cfg, market.GetKlinesRange)
	if err != nil {
		fmt.Fprintf(os.Stderr, "evaluation failed: %v\n", err)
		os.Exit(1)
	}

	printEvaluation(res)
}

func parseTimeArg(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized time %q (want RFC3339 or YYYY-MM-DD)", s)
}

func printEvaluation(res *backtest.Result) {
	m := res.Metrics
	fmt.Printf("\n=== Decision replay (%s candles, taker fee %.3f%%) ===\n",
		res.Config.Timeframe, res.Config.Fees.TakerRate()*100)
	fmt.Printf("opens=%d  trades=%d  skipped=%d\n", res.Opens, m.Trades, res.Skipped)
	fmt.Printf("win rate     : %.1f%% (%dW / %dL)\n", m.WinRate, m.Wins, m.Losses)
	fmt.Printf("net PnL      : %.2f  (gross %.2f, fees %.2f)\n", m.NetPnL, m.GrossPnL, m.TotalFee)
	fmt.Printf("avg net      : %.4f   profit factor: %.2f\n", m.AvgNetPnL, m.ProfitFactor)
	fmt.Printf("max drawdown : %.1f%%   Sharpe %.2f   Sortino %.2f\n", m.MaxDrawdownPct, m.Sharpe, m.Sortino)

	if len(res.BySymbol) > 0 {
		fmt.Println("\n-- by symbol --")
		for _, s := range res.BySymbol {
			fmt.Printf("  %-12s trades=%-4d win=%5.1f%%  net=%9.2f\n", s.Symbol, s.Trades, s.WinRate, s.NetPnL)
		}
	}
	if len(res.ByReason) > 0 {
		fmt.Println("\n-- by exit reason --")
		for _, r := range res.ByReason {
			fmt.Printf("  %-12s trades=%-4d win=%d  net=%9.2f\n", r.Reason, r.Trades, r.Wins, r.NetPnL)
		}
	}
	if len(res.ByConfidence) > 0 {
		fmt.Println("\n-- by AI confidence --")
		for _, c := range res.ByConfidence {
			fmt.Printf("  %-8s trades=%-4d win=%5.1f%%  net=%9.2f\n", c.Bucket, c.Trades, c.WinRate, c.NetPnL)
		}
	}
	if len(res.ByRegime) > 0 {
		fmt.Println("\n-- by entry regime --")
		for _, r := range res.ByRegime {
			fmt.Printf("  %-14s trades=%-4d win=%5.1f%%  net=%9.2f\n", r.Regime, r.Trades, r.WinRate, r.NetPnL)
		}
	}

	sig := res.Significance
	fmt.Printf("\n-- significance vs random entries --\n")
	fmt.Printf("  observed mean net=%.4f  baseline mean=%.4f  p=%.3f (%d samples)\n",
		sig.MeanNetPnL, sig.BaselineMean, sig.PValue, sig.Samples)
	if p := sig.PValue; !math.IsNaN(p) && p > 0.05 {
		fmt.Println("  ⚠ p > 0.05: cannot distinguish this from luck at 95% confidence")
	}

	fmt.Println("\n-- assumptions --")
	for _, n := range res.Notes {
		fmt.Printf("  • %s\n", n)
	}
}
