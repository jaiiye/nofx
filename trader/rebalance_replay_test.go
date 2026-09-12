package trader

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"nofx/provider/hyperliquid"
)

// This file replays recorded history through the *real* correlation-group
// helper to answer one question the live book could not: when the
// notional/equity ratio runs away during an equity decline, how much of the
// book sits in duplicate correlation groups, and what a per-group rebalance
// would have removed?
//
// It is deliberately a test rather than a cmd/ binary: the sizing and grouping
// helpers are unexported, and duplicating them outside the package would
// reintroduce exactly the semantic drift that made the Python simulator diverge
// from live (+36.5% vs -33.5%). Reusing the real functions is the whole point.
//
// Skipped unless NOFX_REPLAY_DIR points at a directory produced by
// scripts/optimize/extract.py.

type replayPosition struct {
	symbol     string
	side       string
	entryPrice float64
	quantity   float64
	notional   float64
	entryTS    time.Time
	exitTS     time.Time
	pnl        float64
}

type replaySnapshot struct {
	ts     time.Time
	equity float64
	open   []replayPosition
	groups map[string][]string // correlation group -> symbols held
	ratio  float64
}

func replayDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("NOFX_REPLAY_DIR")
	if dir == "" {
		t.Skip("set NOFX_REPLAY_DIR to scripts/optimize/data to run the replay")
	}
	if _, err := os.Stat(filepath.Join(dir, "positions.csv")); err != nil {
		t.Skipf("replay dataset missing in %s: %v", dir, err)
	}
	return dir
}

func readCSV(t *testing.T, path string) []map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(rows) == 0 {
		return nil
	}
	out := make([]map[string]string, 0, len(rows)-1)
	header := rows[0]
	for _, r := range rows[1:] {
		m := make(map[string]string, len(header))
		for i, k := range header {
			if i < len(r) {
				m[k] = r[i]
			}
		}
		out = append(out, m)
	}
	return out
}

func atof(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return v
}

func parseTS(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts
}

// loadReplayPositions returns the closed round trips the book actually took.
func loadReplayPositions(t *testing.T, dir string) []replayPosition {
	t.Helper()
	rows := readCSV(t, filepath.Join(dir, "positions.csv"))
	out := make([]replayPosition, 0, len(rows))
	for _, r := range rows {
		entryPrice := atof(r["entry_price"])
		qty := atof(r["quantity"])
		out = append(out, replayPosition{
			symbol:     r["symbol"],
			side:       strings.ToLower(r["side"]),
			entryPrice: entryPrice,
			quantity:   qty,
			notional:   qty * entryPrice,
			entryTS:    parseTS(t, r["entry_ts"]),
			exitTS:     parseTS(t, r["exit_ts"]),
			pnl:        atof(r["realized_pnl"]),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].entryTS.Before(out[j].entryTS) })
	return out
}

// buildSnapshots replays the recorded position book forward in time, marking
// every point at which a correlation group carried more than one symbol.
func buildSnapshots(t *testing.T, dir string, positions []replayPosition) []replaySnapshot {
	t.Helper()
	rows := readCSV(t, filepath.Join(dir, "cycles.csv"))
	out := make([]replaySnapshot, 0, len(rows))
	for _, r := range rows {
		ts := parseTS(t, r["ts"])
		equity := atof(r["equity"])
		if equity <= 0 {
			continue
		}
		var open []replayPosition
		notional := 0.0
		groups := make(map[string][]string)
		for _, p := range positions {
			if p.entryTS.After(ts) || !p.exitTS.After(ts) {
				continue
			}
			open = append(open, p)
			notional += p.notional
			if g := hyperliquid.CorrelationGroup(p.symbol); g != "" {
				groups[g] = append(groups[g], p.symbol)
			}
		}
		out = append(out, replaySnapshot{
			ts:     ts,
			equity: equity,
			open:   open,
			groups: groups,
			ratio:  notional / equity,
		})
	}
	return out
}

// duplicateGroups reports which correlation groups held more than one symbol.
func duplicateGroups(s replaySnapshot) map[string][]string {
	dup := make(map[string][]string)
	for g, syms := range s.groups {
		if len(syms) > 1 {
			dup[g] = syms
		}
	}
	return dup
}

// redundantNotional is the notional that a one-symbol-per-group rule would have
// removed from this snapshot: for a group of N symbols, keeping the single
// best-signal leg means (N-1)/N of that group's notional is redundant.
func redundantNotional(s replaySnapshot) float64 {
	dup := duplicateGroups(s)
	if len(dup) == 0 {
		return 0
	}
	byBase := make(map[string]float64, len(s.open))
	for _, p := range s.open {
		byBase[universeBaseKey(p.symbol)] += p.notional
	}
	total := 0.0
	for _, syms := range dup {
		groupNotional := 0.0
		for _, sym := range syms {
			groupNotional += byBase[universeBaseKey(sym)]
		}
		n := float64(len(syms))
		total += groupNotional * (n - 1) / n
	}
	return total
}

