// Package coinalyze wraps the Coinalyze public REST API (free tier).
//
// Free tier limits: 200 calls/day, 60 calls/min. We use it for:
//   - historical OI (per-coin, per-interval)
//   - funding rate history
//   - liquidation history (long/short notional at intervals)
//
// Docs: https://api.coinalyze.net/v1/doc/
package coinalyze

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	DefaultBaseURL = "https://api.coinalyze.net/v1"
)

type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func New(baseURL, apiKey string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		http: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// get issues a GET request to path?q=... with the api key.
func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	if c.apiKey == "" {
		return fmt.Errorf("coinalyze: api key empty")
	}
	u := c.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("api_key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		buf, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("coinalyze %d: %s", resp.StatusCode, string(buf))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// ============================================================================
// OHLC + OI history
// ============================================================================
//
// Coinalyze returns NESTED arrays: outer index = requested symbol order,
// inner array = historical points. We flatten and tag each row with its
// symbol so callers don't lose provenance.
//
// Timestamps: Coinalyze uses UNIX seconds (not ms). All public funcs
// take seconds for that reason; convert at the call site.

type OIHist struct {
	Symbol string
	Time   int64
	OI     float64 // in contracts (use convert_to_usd=true for USD)
	Close  float64
}

func (c *Client) OIHistory(ctx context.Context, symbols []string, interval string, fromTs, toTs int64) ([]OIHist, error) {
	q := url.Values{}
	q.Set("symbols", joinCSV(symbols))
	q.Set("interval", interval) // "5min", "1hour", "daily"
	q.Set("from", fmt.Sprintf("%d", fromTs))
	q.Set("to", fmt.Sprintf("%d", toTs))
	// Response shape: [{symbol, history: [{t, o, h, l, c}]}, ...]
	var raw []struct {
		Symbol  string `json:"symbol"`
		History []struct {
			T int64   `json:"t"`
			O float64 `json:"o"`
			H float64 `json:"h"`
			L float64 `json:"l"`
			C float64 `json:"c"`
		} `json:"history"`
	}
	if err := c.get(ctx, "/open-interest-history", q, &raw); err != nil {
		return nil, err
	}
	var out []OIHist
	for _, row := range raw {
		for _, p := range row.History {
			out = append(out, OIHist{
				Symbol: row.Symbol,
				Time:   p.T,
				OI:     p.C, // OI is a single number per bar; we use c as the value
				Close:  p.C,
			})
		}
	}
	return out, nil
}

// ============================================================================
// Funding rate history
// ============================================================================

type FundingHist struct {
	Symbol      string
	Time        int64
	FundingRate float64
}

func (c *Client) FundingHistory(ctx context.Context, symbols []string, fromTs, toTs int64) ([]FundingHist, error) {
	q := url.Values{}
	q.Set("symbols", joinCSV(symbols))
	q.Set("from", fmt.Sprintf("%d", fromTs))
	q.Set("to", fmt.Sprintf("%d", toTs))
	// Response: [{symbol, history: [{t, o, h, l, c}]}, ...] — same shape
	var raw []struct {
		Symbol  string `json:"symbol"`
		History []struct {
			T int64   `json:"t"`
			O float64 `json:"o"`
			H float64 `json:"h"`
			L float64 `json:"l"`
			C float64 `json:"c"`
		} `json:"history"`
	}
	if err := c.get(ctx, "/funding-rate-history", q, &raw); err != nil {
		return nil, err
	}
	var out []FundingHist
	for _, row := range raw {
		for _, p := range row.History {
			out = append(out, FundingHist{
				Symbol:      row.Symbol,
				Time:        p.T,
				FundingRate: p.C, // funding rate is a single value; Coinalyze returns it in `c`
			})
		}
	}
	return out, nil
}

// ============================================================================
// Liquidation history (aggregated, per-bar)
// ============================================================================
//
// Response shape: [{symbol, history: [{t, l, s}]}, ...]
// `l` = long liquidations, `s` = short liquidations, in USD.

type LiqHist struct {
	Symbol   string
	Time     int64
	LiqLong  float64
	LiqShort float64
}

func (c *Client) LiquidationHistory(ctx context.Context, symbols []string, interval string, fromTs, toTs int64) ([]LiqHist, error) {
	q := url.Values{}
	q.Set("symbols", joinCSV(symbols))
	q.Set("interval", interval)
	q.Set("from", fmt.Sprintf("%d", fromTs))
	q.Set("to", fmt.Sprintf("%d", toTs))
	var raw []struct {
		Symbol  string `json:"symbol"`
		History []struct {
			T int64   `json:"t"`
			L float64 `json:"l"`
			S float64 `json:"s"`
		} `json:"history"`
	}
	if err := c.get(ctx, "/liquidation-history", q, &raw); err != nil {
		return nil, err
	}
	var out []LiqHist
	for _, row := range raw {
		for _, p := range row.History {
			out = append(out, LiqHist{
				Symbol:   row.Symbol,
				Time:     p.T,
				LiqLong:  p.L,
				LiqShort: p.S,
			})
		}
	}
	return out, nil
}

// ============================================================================
// Symbol mapping helper — Hyperliquid ticker (e.g. "BTC") → Coinalyze ("btcusdt_perp")
// ============================================================================

// HLToCoinalyze converts a HL coin (e.g. "BTC") to a Coinalyze symbol.
//
// Coinalyze exchange codes: 0=Binance, A=Aevo, etc. Hyperliquid is "H"
// (verified via coinalyze.net/hyperliquid/liquidations URL pattern).
// HIP-3 builder-dex coins are not currently supported by Coinalyze.
//
// Examples:
//   HLToCoinalyze("BTC") = "BTCUSDT_PERP.H"
//   HLToCoinalyze("ETH") = "ETHUSDT_PERP.H"
func HLToCoinalyze(coin string) string {
	return coin + "USDT_PERP.H"
}

func joinCSV(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}
