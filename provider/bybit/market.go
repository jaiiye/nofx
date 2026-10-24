package bybit

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// Ticker is one linear contract's current market state. OpenInterest is in base
// units; OpenInterestValue is the venue's own USD valuation. FundingRate is the
// rate for the venue's settlement interval (usually 8h, but some contracts use
// 4h or 1h), so callers must divide by that interval before comparing venues.
type Ticker struct {
	Symbol            string
	LastPrice         float64
	MarkPrice         float64
	IndexPrice        float64
	Change24hPct      float64 // venue's price24hPcnt, already a percentage
	Turnover24h       float64 // quote-currency turnover over 24h
	OpenInterest      float64
	OpenInterestValue float64
	FundingRate       float64
	NextFundingTimeMs int64
}

// Instrument carries the settlement interval, which Bybit does not repeat in
// the ticker. It is fetched once at startup; the default assumption when a
// lookup fails is 8h, and the collector records that assumption on the row.
type Instrument struct {
	Symbol                 string
	FundingIntervalMinutes int
	ContractType           string
	Status                 string
}

const defaultFundingIntervalMinutes = 480

// Tickers returns every linear contract in one request. This is why Bybit is
// part of the cross-venue view: one call covers OI, funding and mark prices for
// the whole market, unlike Binance's per-symbol OI endpoint.
func (c *Client) Tickers(ctx context.Context) (map[string]Ticker, error) {
	params := url.Values{"category": {"linear"}}
	var result struct {
		Category string `json:"category"`
		List     []struct {
			Symbol            string `json:"symbol"`
			LastPrice         string `json:"lastPrice"`
			MarkPrice         string `json:"markPrice"`
			IndexPrice        string `json:"indexPrice"`
			Price24hPcnt      string `json:"price24hPcnt"`
			Turnover24h       string `json:"turnover24h"`
			OpenInterest      string `json:"openInterest"`
			OpenInterestValue string `json:"openInterestValue"`
			FundingRate       string `json:"fundingRate"`
			NextFundingTime   string `json:"nextFundingTime"`
		} `json:"list"`
	}
	if err := c.get(ctx, "/v5/market/tickers", params, &result); err != nil {
		return nil, err
	}

	tickers := make(map[string]Ticker, len(result.List))
	for _, item := range result.List {
		symbol := strings.ToUpper(strings.TrimSpace(item.Symbol))
		if symbol == "" {
			continue
		}
		tickers[symbol] = Ticker{
			Symbol:            symbol,
			LastPrice:         parseFloat(item.LastPrice),
			MarkPrice:         parseFloat(item.MarkPrice),
			IndexPrice:        parseFloat(item.IndexPrice),
			Change24hPct:      parseFloat(item.Price24hPcnt) * 100,
			Turnover24h:       parseFloat(item.Turnover24h),
			OpenInterest:      parseFloat(item.OpenInterest),
			OpenInterestValue: parseFloat(item.OpenInterestValue),
			FundingRate:       parseFloat(item.FundingRate),
			NextFundingTimeMs: parseUnixMilli(item.NextFundingTime),
		}
	}
	return tickers, nil
}

// InstrumentsInfo returns the settlement interval per symbol, following the
// cursor until the venue stops returning one.
func (c *Client) InstrumentsInfo(ctx context.Context) (map[string]Instrument, error) {
	instruments := make(map[string]Instrument)
	cursor := ""

	for page := 0; page < 20; page++ {
		params := url.Values{
			"category": {"linear"},
			"limit":    {"1000"},
		}
		if cursor != "" {
			params.Set("cursor", cursor)
		}

		var result struct {
			List []struct {
				Symbol            string      `json:"symbol"`
				ContractType      string      `json:"contractType"`
				Status            string      `json:"status"`
				FundingInterval   json.Number `json:"fundingInterval"`
				FundingIntervalMs int64       `json:"fundingIntervalMs"`
			} `json:"list"`
			NextPageCursor string `json:"nextPageCursor"`
		}
		if err := c.get(ctx, "/v5/market/instruments-info", params, &result); err != nil {
			return nil, err
		}
		if len(result.List) == 0 {
			break
		}

		for _, item := range result.List {
			symbol := strings.ToUpper(strings.TrimSpace(item.Symbol))
			if symbol == "" {
				continue
			}
			minutes := parseFundingIntervalMinutes(item.FundingInterval, item.FundingIntervalMs)
			if minutes <= 0 {
				minutes = defaultFundingIntervalMinutes
			}
			instruments[symbol] = Instrument{
				Symbol:                 symbol,
				FundingIntervalMinutes: minutes,
				ContractType:           item.ContractType,
				Status:                 item.Status,
			}
		}

		if result.NextPageCursor == "" || result.NextPageCursor == cursor {
			break
		}
		cursor = result.NextPageCursor
	}
	return instruments, nil
}

// parseFundingIntervalMinutes accepts both shapes the venue has used: a JSON
// number (current) and a quoted numeric string (older payloads / docs). The
// live API returns a number, which json.Number also accepts when quoted.
func parseFundingIntervalMinutes(raw json.Number, rawMs int64) int {
	if value, err := strconv.Atoi(strings.TrimSpace(raw.String())); err == nil && value > 0 {
		return value
	}
	if rawMs > 0 {
		return int(rawMs / 60000)
	}
	return 0
}

func parseUnixMilli(raw string) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value <= 0 {
		return 0
	}
	return value
}
