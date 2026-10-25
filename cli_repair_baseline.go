package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"nofx/store"
)

// runRepairBaseline recomputes a trader's cash-flow neutral PnL baseline from
// first principles and, only with --apply, writes it back.
//
// Why this exists: the Hyperliquid connector used to count an xyz position's
// margin twice in the account equity, so the balance stepped up by the margin
// when a position opened and back down when it closed. reconcilePnLBaseline read
// that pair as an external deposit and withdrawal and folded both into
// initial_balance, leaving it ~0.87 USDT below the value the realized ledger
// implies. Every reported PnL% and every drawdown is measured against that
// number, so it is worth repairing once the connector is fixed.
//
// The rule is the one BaselineFromWallet already encodes:
//
//	baseline = wallet - all-time net realized PnL
//
// Default is a dry run: writing the wrong baseline silently rewrites history.
func runRepairBaseline(args []string) {
	fs := flag.NewFlagSet("repair-baseline", flag.ExitOnError)
	traderID := fs.String("trader", "", "trader ID to repair, or \"all\"")
	apply := fs.Bool("apply", false, "write the repaired baseline (default: dry run)")
	dbPath := fs.String("db", "", "override the SQLite database path")
	_ = fs.Parse(args)

	if strings.TrimSpace(*traderID) == "" {
		fmt.Fprintln(os.Stderr, "usage: nofx repair-baseline --trader <id|all> [--db path] [--apply]")
		os.Exit(2)
	}

	st, err := openStoreBare(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open store: %v\n", err)
		os.Exit(1)
	}

	traders, err := resolveTradersForRepair(st, *traderID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve traders: %v\n", err)
		os.Exit(1)
	}
	if len(traders) == 0 {
		fmt.Fprintf(os.Stderr, "no traders matched %q\n", *traderID)
		os.Exit(1)
	}

	failed := false
	changed := 0
	for _, tr := range traders {
		report, written, err := repairBaseline(st, tr, *apply)
		if err != nil {
			failed = true
			fmt.Fprintf(os.Stderr, "⚠ %s: %v\n", tr.ID, err)
			continue
		}
		if written {
			changed++
		}
		fmt.Println(report)
	}

	if !*apply && changed > 0 {
		fmt.Println("\nDRY RUN — nothing was written. Re-run with --apply to persist these baselines.")
	}
	if failed {
		os.Exit(1)
	}
}

func resolveTradersForRepair(st *store.Store, id string) ([]*store.Trader, error) {
	if strings.EqualFold(strings.TrimSpace(id), "all") {
		return st.Trader().ListAll()
	}
	tr, err := st.Trader().GetByID(id)
	if err != nil {
		return nil, err
	}
	if tr == nil {
		return nil, nil
	}
	return []*store.Trader{tr}, nil
}

// repairBaseline reports (and optionally persists) one trader's repaired
// baseline. It returns whether the stored value actually needed changing.
func repairBaseline(st *store.Store, tr *store.Trader, apply bool) (report string, changed bool, err error) {
	// The baseline is reconciled against the wallet, so the repair uses the
	// newest persisted snapshot (the same figure the trading loop feeds to
	// reconcilePnLBaseline) rather than the equity.
	snapshots, err := st.Equity().GetByTimeRange(tr.ID, time.Unix(0, 0), time.Now().Add(24*time.Hour))
	if err != nil {
		return "", false, fmt.Errorf("load equity snapshots: %w", err)
	}
	if len(snapshots) == 0 {
		return fmt.Sprintf("%s (%s)\n  no equity snapshots — nothing to repair", tr.ID, tr.Name), false, nil
	}
	latest := snapshots[len(snapshots)-1]

	netRealized, err := st.Position().GetNetRealizedPnLSince(tr.ID, 0)
	if err != nil {
		return "", false, fmt.Errorf("load realized PnL: %w", err)
	}

	derived := latest.Balance - netRealized
	if derived <= 0 {
		return "", false, fmt.Errorf("derived baseline %.6f is not a usable capital base (wallet %.6f, realized %.6f)",
			derived, latest.Balance, netRealized)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s)\n", tr.ID, tr.Name)
	fmt.Fprintf(&b, "  latest snapshot : wallet=%.6f equity=%.6f unrealized=%.6f at %s\n",
		latest.Balance, latest.TotalEquity, latest.UnrealizedPnL, latest.Timestamp.UTC().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "  net realized PnL: %.6f\n", netRealized)
	fmt.Fprintf(&b, "  stored baseline : %.6f\n", tr.InitialBalance)
	fmt.Fprintf(&b, "  derived baseline: %.6f  (wallet - realized)\n", derived)

	delta := derived - tr.InitialBalance
	switch {
	case math.Abs(delta) < 0.01:
		fmt.Fprintf(&b, "  delta           : %.6f — already consistent, nothing to do", delta)
		return b.String(), false, nil
	case apply:
		if err := st.Trader().SetPnLBaseline(tr.UserID, tr.ID, derived, latest.Balance, time.Now().UTC().UnixMilli()); err != nil {
			return "", false, fmt.Errorf("persist baseline: %w", err)
		}
		fmt.Fprintf(&b, "  delta           : %+.6f — WRITTEN", delta)
	default:
		fmt.Fprintf(&b, "  delta           : %+.6f — would be written with --apply", delta)
	}

	jumps := baselineJumps(snapshots)
	if len(jumps) > 0 {
		fmt.Fprintf(&b, "\n  baseline history (cash flows detected by the reconciler):")
		for _, j := range jumps {
			fmt.Fprintf(&b, "\n    %s  %12.6f → %12.6f  (%+.6f)",
				j.at.UTC().Format("2006-01-02 15:04:05"), j.from, j.to, j.to-j.from)
		}
	}

	return b.String(), true, nil
}

type baselineJump struct {
	at   time.Time
	from float64
	to   float64
}

// baselineJumps lists the points where initial_balance changed, which is exactly
// where the reconciler decided an external transfer had happened. Seeing the
// jumps next to the numbers is what makes a phantom flow obvious.
func baselineJumps(snapshots []*store.EquitySnapshot) []baselineJump {
	var jumps []baselineJump
	var prev float64
	havePrev := false
	for _, s := range snapshots {
		if !havePrev {
			prev = s.InitialBalance
			havePrev = true
			continue
		}
		if math.Abs(s.InitialBalance-prev) < 0.01 {
			continue
		}
		jumps = append(jumps, baselineJump{at: s.Timestamp, from: prev, to: s.InitialBalance})
		prev = s.InitialBalance
	}
	return jumps
}
