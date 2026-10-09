// Package hyperliquid is a minimal client for the public Hyperliquid API.
//
// Endpoints used (all public, no auth):
//   POST /info           - meta, allMids, candleSnapshot, fundingHistory, openInterest
//   POST /info           - recentTrades (per coin, for CVD)
//   WS   /ws             - trades, candle, allMids streams
//
// Reference: https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api
package hyperliquid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

const (
	DefaultAPIURL = "https://api.hyperliquid.xyz"
	DefaultWSURL  = "wss://api.hyperliquid.xyz/ws"
)

type Client struct {
	apiURL string
	http   *http.Client
}

func New(apiURL string) *Client {
	if apiURL == "" {
		apiURL = DefaultAPIURL
	}
	return &Client{
		apiURL: apiURL,
		http: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// InfoRequest is the universal payload for POST /info.
type InfoRequest struct {
	Type string         `json:"type"`
	Body map[string]any `json:",inline"` // fields merged into root; HL API expects flat shape
}

// info is the internal helper that POSTs to /info and decodes the response.
func (c *Client) info(ctx context.Context, payload map[string]any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.apiURL+"/info", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if os.Getenv("HL_DEBUG") == "1" {
		fmt.Fprintf(os.Stderr, "[hl-debug] POST /info  body=%s\n", body)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		buf, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("hl %d: %s (sent: %s)", resp.StatusCode, string(buf), body)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// ============================================================================
// Universe (perps meta) — coin list, decimals, max leverage
// ============================================================================

type UniverseAsset struct {
	Name        string `json:"name"`
	SzDecimals  int    `json:"szDecimals"`
	MaxLeverage int    `json:"maxLeverage"`
	OnlyPerp    bool   `json:"onlyPerp"`
}

type Meta struct {
	Universe []UniverseAsset `json:"universe"`
}

func (c *Client) Meta(ctx context.Context) (*Meta, error) {
	var out Meta
	if err := c.info(ctx, map[string]any{"type": "meta"}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ============================================================================
// All mids — coin → mark price snapshot
// ============================================================================

func (c *Client) AllMids(ctx context.Context) (map[string]string, error) {
	var out map[string]string
	if err := c.info(ctx, map[string]any{"type": "allMids"}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ============================================================================
// Candles — for OHLC 1m / 5m / 15m / etc.
// ============================================================================
// interval: "1m" | "5m" | "15m" | "1h" | "4h" | "1d"
//
// HL quirk: o/h/l/c/v come back as STRINGS, not numbers. We declare
// them as string and parse on access, or use json.Number for delayed
// parsing. We go with explicit string + custom unmarshaller so the
// conversion is centralised.

type Candle struct {
	T  int64  `json:"t"`   // open time ms
	T2 int64  `json:"T"`   // close time ms
	S  string `json:"s"`   // coin
	I  string `json:"i"`   // interval
	O  string `json:"o"`   // open (string in HL)
	C  string `json:"c"`
	H  string `json:"h"`
	L  string `json:"l"`
	V  string `json:"v"`
	N  int    `json:"n"`   // num trades
}

// Convenience accessors — return 0 on parse failure.
func (c Candle) Open() float64  { return parseF64(c.O) }
func (c Candle) Close() float64 { return parseF64(c.C) }
func (c Candle) High() float64  { return parseF64(c.H) }
func (c Candle) Low() float64   { return parseF64(c.L) }
func (c Candle) Volume() float64 { return parseF64(c.V) }

func parseF64(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func (c *Client) Candles(ctx context.Context, coin, interval string, startTimeMs int64) ([]Candle, error) {
	// HL requires BOTH startTime and endTime in the candleSnapshot
	// request. We add a 7-day lookback window by default; callers
	// that need a specific window can pass a custom endTime via
	// CandlesRange.
	endTime := startTimeMs + 7*24*3600*1000
	if endTime < time.Now().UnixMilli() {
		endTime = time.Now().UnixMilli()
	}
	return c.CandlesRange(ctx, coin, interval, startTimeMs, endTime)
}

// CandlesRange allows the caller to specify the exact window. Useful for
// backtest (historical slices) and for screener 24h lookbacks.
func (c *Client) CandlesRange(ctx context.Context, coin, interval string, startTimeMs, endTimeMs int64) ([]Candle, error) {
	var out []Candle
	payload := map[string]any{
		"type": "candleSnapshot",
		"req": map[string]any{
			"coin":      coin,
			"interval":  interval,
			"startTime": startTimeMs,
			"endTime":   endTimeMs,
		},
	}
	if err := c.info(ctx, payload, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ============================================================================
// Funding history
// ============================================================================
//
// HL returns funding as a string ("0.0001") and a premium. We keep
// them as strings and parse on access for consistency with the rest
// of the API (which is almost entirely stringified numbers).

type Funding struct {
	Coin        string `json:"coin"`
	FundingRate string `json:"fundingRate"`
	Premium     string `json:"premium"`
	Time        int64  `json:"time"`
}

func (f Funding) Rate() float64 {
	x, _ := strconv.ParseFloat(f.FundingRate, 64)
	return x
}

func (c *Client) FundingHistory(ctx context.Context, coin string, startTimeMs int64) ([]Funding, error) {
	var out []Funding
	payload := map[string]any{
		"type": "fundingHistory",
		"coin": coin,
		"startTime": startTimeMs,
	}
	if err := c.info(ctx, payload, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ============================================================================
// Open interest (per-coin snapshot)
// ============================================================================
//
// HL doesn't expose a per-coin openInterest endpoint on /info.
// We use the per-coin `metaAndAssetCtxs` shape — but for screener
// efficiency, prefer AssetContexts() which returns ALL coins in one
// call.
//
// OpenInterest(coin) here is implemented via AssetContexts() with a
// filter, so the per-coin API stays simple.

type OpenInterest struct {
	Coin        string  `json:"coin"`
	OpenInterest float64 `json:"openInterest"`
	Time        int64   `json:"time"`
}

func (c *Client) OpenInterest(ctx context.Context, coin string) (*OpenInterest, error) {
	// HL's per-coin "openInterest" endpoint is unreliable (returns
	// 422 on most calls). Go straight to AssetContexts and filter.
	ctxs, err := c.AssetContexts(ctx)
	if err != nil {
		return nil, err
	}
	for i, a := range ctxs.Universe {
		if a.Name == coin {
			return &OpenInterest{
				Coin:         coin,
				OpenInterest: ctxs.Contexts[i].OI(),
				Time:         time.Now().UnixMilli(),
			}, nil
		}
	}
	return nil, fmt.Errorf("coin %s not in asset contexts", coin)
}

// ============================================================================
// AssetContexts — full perp universe + per-coin market context in one call.
// This is the most efficient way to read OI/funding/markPx for screener.
// ============================================================================

type AssetCtx struct {
	Funding             string   `json:"funding"`           // 8h funding rate (string)
	MarkPx              string   `json:"markPx"`            // current mark
	OraclePx            string   `json:"oraclePx"`
	MidPx               string   `json:"midPx"`
	OpenInterest        string   `json:"openInterest"`      // in contracts
	Premium             string   `json:"premium"`
	PrevDayPx           string   `json:"prevDayPx"`
	DayBaseVlm          string   `json:"dayBaseVlm"`
	DayNtlVlm           string   `json:"dayNtlVlm"`
	ImpactPxs           []string `json:"impactPxs"`         // [bid, ask]
	FundingTimeHours    float64  `json:"-"`
}

func (c AssetCtx) Mark() float64           { f, _ := strconv.ParseFloat(c.MarkPx, 64); return f }
func (c AssetCtx) OI() float64             { f, _ := strconv.ParseFloat(c.OpenInterest, 64); return f }
func (c AssetCtx) DayNtlVlmFloat() float64 { f, _ := strconv.ParseFloat(c.DayNtlVlm, 64); return f }
func (c AssetCtx) PrevDay() float64        { f, _ := strconv.ParseFloat(c.PrevDayPx, 64); return f }
func (c AssetCtx) FundingTimeMs() int64 {
	// HL doesn't return funding time; approximate as 1h ahead (HL
	// settles hourly on most pairs).
	return time.Now().UnixMilli() + int64(c.FundingTimeHours*3600*1000)
}

type AssetContexts struct {
	Universe []UniverseAsset
	Contexts []AssetCtx
}

func (c *Client) AssetContexts(ctx context.Context) (*AssetContexts, error) {
	// Response: [meta, [ctx, ctx, ...]]
	var raw []json.RawMessage
	if err := c.info(ctx, map[string]any{"type": "metaAndAssetCtxs"}, &raw); err != nil {
		return nil, err
	}
	if len(raw) < 2 {
		return nil, fmt.Errorf("metaAndAssetCtxs: short response (len=%d)", len(raw))
	}
	var meta Meta
	if err := json.Unmarshal(raw[0], &meta); err != nil {
		return nil, fmt.Errorf("metaAndAssetCtxs meta: %w", err)
	}
	var ctxs []AssetCtx
	if err := json.Unmarshal(raw[1], &ctxs); err != nil {
		return nil, fmt.Errorf("metaAndAssetCtxs ctxs: %w", err)
	}
	return &AssetContexts{Universe: meta.Universe, Contexts: ctxs}, nil
}

// ============================================================================
// Recent trades (for CVD recorder, pagination via start time)
// ============================================================================
//
// HL quirk (same as Candle): px/sz come back as STRINGS. Kept as
// string fields with accessors so both the REST recentTrades and the
// WS trades channel decode correctly against real data.

type Trade struct {
	Coin  string    `json:"coin"`
	Side  string    `json:"side"` // "B" (aggressive buy) or "A" (aggressive sell)
	Px    string    `json:"px"`
	Sz    string    `json:"sz"`
	Hash  string    `json:"hash"`
	Tid   int64     `json:"tid"`
	Users [2]string `json:"users"`
	Time  int64     `json:"time"`
}

func (t Trade) Price() float64 { f, _ := strconv.ParseFloat(t.Px, 64); return f }
func (t Trade) Size() float64  { f, _ := strconv.ParseFloat(t.Sz, 64); return f }

// RecentTrades returns up to ~2000 most recent trades for a coin.
func (c *Client) RecentTrades(ctx context.Context, coin string) ([]Trade, error) {
	var out []Trade
	payload := map[string]any{
		"type": "recentTrades",
		"coin": coin,
	}
	if err := c.info(ctx, payload, &out); err != nil {
		return nil, err
	}
	return out, nil
}
