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
	"time"
)

// ClearinghouseState is one account's perp state: the margin summary plus every
// open position with the two prices a cost/liquidation profile needs. The venue
// reports `liquidationPx` directly, so no maintenance-margin maths is involved.
type ClearinghouseState struct {
	AssetPositions []AssetPosition `json:"assetPositions"`
	MarginSummary  MarginSummary   `json:"marginSummary"`
	Time           int64           `json:"time"`
}

// AssetPosition wraps one position (the venue nests it one level deep).
type AssetPosition struct {
	Position Position `json:"position"`
}

// Position is a single open perp position.
type Position struct {
	Coin          string `json:"coin"`
	Size          string `json:"szi"`
	EntryPx       string `json:"entryPx"`
	PositionValue string `json:"positionValue"`
	UnrealizedPnl string `json:"unrealizedPnl"`
	LiquidationPx string `json:"liquidationPx"`
	Leverage      struct {
		Type  string `json:"type"`
		Value int    `json:"value"`
	} `json:"leverage"`
}

// MarginSummary is the account-level margin roll-up.
type MarginSummary struct {
	AccountValue    string `json:"accountValue"`
	TotalMarginUsed string `json:"totalMarginUsed"`
}

// clearinghouseStateURL is the endpoint the position index reads. It is a
// variable so tests can point it at a local server.
var clearinghouseStateURL = MainnetAPIURL

// GetClearinghouseState fetches one account's perp state. `dex` selects a
// builder-deployed (HIP-3) dex such as "xyz"; an empty dex means the core perps.
// This is the only read path the position index needs, and it is called for
// arbitrary addresses (not just our own wallet), so it lives here rather than in
// an adapter.
func GetClearinghouseState(ctx context.Context, user, dex string) (*ClearinghouseState, error) {
	user = strings.TrimSpace(user)
	if user == "" {
		return nil, fmt.Errorf("user address is required")
	}

	payload := map[string]any{"type": "clearinghouseState", "user": user}
	if dex = strings.TrimSpace(dex); dex != "" {
		payload["dex"] = dex
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to encode clearinghouseState request: %w", err)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, clearinghouseStateURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to build clearinghouseState request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("clearinghouseState request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("failed to read clearinghouseState response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clearinghouseState returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var state ClearinghouseState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("failed to decode clearinghouseState response: %w", err)
	}
	return &state, nil
}

// StringFloat parses the venue's string-encoded numbers, defaulting to 0 so a
// malformed field degrades a single value instead of failing the whole account.
func StringFloat(raw string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0
	}
	return v
}
