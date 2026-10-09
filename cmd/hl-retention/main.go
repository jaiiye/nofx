// cmd/retention runs the periodic retention job. Default cadence: hourly.
//
// Usage:
//   go run ./cmd/retention                 # hourly
//   go run ./cmd/retention -once           # run once and exit
//   go run ./cmd/retention -interval 30m   # custom cadence
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"nofx/internal/hlscreener/store"
)

func main() {
	_ = godotenv.Load()
	dsn := envOr("DATABASE_URL", "")
	if dsn == "" {
		log.Fatal("DATABASE_URL required")
	}
	once := flag.Bool("once", false, "run once and exit")
	interval := flag.Duration("interval", time.Hour, "retention cadence")
	flag.Parse()

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

	run := func() {
		rep, err := st.RunRetention(ctx,
			store.DefaultCVDRetentionMs,
			store.DefaultOHLCRetentionMs,
			store.DefaultFundingRetentionMs,
			store.DefaultOIRetentionMs,
		)
		if err != nil {
			log.Printf("retention: %v", err)
			return
		}
		log.Printf("retention: cvd=%d ohlc=%d funding=%d oi=%d",
			rep.CVDRowsDeleted, rep.OHLCRowsDeleted, rep.FundingDeleted, rep.OIDeleted)
	}

	run()
	if *once {
		return
	}

	t := time.NewTicker(*interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
