package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"nofx/internal/report"
	"nofx/market"
)

// runTradeReport prints the trade-quality report: gross vs net, split by holding
// time and by who closed the position, plus a random-entry baseline matched on
// holding duration and cost.
//
// The distinctions matter: a net loss with a positive gross PnL means costs ate
// the edge; a net loss with a negative gross PnL means there was no edge. Without
// both numbers the two are indistinguishable.
func runTradeReport(args []string) {
	fs := flag.NewFlagSet("trade-report", flag.ExitOnError)
	traderID := fs.String("trader", "", "trader ID to report on (required)")
	days := fs.Int("days", 30, "window in days, ending now")
	limit := fs.Int("limit", 500, "how many closed positions to load before filtering")
	timeframe := fs.String("timeframe", "1h", "candle timeframe for the random baseline (1h covers ~40d)")
	runs := fs.Int("runs", 200, "random baseline runs")
	seed := fs.Int64("seed", 1, "random baseline seed (fixed by default so a report is reproducible)")
	dbPath := fs.String("db", "", "override the SQLite database path")
	_ = fs.Parse(args)

	if *traderID == "" {
		fmt.Fprintln(os.Stderr, "usage: nofx trade-report --trader <id> [--days 30] [--timeframe 1h] [--runs 200] [--db path]")
		os.Exit(2)
	}

	st, err := openStoreBare(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open store: %v\n", err)
		os.Exit(1)
	}
	defer st.Close()

	now := time.Now().UTC()
	since := now.AddDate(0, 0, -*days)

	closed, err := st.Position().GetClosedPositions(*traderID, *limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load closed positions: %v\n", err)
		os.Exit(1)
	}

	// Who closed what: a successful close_* decision near the exit time is the
	// only attribution available today (the position row itself records "sync"
	// for both AI and venue exits).
	closeTimes := map[string][]int64{}
	records, err := st.Decision().GetRecordsByRange(*traderID, since, now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  decision records unavailable, AI/protective split will be unknown: %v\n", err)
	}
	for _, rec := range records {
		for _, d := range rec.Decisions {
			if !d.Success || !strings.HasPrefix(d.Action, "close_") {
				continue
			}
			ts := d.Timestamp
			if ts.IsZero() {
				ts = rec.Timestamp
			}
			key := strings.ToUpper(d.Symbol)
			closeTimes[key] = append(closeTimes[key], ts.UnixMilli())
		}
	}

	trades := make([]report.Trade, 0, len(closed))
	for _, pos := range closed {
		if pos.ExitTime <= 0 || pos.ExitTime < since.UnixMilli() {
			continue
		}
		qty := pos.EntryQuantity
		if qty == 0 {
			qty = pos.Quantity
		}
		dir := 1.0
		if strings.EqualFold(pos.Side, "SHORT") {
			dir = -1
		}
		trade := report.Trade{
			Symbol:    pos.Symbol,
			Side:      pos.Side,
			EntryMs:   pos.EntryTime,
			ExitMs:    pos.ExitTime,
			Qty:       qty,
			EntryPx:   pos.EntryPrice,
			ExitPx:    pos.ExitPrice,
			GrossPnL:  (pos.ExitPrice - pos.EntryPrice) * qty * dir,
			Fees:      pos.Fee,
			Funding:   pos.FundingFee,
			CloseKind: pos.CloseReason,
		}
		trade.AIInitiated = hasCloseNear(closeTimes[strings.ToUpper(pos.Symbol)], pos.ExitTime)
		trades = append(trades, trade)
	}

	if len(trades) == 0 {
		fmt.Printf("No closed trades for trader %s in the last %d days.\n", *traderID, *days)
		return
	}

	rep := report.Build(trades)

	fmt.Printf("📊 Trade report — trader %s — last %d days (%s → %s)\n\n",
		*traderID, *days, since.Format("2006-01-02"), now.Format("2006-01-02"))
	printSummary("Overall", rep.Overall)
	fmt.Printf("\nCloses: AI-initiated %.0f%% (%d) | protective/venue %.0f%% (%d)\n",
		rep.Overall.AIShare, rep.AI.Trades, 100-rep.Overall.AIShare, rep.Protective.Trades)
	if rep.AI.Trades > 0 {
		printCompact("  AI closes", rep.AI)
	}
	if rep.Protective.Trades > 0 {
		printCompact("  protective", rep.Protective)
	}

	fmt.Printf("\n%-10s %6s %6s %12s %10s %12s %8s %9s\n",
		"Hold", "trades", "win%", "gross", "fees", "net", "netPF", "avgHold")
	for _, b := range rep.Buckets {
		printBucket(b)
	}

	printBaseline(trades, *timeframe, *runs, *seed, since, now)
}

