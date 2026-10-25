package bybit

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Kline is one public candle. Turnover is the quote-currency volume.
type Kline struct {
	OpenTime  int64
	CloseTime int64
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    float64
	Turnover  float64
}

// mapInterval converts our timeframe to Bybit's minute-based interval string.
func mapInterval(timeframe string) (string, bool) {
	switch timeframe {
	case "1m":
		return "1", true
	case "3m":
		return "3", true
	case "5m":
		return "5", true
	case "15m":
		return "15", true
	case "30m":
		return "30", true
	case "1h":
		return "60", true
	case "2h":
		return "120", true
	case "4h":
		return "240", true
	case "6h":
		return "360", true
	case "12h":
		return "720", true
	case "1d":
		return "D", true
	case "1w":
		return "W", true
	case "1M":
		return "M", true
	default:
		return "", false
	}
}

// Klines fetches the most recent linear-contract candles.
func (c *Client) Klines(ctx context.Context, symbol, timeframe string, limit int) ([]Kline, error) {
	return c.klines(ctx, symbol, timeframe, 0, 0, limit)
}

// KlinesRange fetches candles in [startMs, endMs], paging backwards when the
// range exceeds the venue's 1000-bar response cap.
func (c *Client) KlinesRange(ctx context.Context, symbol, timeframe string, startMs, endMs int64) ([]Kline, error) {
	if startMs <= 0 || endMs <= startMs {
		return nil, fmt.Errorf("invalid candle range: start=%d end=%d", startMs, endMs)
	}
	var out []Kline
	cursorEnd := endMs
	for cursorEnd > startMs {
		batch, err := c.klines(ctx, symbol, timeframe, startMs, cursorEnd, 1000)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		out = append(batch, out...)
		oldest := batch[0].OpenTime
		if len(batch) < 1000 || oldest <= startMs {
			break
		}
		cursorEnd = oldest - 1
	}
	return out, nil
}

func (c *Client) klines(ctx context.Context, symbol, timeframe string, startMs, endMs int64, limit int) ([]Kline, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return nil, fmt.Errorf("symbol is required")
	}
	interval, ok := mapInterval(timeframe)
	if !ok {
		return nil, fmt.Errorf("bybit does not support timeframe %q", timeframe)
	}
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}

	params := url.Values{
		"category": {"linear"},
		"symbol":   {symbol},
		"interval": {interval},
		"limit":    {strconv.Itoa(limit)},
	}
	if startMs > 0 {
		params.Set("start", strconv.FormatInt(startMs, 10))
	}
	if endMs > 0 {
		params.Set("end", strconv.FormatInt(endMs, 10))
	}

	var result struct {
		List [][]string `json:"list"`
	}
	if err := c.get(ctx, "/v5/market/kline", params, &result); err != nil {
		return nil, err
	}

	// Bybit returns newest first; reverse to ascending.
	klines := make([]Kline, 0, len(result.List))
	for i := len(result.List) - 1; i >= 0; i-- {
		row := result.List[i]
		if len(row) < 7 {
			continue
		}
		openTime, err := strconv.ParseInt(row[0], 10, 64)
		if err != nil || openTime <= 0 {
			continue
		}
		klines = append(klines, Kline{
			OpenTime:  openTime,
			CloseTime: openTime + intervalMillis(interval) - 1,
			Open:      parseFloat(row[1]),
			High:      parseFloat(row[2]),
			Low:       parseFloat(row[3]),
			Close:     parseFloat(row[4]),
			Volume:    parseFloat(row[5]),
			Turnover:  parseFloat(row[6]),
		})
	}
	return klines, nil
}

func intervalMillis(interval string) int64 {
	if value, err := strconv.ParseInt(interval, 10, 64); err == nil {
		return value * int64(time.Minute/time.Millisecond)
	}
	switch interval {
	case "D":
		return int64(24 * time.Hour / time.Millisecond)
	case "W":
		return int64(7 * 24 * time.Hour / time.Millisecond)
	case "M":
		return int64(30 * 24 * time.Hour / time.Millisecond)
	default:
		return int64(5 * time.Minute / time.Millisecond)
	}
}
