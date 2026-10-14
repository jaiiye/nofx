package hyperliquid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// BookLevel is one price level of the Hyperliquid L2 book.
type BookLevel struct {
	Px string `json:"px"`
	Sz string `json:"sz"`
	N  int    `json:"n"`
}

// L2BookResponse is the l2Book info response: levels[0] = bids (best first),
// levels[1] = asks (best first).
type L2BookResponse struct {
	Coin   string         `json:"coin"`
	Time   int64          `json:"time"`
	Levels [2][]BookLevel `json:"levels"`
}

// GetL2Book fetches the L2 order book snapshot for a coin. The coin is run
// through FormatCoinForAPI, so xyz dex assets (stocks, forex, commodities)
// are addressed the same way as GetCandles.
func (c *Client) GetL2Book(ctx context.Context, coin string) (*L2BookResponse, error) {
	reqBody := map[string]any{
		"type": "l2Book",
		"coin": FormatCoinForAPI(coin),
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.apiURL, bytes.NewBuffer(jsonBody))
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
		return nil, fmt.Errorf("hyperliquid API error (status %d): %s", resp.StatusCode, string(body))
	}

	var book L2BookResponse
	if err := json.Unmarshal(body, &book); err != nil {
		return nil, fmt.Errorf("failed to parse l2Book response: %w (body: %s)", err, string(body))
	}
	if len(book.Levels) != 2 || len(book.Levels[0]) == 0 || len(book.Levels[1]) == 0 {
		return nil, fmt.Errorf("l2Book for %s is empty", coin)
	}
	return &book, nil
}
