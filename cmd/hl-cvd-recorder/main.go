// cmd/cvd-recorder is a per-coin CVD recorder. Run one instance per
// coin you want CVD for, OR use --coins to multiplex.
//
// Two transports:
//   default  — poll HL recentTrades every 3s (robust, ~3s latency)
//   --ws     — persistent WebSocket subscription (<200ms latency,
//              reconnect with backoff). Recommended for production.
//
// Usage:
//   go run ./cmd/cvd-recorder --coin BTC
//   go run ./cmd/cvd-recorder --coins=BTC,ETH,SOL --ws
//
// Testnet: set HYPERLIQUID_API_URL=https://api.hyperliquid-testnet.xyz
// and HYPERLIQUID_WS_URL=wss://api.hyperliquid-testnet.xyz/ws
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"nofx/internal/hlscreener/cvd"
	"nofx/internal/hlscreener/hyperliquid"
	"nofx/internal/hlscreener/store"
)

func main() {
	_ = godotenv.Load()
	dsn := envOr("DATABASE_URL", "")
	coinFlag := flag.String("coin", "", "single coin to record (e.g. BTC)")
	coinsFlag := flag.String("coins", "", "comma-separated coin list")
	useWS := flag.Bool("ws", false, "use WebSocket transport instead of polling")
	flag.Parse()

	coins := []string{}
	if *coinFlag != "" {
		coins = []string{*coinFlag}
	} else if *coinsFlag != "" {
		coins = strings.Split(*coinsFlag, ",")
	} else if env := os.Getenv("COLLECTOR_COINS"); env != "" {
		coins = strings.Split(env, ",")
	} else {
		log.Fatal("specify --coin or --coins")
	}
	if dsn == "" {
		log.Fatal("DATABASE_URL required")
	}

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
	bucketMs := int64(parseInt(envOr("CVD_BUCKET_MS", "300000"), 300000))
	wsURL := envOr("HYPERLIQUID_WS_URL", cvd.WSDefaultURL)

	for _, c := range coins {
		c := c
		go func() {
			if *useWS {
				r := cvd.NewWS(c, wsURL, hl, storeCVDWriter{st}, bucketMs)
				log.Printf("[cvd-ws %s] starting (url=%s)", c, wsURL)
				if err := r.Run(ctx); err != nil && err != context.Canceled {
					log.Printf("[cvd-ws %s] %v", c, err)
				}
				return
			}
			r := cvd.New(c, hl, st, bucketMs)
			if err := r.Run(ctx, 3*time.Second); err != nil && err != context.Canceled {
				log.Printf("[cvd %s] %v", c, err)
			}
		}()
	}

	<-ctx.Done()
	log.Printf("cvd-recorder shutting down")
}

// storeCVDWriter adapts *store.Store (which takes store.CVDBar) to
// cvd.Store (which takes cvd.CVDBar). Field-identical mirror types.
type storeCVDWriter struct{ st *store.Store }

func (w storeCVDWriter) UpsertCVDBar(ctx context.Context, b cvd.CVDBar) error {
	return w.st.UpsertCVDBar(ctx, store.CVDBar{
		Coin: b.Coin, Bucket: b.Bucket,
		Buy: b.Buy, Sell: b.Sell, Delta: b.Delta,
	})
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
