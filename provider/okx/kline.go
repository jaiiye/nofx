// Package okx provides the public swap market-data surface nofx needs:
// candles for the venue a trader actually executes on. It does not touch
// account or order endpoints — trading lives in trader/okx.
package okx

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultBaseURL = "https://www.okx.com"
	maxCandles     = 300
)

type Client struct {
	httpClient *http.Client
	baseURL    string
}

func NewClient() *Client {
	return NewClientWithBaseURL(DefaultBaseURL)
}

func NewClientWithBaseURL(baseURL string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 20 * time.Second},
		baseURL:    strings.TrimRight(baseURL, "/"),
	}
}

// Kline is one public candle.
type Kline struct {
	OpenTime  int64
	CloseTime int64
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    float64
	Turnover  float64
	Confirmed bool
}

// MapBar converts our timeframe to OKX's bar string. OKX has no 8h interval.
func MapBar(timeframe string) (string, bool) {
	switch timeframe {
	case "1m":
		return "1m", true
	case "3m":
		return "3m", true
	case "5m":
		return "5m", true
	case "15m":
		return "15m", true
	case "30m":
		return "30m", true
	case "1h":
		return "1H", true
	case "2h":
		return "2H", true
	case "4h":
		return "4H", true
	case "6h":
		return "6H", true
	case "12h":
		return "12H", true
	case "1d":
		return "1D", true
	case "3d":
		return "3D", true
	case "1w":
		return "1W", true
	case "1M":
		return "1M", true
	default:
		return "", false
	}
}

// SwapSymbol converts BTCUSDT to OKX's BTC-USDT-SWAP.
func SwapSymbol(symbol string) string {
	upper := strings.ToUpper(strings.TrimSpace(symbol))
	if strings.HasSuffix(upper, "-SWAP") {
		return upper
	}
	if strings.HasSuffix(upper, "USDT") {
		return strings.TrimSuffix(upper, "USDT") + "-USDT-SWAP"
	}
	return upper
}

// Klines fetches the most recent candles.
func (c *Client) Klines(ctx context.Context, symbol, timeframe string, limit int) ([]Kline, error) {
	if limit <= 0 || limit > maxCandles {
		limit = maxCandles
	}
	return c.candles(ctx, symbol, timeframe, 0, limit)
}

// KlinesRange walks a range backwards using OKX's `after` cursor.
func (c *Client) KlinesRange(ctx context.Context, symbol, timeframe string, startMs, endMs int64) ([]Kline, error) {
	if startMs <= 0 || endMs <= startMs {
		return nil, fmt.Errorf("invalid candle range: start=%d end=%d", startMs, endMs)
	}
	var out []Kline
	cursor := endMs
	for cursor > startMs {
		batch, err := c.candles(ctx, symbol, timeframe, cursor, maxCandles)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		out = append(batch, out...)
		oldest := batch[0].OpenTime
		if len(batch) < maxCandles || oldest <= startMs {
			break
		}
		cursor = oldest
	}
	return out, nil
}

// candles requests one page. afterMs is an exclusive upper bound (OKX returns
// rows earlier than it); 0 means the newest page.
func (c *Client) candles(ctx context.Context, symbol, timeframe string, afterMs int64, limit int) ([]Kline, error) {
	bar, ok := MapBar(timeframe)
	if !ok {
		return nil, fmt.Errorf("okx does not support timeframe %q", timeframe)
	}
	params := url.Values{
		"instId": {SwapSymbol(symbol)},
		"bar":    {bar},
		"limit":  {strconv.Itoa(limit)},
	}
	if afterMs > 0 {
		params.Set("after", strconv.FormatInt(afterMs, 10))
	}

	endpoint := c.baseURL + "/api/v5/market/candles?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("okx candles failed (status %d): %s", resp.StatusCode, truncate(string(body), 200))
	}

	var envelope struct {
		Code string     `json:"code"`
		Msg  string     `json:"msg"`
		Data [][]string `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("failed to parse candles: %w", err)
	}
	if envelope.Code != "0" {
		return nil, fmt.Errorf("okx candles rejected (code %s): %s", envelope.Code, envelope.Msg)
	}

	klines := make([]Kline, 0, len(envelope.Data))
	for i := len(envelope.Data) - 1; i >= 0; i-- {
		row := envelope.Data[i]
		if len(row) < 6 {
			continue
		}
		openTime, err := strconv.ParseInt(row[0], 10, 64)
		if err != nil || openTime <= 0 {
			continue
		}
		volume := parseFloat(row[5])
		turnover := 0.0
		if len(row) > 7 {
			turnover = parseFloat(row[7])
		}
		confirmed := len(row) > 8 && row[8] == "1"
		klines = append(klines, Kline{
			OpenTime:  openTime,
			CloseTime: openTime + barMillis(bar) - 1,
			Open:      parseFloat(row[1]),
			High:      parseFloat(row[2]),
			Low:       parseFloat(row[3]),
			Close:     parseFloat(row[4]),
			Volume:    volume,
			Turnover:  turnover,
			Confirmed: confirmed,
		})
	}
	return klines, nil
}

func barMillis(bar string) int64 {
	switch bar {
	case "1m":
		return int64(time.Minute / time.Millisecond)
	case "3m":
		return int64(3 * time.Minute / time.Millisecond)
	case "5m":
		return int64(5 * time.Minute / time.Millisecond)
	case "15m":
		return int64(15 * time.Minute / time.Millisecond)
	case "30m":
		return int64(30 * time.Minute / time.Millisecond)
	case "1H":
		return int64(time.Hour / time.Millisecond)
	case "2H":
		return int64(2 * time.Hour / time.Millisecond)
	case "4H":
		return int64(4 * time.Hour / time.Millisecond)
	case "6H":
		return int64(6 * time.Hour / time.Millisecond)
	case "12H":
		return int64(12 * time.Hour / time.Millisecond)
	case "1D":
		return int64(24 * time.Hour / time.Millisecond)
	case "3D":
		return int64(72 * time.Hour / time.Millisecond)
	case "1W":
		return int64(7 * 24 * time.Hour / time.Millisecond)
	case "1M":
		return int64(30 * 24 * time.Hour / time.Millisecond)
	default:
		return int64(5 * time.Minute / time.Millisecond)
	}
}

func parseFloat(raw string) float64 {
	value, _ := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	return value
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
