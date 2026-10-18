package market

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"nofx/provider/hyperliquid"
)

const (
	binanceFuturesKlinesURL = "https://fapi.binance.com/fapi/v1/klines"
	binanceMaxKlineLimit    = 1500
)

// GetKlinesRangeAuto fetches closed candles for a symbol, routing HIP-3 (xyz)
// assets to the venue's own candle snapshot and everything else to Binance.
// Analyses that compare a trade with a random-entry baseline need the price path
// of the venue the trade actually happened on — Binance has no stock/commodity
// history at all.
func GetKlinesRangeAuto(symbol, timeframe string, start, end time.Time) ([]Kline, error) {
	if hyperliquid.IsXYZAsset(symbol) {
		return getKlinesFromHyperliquidRange(symbol, timeframe, start, end)
	}
	return GetKlinesRange(symbol, timeframe, start, end)
}

// hyperliquidSnapshotLimit caps the bar count requested from the venue: its
// candle snapshot takes a count rather than a range.
const hyperliquidSnapshotLimit = 1000

// getKlinesFromHyperliquidRange requests enough recent candles to cover the
// window, then keeps only bars that closed inside it.
func getKlinesFromHyperliquidRange(symbol, timeframe string, start, end time.Time) ([]Kline, error) {
	if !end.After(start) {
		return nil, fmt.Errorf("end time must be after start time")
	}
	dur, err := TFDuration(timeframe)
	if err != nil {
		return nil, err
	}

	bars := int(end.Sub(start)/dur) + 10
	if bars < 10 {
		bars = 10
	}
	if bars > hyperliquidSnapshotLimit {
		bars = hyperliquidSnapshotLimit
	}

	klines, err := getKlinesFromHyperliquid(symbol, timeframe, bars)
	if err != nil {
		return nil, err
	}

	startMs, endMs := start.UnixMilli(), end.UnixMilli()
	out := make([]Kline, 0, len(klines))
	for _, k := range klines {
		if k.OpenTime >= startMs && k.CloseTime <= endMs {
			out = append(out, k)
		}
	}
	return out, nil
}

// GetKlinesRange fetches K-line series within specified time range (closed interval), returns data sorted by time in ascending order.
func GetKlinesRange(symbol string, timeframe string, start, end time.Time) ([]Kline, error) {
	symbol = Normalize(symbol)
	normTF, err := NormalizeTimeframe(timeframe)
	if err != nil {
		return nil, err
	}
	if !end.After(start) {
		return nil, fmt.Errorf("end time must be after start time")
	}

	startMs := start.UnixMilli()
	endMs := end.UnixMilli()

	var all []Kline
	cursor := startMs

	client := &http.Client{Timeout: 15 * time.Second}

	for cursor < endMs {
		req, err := http.NewRequest("GET", binanceFuturesKlinesURL, nil)
		if err != nil {
			return nil, err
		}

		q := req.URL.Query()
		q.Set("symbol", symbol)
		q.Set("interval", normTF)
		q.Set("limit", fmt.Sprintf("%d", binanceMaxKlineLimit))
		q.Set("startTime", fmt.Sprintf("%d", cursor))
		q.Set("endTime", fmt.Sprintf("%d", endMs))
		req.URL.RawQuery = q.Encode()

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("binance klines api returned status %d: %s", resp.StatusCode, string(body))
		}

		var raw [][]interface{}
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, err
		}
		if len(raw) == 0 {
			break
		}

		batch := make([]Kline, len(raw))
		for i, item := range raw {
			openTime := int64(item[0].(float64))
			open, _ := parseFloat(item[1])
			high, _ := parseFloat(item[2])
			low, _ := parseFloat(item[3])
			close, _ := parseFloat(item[4])
			volume, _ := parseFloat(item[5])
			closeTime := int64(item[6].(float64))

			batch[i] = Kline{
				OpenTime:  openTime,
				Open:      open,
				High:      high,
				Low:       low,
				Close:     close,
				Volume:    volume,
				CloseTime: closeTime,
			}
		}

		all = append(all, batch...)

		last := batch[len(batch)-1]
		cursor = last.CloseTime + 1

		// If returned quantity is less than request limit, reached the end, can exit early.
		if len(batch) < binanceMaxKlineLimit {
			break
		}
	}

	return all, nil
}
