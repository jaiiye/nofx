package nofxhl

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestE2E_LiveDataPlane runs against a live screener data plane
// (HLSCRAPER_DSN must point at the shared Postgres with the serve
// mode running). Skipped in CI / without configuration.
func TestE2E_LiveDataPlane(t *testing.T) {
	dsn := os.Getenv("HLSCRAPER_DSN")
	if dsn == "" {
		t.Skip("HLSCRAPER_DSN not set")
	}
	c := NewClient(dsn)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	nf, err := c.NetFlowRanking(ctx, "24h", 5)
	if err != nil {
		t.Fatalf("NetFlowRanking: %v", err)
	}
	fmt.Printf("netflow: in=%d out=%d\n",
		len(nf.InstitutionFutureTop), len(nf.InstitutionFutureLow))
	if len(nf.InstitutionFutureTop) > 0 {
		p := nf.InstitutionFutureTop[0]
		fmt.Printf("  top inflow: %s %+.0f USD @ %.2f\n", p.Symbol, p.Amount, p.Price)
	}

	oi, err := c.OIRanking(ctx, "24h", 5)
	if err != nil {
		t.Fatalf("OIRanking: %v", err)
	}
	fmt.Printf("oi: top=%d low=%d\n", len(oi.TopPositions), len(oi.LowPositions))

	pr, err := c.PriceRanking(ctx, "24h", 5)
	if err != nil {
		t.Fatalf("PriceRanking: %v", err)
	}
	fmt.Printf("price: durations=%d\n", len(pr.Durations))

	coins, err := c.TopPoolCoins(ctx, 10)
	if err != nil {
		t.Fatalf("TopPoolCoins: %v", err)
	}
	fmt.Printf("pool: %d coins\n", len(coins))
	for _, p := range coins {
		fmt.Printf("  %s score=%.1f\n", p.Coin, p.Score)
	}
}
