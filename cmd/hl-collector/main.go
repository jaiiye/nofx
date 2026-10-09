// cmd/collector is the central orchestrator. It periodically:
//   1. Refreshes coin_meta (perps universe) from HL
//   2. Updates OHLC 1m for each top coin
//   3. Captures funding + OI snapshots
//   4. Triggers the screener (every SCREENER_REFRESH_SEC)
//
// For per-coin high-frequency work (CVD, liq) it spawns subprocesses via
// cmd/cvd-recorder and cmd/liq-collector. This binary itself is the
// supervisor, and uses a single HL client for low-rate ops.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"nofx/internal/hlscreener/coinalyze"
	"nofx/internal/hlscreener/hyperliquid"
	"nofx/internal/hlscreener/pool"
	"nofx/internal/hlscreener/store"
)

func main() {
	_ = godotenv.Load()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL required")
	}
	coins := strings.Split(envOr("COLLECTOR_COINS", "BTC,ETH,SOL,BNB,XRP"), ",")

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	st, err := store.New(ctx, dsn)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()
	if err := st.EnsureSchema(ctx); err != nil {
		log.Fatalf("schema: %v", err)
	}

	hl := hyperliquid.New(os.Getenv("HYPERLIQUID_API_URL"))

	// Coinalyze (optional; needs COINALYZE_API_KEY).
	// Polls OI + funding history every hour. Free tier: 200 calls/day
	// so we have to be careful — 1 call per coin per hour, with 5
	// coins that's 120 calls/day (within budget).
	var cz *coinalyze.Client
	if key := os.Getenv("COINALYZE_API_KEY"); key != "" {
		cz = coinalyze.New(os.Getenv("COINALYZE_BASE_URL"), key)
		log.Printf("coinalyze enabled (base=%s)", os.Getenv("COINALYZE_BASE_URL"))
	} else {
		log.Printf("coinalyze disabled (no COINALYZE_API_KEY)")
	}

	// meta snapshot
	if err := refreshMeta(ctx, hl); err != nil {
		log.Printf("meta refresh: %v", err)
	}

	// OHLC loop (1m)
	go ohlcLoop(ctx, hl, st, coins, time.Minute)

	// funding/OI loop (1m)
	go snapshotLoop(ctx, hl, st, coins, time.Minute)

	// Coinalyze OI history (hourly; only if key set)
	if cz != nil {
		go coinalyzeOILoop(ctx, cz, st, coins, time.Hour)
	}

	// screener (every SCREENER_REFRESH_SEC)
	refresh := time.Duration(parseInt(os.Getenv("SCREENER_REFRESH_SEC"), 3600)) * time.Second
	go screenerLoop(ctx, hl, st, coins, refresh)

	<-ctx.Done()
	log.Printf("collector shutting down")
}

func refreshMeta(ctx context.Context, hl *hyperliquid.Client) error {
	m, err := hl.Meta(ctx)
	if err != nil {
		return err
	}
	log.Printf("meta: %d perps", len(m.Universe))
	return nil
}

func ohlcLoop(ctx context.Context, hl *hyperliquid.Client, st *store.Store, coins []string, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// HL only keeps ~5000 bars of recent data; ask for the
			// last 10 minutes so we always get a candle. endTime
			// defaults to "now" in Candles() so we just need a
			// reasonable start.
			from := time.Now().Add(-10 * time.Minute).UnixMilli()
			for _, c := range coins {
				candles, err := hl.Candles(ctx, c, "1m", from)
				if err != nil {
					log.Printf("[ohlc %s] %v", c, err)
					continue
				}
				for _, cd := range candles {
					_ = st.UpsertOHLC(ctx, store.OHLC{
						Coin: c,
						T:    cd.T,
						O:    cd.Open(), H: cd.High(), L: cd.Low(), C: cd.Close(),
						V: cd.Volume(),
					})
				}
			}
		}
	}
}

