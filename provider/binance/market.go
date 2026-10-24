package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PremiumIndex is one symbol's funding/mark state. LastFundingRate is the rate
// charged at the next settlement (every 8 hours on Binance), not an hourly
// rate; callers must normalize before comparing venues.
type PremiumIndex struct {
	Symbol          string
	MarkPrice       float64
	IndexPrice      float64
	LastFundingRate float64
	NextFundingTime int64 // ms
	Time            int64 // ms
}

// OpenInterest is the current open interest for one symbol.
type OpenInterest struct {
	Symbol       string
	OpenInterest float64 // base units
	Time         int64   // ms
}

// FundingPoint is one historical settlement.
type FundingPoint struct {
	Symbol      string
	FundingRate float64
	FundingTime int64 // ms
}

// PremiumIndexAll returns every listed USD-M symbol's funding/mark state in one
// request. It is the cheapest way to keep a cross-venue view current: one call
// covers the whole market instead of one call per symbol.
func (c *Client) PremiumIndexAll(ctx context.Context) (map[string]PremiumIndex, error) {
	var raw []struct {
		Symbol          string `json:"symbol"`
		MarkPrice       string `json:"markPrice"`
		IndexPrice      string `json:"indexPrice"`
		LastFundingRate string `json:"lastFundingRate"`
		NextFundingTime int64  `json:"nextFundingTime"`
		Time            int64  `json:"time"`
	}
	if err := c.getJSON(ctx, "/fapi/v1/premiumIndex", nil, &raw); err != nil {
		return nil, err
	}

	result := make(map[string]PremiumIndex, len(raw))
	for _, item := range raw {
		symbol := strings.TrimSpace(item.Symbol)
		if symbol == "" {
			continue
		}
		result[symbol] = PremiumIndex{
			Symbol:          symbol,
			MarkPrice:       parseFloat(item.MarkPrice),
			IndexPrice:      parseFloat(item.IndexPrice),
			LastFundingRate: parseFloat(item.LastFundingRate),
			NextFundingTime: item.NextFundingTime,
			Time:            item.Time,
		}
	}
	return result, nil
}

// OpenInterest returns the current open interest for one symbol. Binance has no
// all-symbol OI endpoint, so this is the one per-symbol call in the polling
// loop.
func (c *Client) OpenInterest(ctx context.Context, symbol string) (*OpenInterest, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return nil, fmt.Errorf("symbol is required")
	}

	params := url.Values{"symbol": {symbol}}
	var raw struct {
		Symbol       string `json:"symbol"`
		OpenInterest string `json:"openInterest"`
		Time         int64  `json:"time"`
	}
	if err := c.getJSON(ctx, "/fapi/v1/openInterest", params, &raw); err != nil {
		return nil, err
	}
	return &OpenInterest{
		Symbol:       raw.Symbol,
		OpenInterest: parseFloat(raw.OpenInterest),
		Time:         raw.Time,
	}, nil
}

// FundingRateHistory returns the most recent settlements, newest last.
func (c *Client) FundingRateHistory(ctx context.Context, symbol string, limit int) ([]FundingPoint, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return nil, fmt.Errorf("symbol is required")
	}
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}

	params := url.Values{
		"symbol": {symbol},
		"limit":  {strconv.Itoa(limit)},
	}
	var raw []struct {
		Symbol      string `json:"symbol"`
		FundingRate string `json:"fundingRate"`
		FundingTime int64  `json:"fundingTime"`
	}
	if err := c.getJSON(ctx, "/fapi/v1/fundingRate", params, &raw); err != nil {
		return nil, err
	}

	points := make([]FundingPoint, 0, len(raw))
	for _, item := range raw {
		points = append(points, FundingPoint{
			Symbol:      item.Symbol,
			FundingRate: parseFloat(item.FundingRate),
			FundingTime: item.FundingTime,
		})
	}
	return points, nil
}

// OIPoint is one historical open-interest observation.
type OIPoint struct {
	TimestampMs int64
	OIBase      float64
	OIUSD       float64
}

// PricePoint is one historical candle close.
type PricePoint struct {
	TimestampMs int64
	Close       float64
}

// OpenInterestHist returns historical open interest (5m..1d periods, last 30
// days). It is free and keyless, and lets a freshly started process derive OI
// deltas without waiting an hour for its own snapshots.
func (c *Client) OpenInterestHist(ctx context.Context, symbol, period string, limit int) ([]OIPoint, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return nil, fmt.Errorf("symbol is required")
	}
	if period == "" {
		period = "1h"
	}
	if limit <= 0 || limit > 500 {
		limit = 500
	}

	params := url.Values{
		"symbol": {symbol},
		"period": {period},
		"limit":  {strconv.Itoa(limit)},
	}
	var raw []struct {
		Symbol               string `json:"symbol"`
		SumOpenInterest      string `json:"sumOpenInterest"`
		SumOpenInterestValue string `json:"sumOpenInterestValue"`
		Timestamp            int64  `json:"timestamp"`
	}
	if err := c.getJSON(ctx, "/futures/data/openInterestHist", params, &raw); err != nil {
		return nil, err
	}

	points := make([]OIPoint, 0, len(raw))
	for _, item := range raw {
		if item.Timestamp <= 0 {
			continue
		}
		points = append(points, OIPoint{
			TimestampMs: item.Timestamp,
			OIBase:      parseFloat(item.SumOpenInterest),
			OIUSD:       parseFloat(item.SumOpenInterestValue),
		})
	}
	return points, nil
}

// PriceHistory returns candle closes for a symbol. Used to seed the local price
// ranking history on startup.
func (c *Client) PriceHistory(ctx context.Context, symbol, interval string, limit int) ([]PricePoint, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return nil, fmt.Errorf("symbol is required")
	}
	if interval == "" {
		interval = "1h"
	}
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}

	params := url.Values{
		"symbol":   {symbol},
		"interval": {interval},
		"limit":    {strconv.Itoa(limit)},
	}
	var raw [][]json.RawMessage
	if err := c.getJSON(ctx, "/fapi/v1/klines", params, &raw); err != nil {
		return nil, err
	}

	points := make([]PricePoint, 0, len(raw))
	for _, row := range raw {
		if len(row) < 5 {
			continue
		}
		var openTime int64
		if err := json.Unmarshal(row[0], &openTime); err != nil || openTime <= 0 {
			continue
		}
		var closePrice string
		if err := json.Unmarshal(row[4], &closePrice); err != nil {
			continue
		}
		if price := parseFloat(closePrice); price > 0 {
			points = append(points, PricePoint{TimestampMs: openTime, Close: price})
		}
	}
	return points, nil
}

// FundingInterval is how often Binance settles USD-M funding. It is a constant
// of the venue; exposing it keeps callers from hard-coding 8 in a normalization
// formula without a name.
const FundingInterval = 8 * time.Hour
