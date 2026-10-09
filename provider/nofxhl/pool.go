package nofxhl

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// PoolCoin is one entry of the screener's active candidate pool.
type PoolCoin struct {
	Coin        string  `json:"coin"`
	Score       float64 `json:"score"`
	ActivatedAt int64   `json:"activated_at"`
	ExpiresAt   int64   `json:"expires_at"`
}

// TopPoolCoins returns the in-pool coins ordered by score. The pool is
// empty while the screener is still collecting — callers (kernel) fall
// back to static coins in that case.
func (c *Client) TopPoolCoins(ctx context.Context, limit int) ([]PoolCoin, error) {
	ctx, cancel := fetchCtx(ctx)
	defer cancel()
	if limit <= 0 {
		limit = defaultPoolLimit
	}
	pool, err := c.getPool(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, `
		SELECT coin, score, activated_at, expires_at
		FROM candidate_active
		WHERE expires_at > $1
		ORDER BY score DESC
		LIMIT $2
	`, time.Now().UnixMilli(), limit)
	if err != nil {
		return nil, fmt.Errorf("nofxhl pool query: %w", err)
	}
	defer rows.Close()
	var out []PoolCoin
	for rows.Next() {
		var pc PoolCoin
		if err := rows.Scan(&pc.Coin, &pc.Score, &pc.ActivatedAt, &pc.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, pc)
	}
	return out, rows.Err()
}

// LiqHeatmapMarkdown renders the densest liquidation bins for one coin
// as a markdown table (replaces the paid vergex heatmap formatting in
// the AI prompt). Only the top-N bins by notional are shown, each
// tagged with how long ago the level was first observed.
func (c *Client) LiqHeatmapMarkdown(ctx context.Context, coin string, limit int) (string, error) {
	ctx, cancel := fetchCtx(ctx)
	defer cancel()
	if limit <= 0 {
		limit = 8
	}
	pool, err := c.getPool(ctx)
	if err != nil {
		return "", err
	}
	rows, err := pool.Query(ctx, `
		SELECT bin_start, bin_end, liquidation_usd, positions_count, first_seen
		FROM liq_levels
		WHERE coin = $1
		ORDER BY liquidation_usd DESC
		LIMIT $2
	`, coin, limit)
	if err != nil {
		return "", fmt.Errorf("nofxhl liq query: %w", err)
	}
	defer rows.Close()

	type binRow struct {
		start, end, notional float64
		positions            int
		firstSeen            int64
	}
	var bins []binRow
	for rows.Next() {
		var b binRow
		if err := rows.Scan(&b.start, &b.end, &b.notional, &b.positions, &b.firstSeen); err != nil {
			return "", err
		}
		bins = append(bins, b)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(bins) == 0 {
		return "", fmt.Errorf("nofxhl: no liq data for %s", coin)
	}

	now := time.Now().UnixMilli()
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Liquidation Heatmap — %s (self-hosted, tiered-leverage model)\n\n", coin)
	sb.WriteString("| Price Zone | Liq Notional (USD) | Positions | Level Age |\n")
	sb.WriteString("|---|---|---|---|\n")
	for _, b := range bins {
		age := "n/a"
		if b.firstSeen > 0 {
			age = fmt.Sprintf("%.0fh", float64(now-b.firstSeen)/3600000)
		}
		fmt.Fprintf(&sb, "| %.4g – %.4g | %.0f | %d | %s |\n",
			b.start, b.end, b.notional, b.positions, age)
	}
	sb.WriteString("\n")
	return sb.String(), nil
}
