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

// UserFeeRates are the account's actual fee rates (fractions of notional).
type UserFeeRates struct {
	Maker float64
	Taker float64
}

// GetUserFeeRates returns the fee rates the venue charges this account. They
// decide position sizing, and the static per-venue model can differ from the
// account's actual tier.
func (c *Client) GetUserFeeRates(ctx context.Context, user string) (*UserFeeRates, error) {
	if strings.TrimSpace(user) == "" {
		return nil, fmt.Errorf("wallet address is required")
	}

	jsonBody, err := json.Marshal(map[string]any{"type": "userFees", "user": user})
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
		return nil, fmt.Errorf("userFees error (status %d): %s", resp.StatusCode, string(body))
	}

	// The venue reports the taker rate as the cross-margin rate and the maker
	// rate as the "add liquidity" rate.
	var result struct {
		UserAddRate       string `json:"userAddRate"`
		UserCrossRate     string `json:"userCrossRate"`
		UserSpotCrossRate string `json:"userSpotCrossRate"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse userFees: %w", err)
	}

	taker := ParseVenueNumber(result.UserCrossRate)
	if taker == 0 {
		taker = ParseVenueNumber(result.UserSpotCrossRate)
	}
	return &UserFeeRates{
		Maker: ParseVenueNumber(result.UserAddRate),
		Taker: taker,
	}, nil
}
