package nofxhl

import (
	"context"
	"fmt"
	"sort"
	"time"

	"nofx/provider/nofxos"
)

// NetFlowRanking builds the taker-flow ranking from cvd_bars over the
// requested window ("1h"/"4h"/"24h"; anything else falls back to 24h).
//
// Semantic mapping onto nofxos.NetFlowRankingData: Hyperliquid taker
// flow has no institution/retail attribution, so the CVD ranking fills
// the Institution* tables (the prompt's "Smart Money" section) and the
// Personal* tables stay empty — the ZH/EN formatters skip that section
// when both Personal slices are empty.
func (c *Client) NetFlowRanking(ctx context.Context, duration string, limit int) (*nofxos.NetFlowRankingData, error) {
	ctx, cancel := fetchCtx(ctx)
	defer cancel()
	if limit <= 0 {
		limit = 10
	}
	from := time.Now().UnixMilli() - windowMs(duration)

	pool, err := c.getPool(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, `
		SELECT coin, SUM(delta) AS delta
		FROM cvd_bars
		WHERE bucket >= $1
		GROUP BY coin
	`, from)
	if err != nil {
		return nil, fmt.Errorf("nofxhl cvd query: %w", err)
	}
	defer rows.Close()

	var all []flowRow
	for rows.Next() {
		var r flowRow
		if err := rows.Scan(&r.Coin, &r.Delta); err != nil {
			return nil, err
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("nofxhl: no cvd data in window (is the screener serve mode running?)")
	}

	// Attach prices from the (cached) live universe snapshot.
	prices, _ := c.priceSnapshot(ctx)
	for i := range all {
		all[i].Price = prices[all[i].Coin]
	}

	out := &nofxos.NetFlowRankingData{
		Duration:  duration,
		TimeRange: fmt.Sprintf("cvd window: last %s", duration),
		FetchedAt: time.Now(),
	}

	// Inflow ranking: most positive delta first.
	sort.Slice(all, func(i, j int) bool { return all[i].Delta > all[j].Delta })
	rank := 0
	for _, r := range all {
		if r.Delta <= 0 || rank >= limit {
			break
		}
		rank++
		out.InstitutionFutureTop = append(out.InstitutionFutureTop, nofxos.NetFlowPosition{
			Rank: rank, Symbol: r.Coin, Amount: r.Delta, Price: r.Price,
		})
	}

	// Outflow ranking: most negative delta first.
	sort.Slice(all, func(i, j int) bool { return all[i].Delta < all[j].Delta })
	rank = 0
	for _, r := range all {
		if r.Delta >= 0 || rank >= limit {
			break
		}
		rank++
		out.InstitutionFutureLow = append(out.InstitutionFutureLow, nofxos.NetFlowPosition{
			Rank: rank, Symbol: r.Coin, Amount: r.Delta, Price: r.Price,
		})
	}
	return out, nil
}

// flowRow is one coin's aggregated delta over the ranking window.
type flowRow struct {
	Coin  string
	Delta float64
	Price float64
}