// TestRebalanceReplaySurfacesRedundantExposure is the B1 report. It does not
// assert a profit: it quantifies how much exposure sat in duplicate correlation
// groups at the moment the book was most levered, which is the precondition for
// any position-period rebalance to matter.
func TestRebalanceReplaySurfacesRedundantExposure(t *testing.T) {
	dir := replayDir(t)
	positions := loadReplayPositions(t, dir)
	snapshots := buildSnapshots(t, dir, positions)
	if len(snapshots) == 0 {
		t.Skip("no usable snapshots")
	}

	var over []replaySnapshot
	for _, s := range snapshots {
		if s.ratio > 4.5 {
			over = append(over, s)
		}
	}

	fmt.Printf("\n=== B1 correlation rebalance replay ===\n")
	fmt.Printf("snapshots=%d  positions=%d  over-4.5x=%d\n",
		len(snapshots), len(positions), len(over))

	dupOver := 0
	for _, s := range over {
		if len(duplicateGroups(s)) > 0 {
			dupOver++
		}
	}
	fmt.Printf("over-4.5x snapshots carrying a duplicate group: %d/%d (%.1f%%)\n",
		dupOver, len(over), share(dupOver, len(over)))

	groupHits := map[string]int{}
	redundantTotal, redundantRatioTotal := 0.0, 0.0
	for _, s := range snapshots {
		for g := range duplicateGroups(s) {
			groupHits[g]++
		}
		r := redundantNotional(s)
		redundantTotal += r
		redundantRatioTotal += r / s.equity
	}
	fmt.Printf("duplicate-group snapshot counts: %v\n", groupHits)
	fmt.Printf("average redundant notional per snapshot: $%.2f (%.2fx equity)\n",
		redundantTotal/float64(len(snapshots)),
		redundantRatioTotal/float64(len(snapshots)))
	fmt.Printf("correlation groups known today: %v\n", knownCorrelationGroups())

	// How much exposure a one-per-group rule would have freed while the book
	// ran above 4.5x. Averaged per window, not summed: consecutive snapshots
	// describe the same positions, so a sum would count them many times.
	if len(over) > 0 {
		sum, peak := 0.0, 0.0
		for _, s := range over {
			r := redundantNotional(s)
			sum += r
			if r > peak {
				peak = r
			}
		}
		fmt.Printf("redundant notional in over-4.5x windows: avg $%.2f  peak $%.2f\n",
			sum/float64(len(over)), peak)
	}

	// Rule A — the shipped open-time gate (2514ed3d): a leg that entered while
	// an earlier same-group leg was still open would never have been opened.
	blocked, avoided := shippedGateAvoided(positions)
	fmt.Printf("shipped open-time gate: %d legs blocked, avoided P&L %.3f\n",
		blocked, avoided)

	// Rule B — a rank-preferred rebalance: inside each duplicate cluster keep
	// the leg the live board ranked best when the cluster formed, drop the
	// rest. Union-find clustering puts every leg in exactly one cluster, so no
	// leg is counted on both sides of the comparison.
	board := loadSignalRanks(t, dir)
	clusters := duplicateClusters(positions)
	fmt.Printf("\n--- duplicate clusters, rank-preferred rebalance ---\n")
	fmt.Printf("%-16s %-12s %-22s %10s %10s\n",
		"group", "cluster-at", "kept (best rank)", "kept P&L", "dropped P&L")
	keptTotal, droppedTotal := 0.0, 0.0
	unrankable := 0
	for _, cl := range clusters {
		boardAt := boardSnapshotAt(board, earliestEntry(cl))
		rankable := true
		for _, p := range cl {
			if boardRank(boardAt, p) == missingRank {
				rankable = false
				break
			}
		}
		if !rankable {
			unrankable++
			continue
		}
		sort.Slice(cl, func(i, j int) bool {
			return boardRank(boardAt, cl[i]) < boardRank(boardAt, cl[j])
		})
		dropped := sumPnL(cl[1:])
		keptTotal += cl[0].pnl
		droppedTotal += dropped
		fmt.Printf("%-16s %-12s %-22s %10.3f %10.3f\n",
			hyperliquid.CorrelationGroup(cl[0].symbol),
			earliestEntry(cl).Format("01-02 15:04"),
			cl[0].symbol, cl[0].pnl, dropped)
	}
	fmt.Printf("\nranked clusters: %d (unrankable: %d)  kept %.3f  dropped %.3f  delta %+.3f\n",
		len(clusters)-unrankable, unrankable,
		keptTotal, droppedTotal, keptTotal-droppedTotal)
}