func printSummary(label string, s report.Summary) {
	fmt.Printf("%s: %d trades | win %.0f%% | gross %+.2f | fees %.2f | funding %+.2f | net %+.2f\n",
		label, s.Trades, s.WinRate, s.Gross, s.Fees, s.Funding, s.Net)
	fmt.Printf("  gross PF %s | net PF %s | fees %.3f%% of notional | avg hold %s\n",
		formatPF(s.GrossPF), formatPF(s.NetPF), s.FeesPctOfNotional, s.AvgHold.Round(time.Minute))
}

func printCompact(label string, s report.Summary) {
	fmt.Printf("%s: %d trades | win %.0f%% | gross %+.2f | net %+.2f | net PF %s\n",
		label, s.Trades, s.WinRate, s.Gross, s.Net, formatPF(s.NetPF))
}

func printBucket(b report.Bucket) {
	s := b.Summary
	fmt.Printf("%-10s %6d %6.0f %+12.2f %+10.2f %+12.2f %8s %9s\n",
		b.Label, s.Trades, s.WinRate, s.Gross, s.Fees, s.Net, formatPF(s.NetPF), s.AvgHold.Round(time.Minute))
}

func formatPF(pf float64) string {
	if math.IsInf(pf, 1) {
		return "∞"
	}
	return fmt.Sprintf("%.2f", pf)
}

// hasCloseNear reports whether a successful close decision exists within 15
// minutes of the exit. The sync close can land a few seconds after the fill.
func hasCloseNear(times []int64, exitMs int64) bool {
	const windowMs = int64(15 * time.Minute / time.Millisecond)
	for _, ts := range times {
		if ts >= exitMs-windowMs && ts <= exitMs+windowMs {
			return true
		}
	}
	return false
}

// printBaseline builds a price path per traded symbol and compares the trades
// with random entries on those paths.
func printBaseline(trades []report.Trade, timeframe string, runs int, seed int64, since, now time.Time) {
	dur, err := market.TFDuration(timeframe)
	if err != nil {
		fmt.Printf("\n⚠️  baseline skipped: %v\n", err)
		return
	}
	// One day of padding so the earliest trades still have a usable window.
	from := since.Add(-24 * time.Hour)

	symbols := map[string]bool{}
	for _, t := range trades {
		symbols[strings.ToUpper(t.Symbol)] = true
	}
	series := map[string]report.Series{}
	missing := 0
	for symbol := range symbols {
		klines, err := market.GetKlinesRangeAuto(symbol, timeframe, from, now)
		if err != nil || len(klines) < 3 {
			missing++
			continue
		}
		closes := make([]float64, 0, len(klines))
		for _, k := range klines {
			closes = append(closes, k.Close)
		}
		series[symbol] = report.Series{BarMs: dur.Milliseconds(), Closes: closes}
	}

	fmt.Printf("\nBaseline (random entries, same holding time and fees, %s bars, %d runs):\n", timeframe, runs)
	if len(series) == 0 {
		fmt.Printf("  no price history available for the traded symbols — baseline not computed\n")
		return
	}
	base := report.RandomBaseline(trades, series, runs, seed)
	fmt.Printf("  covered %d/%d trades | strategy net/trade %+.3f%% | random median %+.3f%% (p25 %+.3f%%, p75 %+.3f%%) | percentile %.0f\n",
		base.Covered, base.Total, base.StrategyNetPct, base.RandomMedian, base.RandomP25, base.RandomP75, base.Percentile)
	fmt.Printf("  fee assumed: %.3f%% per leg (measured from the trades)\n", base.FeePerLeg*100)
	if missing > 0 {
		fmt.Printf("  %d symbol(s) had no usable history and were excluded from BOTH sides\n", missing)
	}
	if base.Percentile >= 75 {
		fmt.Printf("  → the entries beat %.0f%% of matched random entries (timing added value)\n", base.Percentile)
	} else if base.Percentile <= 25 {
		fmt.Printf("  → the entries beat only %.0f%% of matched random entries (timing added nothing)\n", base.Percentile)
	} else {
		fmt.Printf("  → indistinguishable from random entries with the same holding time\n")
	}
}
