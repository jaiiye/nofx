package hyperliquid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// AssetContext is the per-asset market context returned by the Hyperliquid
// info API `metaAndAssetCtxs` endpoint, with numeric fields pre-parsed.
type AssetContext struct {
	Coin         string
	MarkPrice    float64
	OraclePrice  float64
	PrevDayPrice float64
	FundingRate  float64 // hourly funding rate, as a decimal (0.0000125 = 0.00125%)
	OpenInterest float64 // open interest in coin units
	DayVolumeUSD float64 // 24h notional volume in USD
}

// assetContextPayload mirrors the raw JSON field names so decoding stays local
// to this file.
type assetContextPayload struct {
	Funding      string `json:"funding"`
	OpenInterest string `json:"openInterest"`
	PrevDayPx    string `json:"prevDayPx"`
	DayNtlVlm    string `json:"dayNtlVlm"`
	OraclePx     string `json:"oraclePx"`
	MarkPx       string `json:"markPx"`
}

type assetMetaPayload struct {
	Universe []struct {
		Name       string `json:"name"`
		IsDelisted bool   `json:"isDelisted"`
	} `json:"universe"`
}

// FetchAssetContexts returns the current market context for every default
// (non-xyz) Hyperliquid perp, keyed by coin name (e.g. "BTC").
//
// This is the native replacement for the CEX-specific open-interest and
// funding-rate endpoints that were removed along with the multi-exchange
// support.
func FetchAssetContexts() (map[string]AssetContext, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	reqBody, err := json.Marshal(map[string]string{"type": "metaAndAssetCtxs"})
	if err != nil {
		return nil, fmt.Errorf("failed to encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, MainnetAPIURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch asset contexts: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Hyperliquid API returned status %d", resp.StatusCode)
	}

	var raw []json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	if len(raw) < 2 {
		return nil, fmt.Errorf("unexpected response format (%d parts)", len(raw))
	}

	var meta assetMetaPayload
	if err := json.Unmarshal(raw[0], &meta); err != nil {
		return nil, fmt.Errorf("failed to parse meta: %w", err)
	}

	var ctxs []assetContextPayload
	if err := json.Unmarshal(raw[1], &ctxs); err != nil {
		return nil, fmt.Errorf("failed to parse asset contexts: %w", err)
	}

	out := make(map[string]AssetContext, len(meta.Universe))
	for i, asset := range meta.Universe {
		if i >= len(ctxs) || asset.IsDelisted {
			continue
		}
		ctx := ctxs[i]
		out[asset.Name] = AssetContext{
			Coin:         asset.Name,
			MarkPrice:    parseAssetFloat(ctx.MarkPx),
			OraclePrice:  parseAssetFloat(ctx.OraclePx),
			PrevDayPrice: parseAssetFloat(ctx.PrevDayPx),
			FundingRate:  parseAssetFloat(ctx.Funding),
			OpenInterest: parseAssetFloat(ctx.OpenInterest),
			DayVolumeUSD: parseAssetFloat(ctx.DayNtlVlm),
		}
	}

	return out, nil
}

// CoinNameFromSymbol converts an internal trading symbol into the Hyperliquid
// coin name (e.g. "BTCUSDT" -> "BTC", "ETH-USDC" -> "ETH").
func CoinNameFromSymbol(symbol string) string {
	return NormalizeCoinBase(strings.TrimSpace(symbol))
}

// Ticker24h is a lightweight 24-hour price summary for one Hyperliquid perp.
type Ticker24h struct {
	Symbol       string  // normalized symbol, e.g. "BTCUSDT"
	LastPrice    float64 // current mark price
	QuoteVolume  float64 // 24h notional volume in USD
	PriceChange  float64 // 24h absolute price change
	ChangePct    float64 // 24h change in percent
	OpenInterest float64 // open interest notional in USD
}

// FetchTicker24h returns the 24-hour summary for a single symbol, sourced from
// the Hyperliquid native info API. This replaces the per-exchange ticker calls
// that were removed along with multi-exchange support.
func FetchTicker24h(symbol string) (*Ticker24h, error) {
	coin := CoinNameFromSymbol(symbol)
	contexts, err := FetchAssetContexts()
	if err != nil {
		return nil, err
	}

	asset, ok := contexts[coin]
	if !ok {
		return nil, fmt.Errorf("no Hyperliquid market for %s", coin)
	}

	price := asset.MarkPrice
	if price <= 0 {
		price = asset.OraclePrice
	}

	changePct := 0.0
	change := 0.0
	if asset.PrevDayPrice > 0 && price > 0 {
		change = price - asset.PrevDayPrice
		changePct = change / asset.PrevDayPrice * 100
	}

	return &Ticker24h{
		Symbol:       NormalizeCoinBase(coin) + "USDT",
		LastPrice:    price,
		QuoteVolume:  asset.DayVolumeUSD,
		PriceChange:  change,
		ChangePct:    changePct,
		OpenInterest: asset.OpenInterest * price,
	}, nil
}

func parseAssetFloat(raw string) float64 {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0
	}
	value, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return 0
	}
	return value
}
