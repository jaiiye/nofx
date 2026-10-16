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

// LedgerUpdate is one non-funding ledger movement from the venue: deposits,
// withdrawals, transfers between accounts and vault movements. By definition it
// excludes funding, so summing its USDC gives the exact external cash flow.
type LedgerUpdate struct {
	Time int64
	Type string
	USDC float64
}

// GetLedgerUpdates returns non-funding ledger movements at or after sinceMs.
func (c *Client) GetLedgerUpdates(ctx context.Context, user string, sinceMs int64) ([]LedgerUpdate, error) {
	if strings.TrimSpace(user) == "" {
		return nil, fmt.Errorf("wallet address is required")
	}

	payload := map[string]any{
		"type":      "userNonFundingLedgerUpdates",
		"user":      user,
		"startTime": sinceMs,
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
		return nil, fmt.Errorf("userNonFundingLedgerUpdates error (status %d): %s", resp.StatusCode, string(body))
	}

	var raw []struct {
		Time  int64 `json:"time"`
		Delta struct {
			Type string `json:"type"`
			USDC string `json:"usdc"`
			Fee  string `json:"fee"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse ledger updates: %w", err)
	}

	updates := make([]LedgerUpdate, 0, len(raw))
	for _, entry := range raw {
		updates = append(updates, LedgerUpdate{
			Time: entry.Time,
			Type: entry.Delta.Type,
			USDC: ParseVenueNumber(entry.Delta.USDC),
		})
	}
	return updates, nil
}
