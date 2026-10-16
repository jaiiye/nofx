package hyperliquid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// AssetCtx is the venue's per-asset context: funding, open interest, premium
// against the oracle price, and 24h traded notional.
type AssetCtx struct {
	Funding      float64 // funding rate charged per hour (fraction, signed)
	OpenInterest float64 // open interest in base units
	PrevDayPx    float64
	DayNtlVlm    float64 // 24h notional volume in USD
	Premium      float64 // mark vs oracle (fraction)
	MarkPx       float64
	MidPx        float64
	OraclePx     float64
}

// GetAssetContexts returns every asset's context for one perp dex.
//
// dex "" is the default perp dex (crypto); "xyz" is the HIP-3 dex holding
// stocks, commodities and indices, which Binance cannot serve at all. One
// request covers every asset, so callers should cache the result rather than
// calling per symbol. Keys are the venue's own names: "BTC" on the default dex,
// "xyz:SILVER" on the HIP-3 dex.
func (c *Client) GetAssetContexts(ctx context.Context, dex string) (map[string]AssetCtx, error) {
	payload := map[string]any{"type": "metaAndAssetCtxs"}
	if dex != "" {
		payload["dex"] = dex
	}

	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metaAndAssetCtxs error (status %d): %s", resp.StatusCode, string(body))
	}

	// The response is a two-element array: [meta, contexts], aligned by index.
	var raw []json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil || len(raw) != 2 {
		return nil, fmt.Errorf("unexpected metaAndAssetCtxs shape (body prefix: %.120s)", string(body))
	}

	var meta struct {
		Universe []struct {
			Name string `json:"name"`
		} `json:"universe"`
	}
	var contexts []struct {
		Funding      string `json:"funding"`
		OpenInterest string `json:"openInterest"`
		PrevDayPx    string `json:"prevDayPx"`
		DayNtlVlm    string `json:"dayNtlVlm"`
		Premium      string `json:"premium"`
		OraclePx     string `json:"oraclePx"`
		MarkPx       string `json:"markPx"`
		MidPx        string `json:"midPx"`
	}
	if err := json.Unmarshal(raw[0], &meta); err != nil {
		return nil, fmt.Errorf("failed to parse meta: %w", err)
	}
	if err := json.Unmarshal(raw[1], &contexts); err != nil {
		return nil, fmt.Errorf("failed to parse asset contexts: %w", err)
	}

	out := make(map[string]AssetCtx, len(meta.Universe))
	for i := range meta.Universe {
		if i >= len(contexts) {
			break
		}
		name := strings.TrimSpace(meta.Universe[i].Name)
		if name == "" {
			continue
		}
		out[name] = AssetCtx{
			Funding:      ParseVenueNumber(contexts[i].Funding),
			OpenInterest: ParseVenueNumber(contexts[i].OpenInterest),
			PrevDayPx:    ParseVenueNumber(contexts[i].PrevDayPx),
			DayNtlVlm:    ParseVenueNumber(contexts[i].DayNtlVlm),
			Premium:      ParseVenueNumber(contexts[i].Premium),
			MarkPx:       ParseVenueNumber(contexts[i].MarkPx),
			MidPx:        ParseVenueNumber(contexts[i].MidPx),
			OraclePx:     ParseVenueNumber(contexts[i].OraclePx),
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("metaAndAssetCtxs returned no assets (dex=%q)", dex)
	}
	return out, nil
}

// ParseVenueNumber parses the venue's numeric strings, which may carry
// thousands separators.
func ParseVenueNumber(s string) float64 {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", ""))
	if s == "" {
		return 0
	}
	v, _ := strconv.ParseFloat(s, 64)
	return v
}
