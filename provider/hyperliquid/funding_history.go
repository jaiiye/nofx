package hyperliquid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// FundingPoint is one hourly funding observation for a coin.
type FundingPoint struct {
	TimeMs int64
	Rate   float64 // hourly funding rate as a fraction of notional, signed
}

// GetFundingHistory returns the venue's hourly funding for a coin from startMs
// onwards (all entries when startMs <= 0).
//
// This is the baseline for judging whether the current funding is crowded
// relative to the coin's own recent distribution. Hyperliquid charges funding
// hourly on the long/short imbalance, so its level and its percentile are the
// venue's own positioning signal — and the only one that also exists for the
// HIP-3 stock/commodity perps.
func (c *Client) GetFundingHistory(ctx context.Context, coin string, startMs int64) ([]FundingPoint, error) {
	coin = strings.TrimSpace(coin)
	if coin == "" {
		return nil, fmt.Errorf("coin is required")
	}

	payload := map[string]any{"type": "fundingHistory", "coin": coin}
	if startMs > 0 {
		payload["startTime"] = startMs
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
		return nil, fmt.Errorf("fundingHistory error (status %d): %s", resp.StatusCode, string(body))
	}

	var raw []struct {
		FundingRate string `json:"fundingRate"`
		Time        int64  `json:"time"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse fundingHistory: %w", err)
	}

	points := make([]FundingPoint, 0, len(raw))
	for _, p := range raw {
		if p.Time <= 0 {
			continue
		}
		points = append(points, FundingPoint{TimeMs: p.Time, Rate: ParseVenueNumber(p.FundingRate)})
	}
	return points, nil
}
