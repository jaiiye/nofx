// cmd/screener is a one-shot CLI: run the screener once and print the
// active pool. Useful for cron + ad-hoc inspection.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/joho/godotenv"

	"nofx/internal/hlscreener/hyperliquid"
	"nofx/internal/hlscreener/pool"
	"nofx/internal/hlscreener/store"
)

func main() {
	_ = godotenv.Load()
	dsn := envOr("DATABASE_URL", "")
	if dsn == "" {
		log.Fatal("DATABASE_URL required")
	}
	coinsFlag := flag.String("coins", envOr("COLLECTOR_COINS", "BTC,ETH,SOL,BNB,XRP"), "comma-separated coin list")
	jsonOut := flag.Bool("json", false, "JSON output")
	flag.Parse()

	coins := strings.Split(*coinsFlag, ",")

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
	hl := hyperliquid.New(envOr("HYPERLIQUID_API_URL", hyperliquid.DefaultAPIURL))

	s := pool.New(hl, st, pool.DefaultThresholds())
	results, stats, err := s.Run(ctx, coins)
	if err != nil {
		log.Fatalf("screener: %v", err)
	}
	if err := s.PersistResults(ctx, results); err != nil {
		log.Printf("persist: %v", err)
	}

	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{
			"stats":   stats,
			"results": results,
		})
		return
	}

	fmt.Println("=== Screener run ===")
	fmt.Printf("input=%d survivors=%d\n", stats["input"], stats["survivors"])
	for k, v := range stats {
		if k == "input" || k == "survivors" {
			continue
		}
		fmt.Printf("  after %s: %d\n", k, v)
	}
	fmt.Println()
	for _, r := range results {
		mark := " "
		if r.InPool {
			mark = "✓"
		}
		fmt.Printf("%s %-6s score=%5.1f  ", mark, r.Coin, r.Score)
		for _, k := range []string{"liquidity", "volatility", "structure", "volprice", "event"} {
			lr := r.Layers[k]
			if lr.Pass {
				fmt.Printf("%s ", k[:3])
			} else {
				fmt.Printf("_%s ", k[:3])
			}
		}
		fmt.Println()
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