func snapshotLoop(ctx context.Context, hl *hyperliquid.Client, st *store.Store, coins []string, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := time.Now().UnixMilli()
			// AllMids returns the full universe in one call — fetch
			// once, then loop.
			mids, merr := hl.AllMids(ctx)
			if merr != nil {
				log.Printf("[snapshot] allMids: %v", merr)
				continue
			}
			for _, c := range coins {
				oi, oerr := hl.OpenInterest(ctx, c)
				if oerr != nil {
					continue
				}
				mid := parseF(mids[c])
				_ = st.UpsertOI(ctx, c, now, oi.OpenInterest*mid)
				fr, ferr := hl.FundingHistory(ctx, c, now-3*3600*1000)
				if ferr != nil {
					continue
				}
				for _, f := range fr {
					_ = st.UpsertFunding(ctx, c, f.Time, f.Rate(), mid)
				}
			}
		}
	}
}

func screenerLoop(ctx context.Context, hl *hyperliquid.Client, st *store.Store, coins []string, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	runScreener(ctx, hl, st, coins)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			runScreener(ctx, hl, st, coins)
		}
	}
}

func runScreener(ctx context.Context, hl *hyperliquid.Client, st *store.Store, coins []string) {
	s := pool.New(hl, st, pool.DefaultThresholds())
	res, stats, err := s.Run(ctx, coins)
	if err != nil {
		log.Printf("screener: %v", err)
		return
	}
	if err := s.PersistResults(ctx, res); err != nil {
		log.Printf("persist: %v", err)
		return
	}
	log.Printf("screener: %+v", stats)
	for _, r := range res {
		if r.InPool {
			log.Printf("  ✓ %s score=%.1f", r.Coin, r.Score)
		}
	}
}

// coinalyzeOILoop pulls hourly OI history from Coinalyze and writes
// each bar to oi_snapshots. Stops on ctx cancel. Skips if key not
// configured.
//
// Free tier budget: 200 calls/day, 40/min. We pull N coins in
// ONE call (Coinalyze accepts comma-separated symbols) → 1 call
// per hour → 24 calls/day for any number of coins.
//
// We build a coinalyze-symbol → HL-coin map so writes go to
// oi_snapshots under the HL name (e.g. "BTC"), matching what
// the screener's OIHistory(coin) lookup expects.
func coinalyzeOILoop(ctx context.Context, cz *coinalyze.Client, st *store.Store, coins []string, interval time.Duration) {
	// Build coinalyze symbol → HL coin map once.
	syms, symToCoin := buildCoinalyzeMap(coins)
	tick := func() {
		// 24h window of 1h OI bars
		to := time.Now().Unix()
		from := to - 24*3600
		hist, err := cz.OIHistory(ctx, syms, "1hour", from, to)
		if err != nil {
			log.Printf("[coinalyze] OIHistory: %v", err)
			return
		}
		written := ingestCoinalyzeOI(ctx, st, symToCoin, hist)
		log.Printf("[coinalyze] wrote %d OI snapshots (across %d coins, mapped to HL names)",
			written, len(syms))
	}
	tick() // initial run
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}

// buildCoinalyzeMap converts HL coin names to coinalyze symbols and
// returns both the symbol list and a reverse-lookup map. Extracted
// for testability.
func buildCoinalyzeMap(coins []string) ([]string, map[string]string) {
	syms := make([]string, 0, len(coins))
	symToCoin := map[string]string{}
	for _, c := range coins {
		s := coinalyze.HLToCoinalyze(c)
		syms = append(syms, s)
		symToCoin[s] = c
	}
	return syms, symToCoin
}

// OIWriter is the minimal interface ingestCoinalyzeOI needs. Tests
// can provide a stub; production wraps *store.Store.
type OIWriter interface {
	UpsertOI(ctx context.Context, coin string, t int64, oiUSD float64) error
}

// ingestCoinalyzeOI writes coinalyze OI history under the HL coin
// name. Returns the number of rows written. Unknown symbols (not
// in symToCoin) are skipped to prevent table pollution.
// Extracted for testability.
func ingestCoinalyzeOI(
	ctx context.Context,
	w OIWriter,
	symToCoin map[string]string,
	hist []coinalyze.OIHist,
) int {
	written := 0
	for _, h := range hist {
		coin, ok := symToCoin[h.Symbol]
		if !ok {
			continue
		}
		if err := w.UpsertOI(ctx, coin, h.Time, h.OI); err != nil {
			log.Printf("[coinalyze] UpsertOI %s: %v", coin, err)
			continue
		}
		written++
	}
	return written
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func parseInt(s string, d int) int {
	n, err := strconv.Atoi(s)
	if err != nil || n == 0 {
		return d
	}
	return n
}

func parseF(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}
