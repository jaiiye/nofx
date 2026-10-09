// Package nofxhl is the self-hosted data source for nofx: it replaces
// the paid nofxos.ai / claw402 / vergex subscriptions with data from
// the nofx-hl-screener data plane (Hyperliquid public API + local
// CVD/liq collection) and the live Hyperliquid info API.
//
// Files follow the provider/nofxos layout — one file per data source:
//
//	client.go   connection management + shared helpers
//	flow.go     NetFlow ranking (cvd_bars → nofxos.NetFlowRankingData)
//	ranking.go  OI + Price rankings (HL metaAndAssetCtxs, oi_snapshots)
//	pool.go     candidate coins (candidate_active) + liq heatmap
//
// All ranking structs are the nofxos.* types verbatim, so the kernel
// prompt pipeline needs zero changes: only the Fetch* methods branch
// on this client being configured.
//
// This package reads tables written by an external process (the
// nofx-hl-screener "serve" mode). Mirror notice: the internal/hlscreener
// packages are a copy of the nofx-hl-screener repo — that repo is the
// authoritative source; keep changes in sync.
package nofxhl

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"nofx/internal/hlscreener/hyperliquid"
)

// Environment variables:
//
//	HLSCRAPER_DSN         screener Postgres DSN; empty = source disabled
//	HLSCRAPER_HL_API_URL  optional Hyperliquid API override (e.g. testnet)
const (
	DefaultDSNEnv    = "HLSCRAPER_DSN"
	DefaultHLAPIEnv  = "HLSCRAPER_HL_API_URL"
	fetchTimeout     = 15 * time.Second
	priceCacheTTL    = 30 * time.Second
	defaultPoolLimit = 10
)

// Client is lazy-connecting and safe for concurrent use. A nil
// *Client is valid and reports Enabled() == false.
type Client struct {
	dsn string
	hl  *hyperliquid.Client

	mu   sync.Mutex
	pool *pgxpool.Pool

	// priceCache holds one metaAndAssetCtxs result so the several
	// Fetch* calls within one AI cycle share a single HL RPC.
	priceMu  sync.Mutex
	prices   map[string]float64
	universe []UniverseEntry
	pricesAt time.Time
}

// UniverseEntry pairs a universe coin name with its market context —
// the shape returned by metaAndAssetCtxs.
type UniverseEntry struct {
	Name string
	Ctx  hyperliquid.AssetCtx
}

// NewClient creates a provider. The DSN may be empty (provider
// disabled) — every fetch then returns an error the kernel treats
// like any upstream failure.
func NewClient(dsn string) *Client {
	return &Client{
		dsn: dsn,
		hl:  hyperliquid.New(hyperliquid.DefaultAPIURL),
	}
}

// NewClientFromEnv reads the environment and returns nil when
// HLSCRAPER_DSN is unset (self-hosted source not configured).
func NewClientFromEnv() *Client {
	dsn := strings.TrimSpace(os.Getenv(DefaultDSNEnv))
	if dsn == "" {
		return nil
	}
	c := NewClient(dsn)
	if url := strings.TrimSpace(os.Getenv(DefaultHLAPIEnv)); url != "" {
		c.hl = hyperliquid.New(url)
	}
	return c
}

// Enabled reports whether a DSN was configured. Safe on a nil client.
func (c *Client) Enabled() bool { return c != nil && c.dsn != "" }

// getPool lazily connects. Connection errors are NOT cached: if the
// data plane starts after nofx, the next fetch retries and succeeds.
func (c *Client) getPool(ctx context.Context) (*pgxpool.Pool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pool != nil {
		return c.pool, nil
	}
	cfg, err := pgxpool.ParseConfig(c.dsn)
	if err != nil {
		return nil, fmt.Errorf("nofxhl dsn: %w", err)
	}
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("nofxhl connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("nofxhl ping: %w", err)
	}
	c.pool = pool
	return pool, nil
}

// Close releases the pool, if connected.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pool != nil {
		c.pool.Close()
		c.pool = nil
	}
}

// fetchCtx wraps each outgoing fetch in a fixed timeout so a stalled
// data plane or HL API can never block the AI trading cycle.
func fetchCtx(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, fetchTimeout)
}

// priceSnapshot returns coin → mark price (and the raw universe) with
// a short TTL cache. A nil map on error; callers must tolerate it.
func (c *Client) priceSnapshot(ctx context.Context) (map[string]float64, []UniverseEntry) {
	c.priceMu.Lock()
	defer c.priceMu.Unlock()
	if c.prices != nil && time.Since(c.pricesAt) < priceCacheTTL {
		return c.prices, c.universe
	}
	ctxs, err := c.hl.AssetContexts(ctx)
	if err != nil {
		return nil, nil
	}
	m := make(map[string]float64, len(ctxs.Universe))
	u := make([]UniverseEntry, 0, len(ctxs.Universe))
	for i, a := range ctxs.Universe {
		m[a.Name] = ctxs.Contexts[i].Mark()
		u = append(u, UniverseEntry{Name: a.Name, Ctx: ctxs.Contexts[i]})
	}
	c.prices = m
	c.universe = u
	c.pricesAt = time.Now()
	return m, u
}

func windowMs(duration string) int64 {
	d := strings.ToLower(strings.TrimSpace(duration))
	switch {
	case strings.HasSuffix(d, "h"):
		var n int
		if _, err := fmt.Sscanf(d, "%dh", &n); err != nil || n <= 0 || n > 168 {
			n = 24
		}
		return int64(n) * 3600 * 1000
	case strings.HasSuffix(d, "d"):
		var n int
		if _, err := fmt.Sscanf(d, "%dd", &n); err != nil || n <= 0 || n > 7 {
			n = 1
		}
		return int64(n) * 24 * 3600 * 1000
	default:
		return 24 * 3600 * 1000
	}
}
