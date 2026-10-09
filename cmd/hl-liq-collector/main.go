// cmd/liq-collector snapshots liquidation levels for each coin on a
// 60-second cadence. In a future iteration, this will also subscribe
// to trade tape and reconstruct liq events from OI drops.
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

	"nofx/internal/hlscreener/hyperliquid"
	"nofx/internal/hlscreener/liq"
	"nofx/internal/hlscreener/store"
)

func main() {
	_ = godotenv.Load()
	dsn := envOr("DATABASE_URL", "")
	if dsn == "" {
		log.Fatal("DATABASE_URL required")
	}
	coins := strings.Split(envOr("COLLECTOR_COINS", "BTC,ETH,SOL"), ",")
	binBps, _ := strconv.ParseFloat(envOr("LIQ_BIN_BPS", "10"), 64)
	interval := time.Duration(parseInt(envOr("LIQ_SNAPSHOT_INTERVAL_SEC", "60"), 60)) * time.Second

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

	for _, c := range coins {
		c := c
		go func() {
			// Wrap *store.Store as liq.StoreFunc — liq's LiqLevel
			// type mirrors store.LiqLevel field-for-field.
			stFunc := liq.StoreFunc(func(_ context.Context, l liq.LiqLevel) error {
				return st.UpsertLiqLevel(ctx, store.LiqLevel{
					Coin:        l.Coin,
					BinStart:    l.BinStart,
					BinEnd:      l.BinEnd,
					NotionalUSD: l.NotionalUSD,
					Positions:   l.Positions,
					FirstSeen:   l.FirstSeen,
					LastSeen:    l.LastSeen,
				})
			})
			col := liq.New(c, hl, stFunc, binBps)
			if err := col.Run(ctx, interval); err != nil && err != context.Canceled {
				log.Printf("[liq %s] %v", c, err)
			}
		}()
	}

	<-ctx.Done()
	log.Printf("liq-collector shutting down")
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func parseInt(s string, d int) int {
	var n int
	for _, c := range s {
		if c < '0' || c > '9' {
			return d
		}
		n = n*10 + int(c-'0')
	}
	if n == 0 {
		return d
	}
	return n
}