// shippedGateAvoided applies the open-time correlation gate exactly as shipped
// (2514ed3d): an open is blocked when the book already holds the same group.
// Positions arrive sorted by entry time, so an earlier overlapping same-group
// leg means this open would have been blocked and its P&L avoided. Same-symbol
// overlaps are excluded — live blocks those through the positions map, not the
// group gate, and sync-lagged timestamps can make them look concurrent.
func shippedGateAvoided(positions []replayPosition) (blocked int, avoided float64) {
	for j, p := range positions {
		g := hyperliquid.CorrelationGroup(p.symbol)
		if g == "" {
			continue
		}
		base := universeBaseKey(p.symbol)
		for i := 0; i < j; i++ {
			o := positions[i]
			if hyperliquid.CorrelationGroup(o.symbol) != g ||
				universeBaseKey(o.symbol) == base {
				continue
			}
			if o.entryTS.Before(p.exitTS) && p.entryTS.Before(o.exitTS) {
				blocked++
				avoided += p.pnl
				break
			}
		}
	}
	return blocked, avoided
}

// duplicateClusters groups legs into connected components of overlapping
// same-group positions. A component may chain through time (A overlaps B, B
// overlaps C, A and C never coexist); connected components keep every leg in
// exactly one cluster, which point-in-time grouping cannot guarantee.
func duplicateClusters(positions []replayPosition) [][]replayPosition {
	parent := make([]int, len(positions))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}
	for i := 0; i < len(positions); i++ {
		gi := hyperliquid.CorrelationGroup(positions[i].symbol)
		if gi == "" {
			continue
		}
		base := universeBaseKey(positions[i].symbol)
		for j := i + 1; j < len(positions); j++ {
			if hyperliquid.CorrelationGroup(positions[j].symbol) != gi ||
				universeBaseKey(positions[j].symbol) == base {
				continue
			}
			if positions[j].entryTS.Before(positions[i].exitTS) &&
				positions[i].entryTS.Before(positions[j].exitTS) {
				union(i, j)
			}
		}
	}
	members := map[int][]replayPosition{}
	for i, p := range positions {
		if hyperliquid.CorrelationGroup(p.symbol) == "" {
			continue
		}
		root := find(i)
		members[root] = append(members[root], p)
	}
	out := make([][]replayPosition, 0, len(members))
	for _, cl := range members {
		if len(cl) < 2 {
			continue
		}
		sort.Slice(cl, func(a, b int) bool { return cl[a].entryTS.Before(cl[b].entryTS) })
		out = append(out, cl)
	}
	sort.Slice(out, func(a, b int) bool {
		return out[a][0].entryTS.Before(out[b][0].entryTS)
	})
	return out
}

// earliestEntry returns the first entry timestamp in a cluster.
func earliestEntry(cluster []replayPosition) time.Time {
	earliest := cluster[0].entryTS
	for _, p := range cluster[1:] {
		if p.entryTS.Before(earliest) {
			earliest = p.entryTS
		}
	}
	return earliest
}

func sumPnL(ps []replayPosition) float64 {
	total := 0.0
	for _, p := range ps {
		total += p.pnl
	}
	return total
}

// loadSignalRanks indexes every board snapshot as ts -> base symbol -> rank, so
// a cluster can be ranked by what the live loop actually saw at that moment.
func loadSignalRanks(t *testing.T, dir string) map[time.Time]map[string]int {
	t.Helper()
	rows := readCSV(t, filepath.Join(dir, "signals.csv"))
	out := map[time.Time]map[string]int{}
	for _, r := range rows {
		rank, err := strconv.Atoi(strings.TrimSpace(r["rank"]))
		if err != nil || rank <= 0 {
			continue
		}
		ts := parseTS(t, r["ts"])
		if out[ts] == nil {
			out[ts] = map[string]int{}
		}
		out[ts][universeBaseKey(r["symbol"])] = rank
	}
	return out
}

// boardSnapshotAt returns the most recent board at or before t. Boards are
// published every 30 minutes, so a position's cluster cannot be ranked by a
// board that had not been published yet when it formed.
func boardSnapshotAt(board map[time.Time]map[string]int, t time.Time) map[string]int {
	var best time.Time
	var found map[string]int
	for ts, ranks := range board {
		if ts.After(t) {
			continue
		}
		if best.IsZero() || ts.After(best) {
			best, found = ts, ranks
		}
	}
	if found == nil {
		return map[string]int{}
	}
	return found
}

// missingRank marks a leg the board snapshot does not cover; a cluster
// containing such a leg cannot be ranked fairly and is reported separately.
const missingRank = 1 << 30

func boardRank(board map[string]int, p replayPosition) int {
	if r, ok := board[universeBaseKey(p.symbol)]; ok {
		return r
	}
	return missingRank
}

func share(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b) * 100
}

// knownCorrelationGroups enumerates the buckets the live helper can return, so
// a replay that never triggers one is visibly a data problem, not a logic one.
func knownCorrelationGroups() []string {
	samples := []string{
		"CL", "BRENTOIL", "GOLD", "COPPER", "CORN", "NVDA", "SP500",
		"AAPL", "BTC", "ARBUSDT",
	}
	seen := map[string]bool{}
	for _, s := range samples {
		if g := hyperliquid.CorrelationGroup(s); g != "" {
			seen[g] = true
		}
	}
	out := make([]string, 0, len(seen))
	for g := range seen {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}
