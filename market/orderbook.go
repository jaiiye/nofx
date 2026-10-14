package market

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// OrderBookLevel is one price level of the venue order book.
type OrderBookLevel struct {
	Price float64
	Size  float64
}

// OrderBookDepth is a point-in-time snapshot of the decision-context order
// book.
//
// Data-source convention: the decision context is Binance-based — the same
// baseline as the k-lines in GetWithTimeframes — so this book is the Binance
// futures book regardless of the trader's execution venue. xyz dex assets
// (Hyperliquid) have no L2 fetch in the market layer yet; callers must treat
// a fetch error as "no microstructure for this symbol" and degrade gracefully.
type OrderBookDepth struct {
	Symbol    string
	Bids      []OrderBookLevel // best bid first
	Asks      []OrderBookLevel // best ask first
	FetchTime time.Time
}

// Mid returns the mid price of the best bid/ask (0 when one side is empty).
func (b *OrderBookDepth) Mid() float64 {
	if b == nil || len(b.Bids) == 0 || len(b.Asks) == 0 {
		return 0
	}
	return (b.Bids[0].Price + b.Asks[0].Price) / 2
}

// GetOrderBookDepth fetches the Binance futures order book (public endpoint,
// no signature). depth is the number of levels per side, capped at 100
// (Binance limit); the shared hook-aware client with its 30s timeout is
// reused, matching the other market-layer fetches.
func GetOrderBookDepth(symbol string, depth int) (*OrderBookDepth, error) {
	symbol = Normalize(symbol)
	if depth <= 0 {
		depth = 20
	}
	if depth > 100 {
		depth = 100
	}

	client := NewAPIClient()
	url := fmt.Sprintf("%s/fapi/v1/depth?symbol=%s&limit=%d", baseURL, symbol, depth)
	resp, err := client.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch order book for %s: %w", symbol, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read order book response for %s: %w", symbol, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("order book fetch for %s returned HTTP %d: %s", symbol, resp.StatusCode, string(body))
	}

	var raw struct {
		Bids [][2]string `json:"bids"`
		Asks [][2]string `json:"asks"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse order book for %s: %w", symbol, err)
	}
	if len(raw.Bids) == 0 || len(raw.Asks) == 0 {
		return nil, fmt.Errorf("order book for %s is empty", symbol)
	}

	book := &OrderBookDepth{
		Symbol:    symbol,
		Bids:      make([]OrderBookLevel, 0, len(raw.Bids)),
		Asks:      make([]OrderBookLevel, 0, len(raw.Asks)),
		FetchTime: time.Now(),
	}
	parseLevels := func(rows [][2]string) []OrderBookLevel {
		levels := make([]OrderBookLevel, 0, len(rows))
		for _, row := range rows {
			price, priceErr := strconv.ParseFloat(row[0], 64)
			size, sizeErr := strconv.ParseFloat(row[1], 64)
			if priceErr != nil || sizeErr != nil {
				continue
			}
			levels = append(levels, OrderBookLevel{Price: price, Size: size})
		}
		return levels
	}
	book.Bids = parseLevels(raw.Bids)
	book.Asks = parseLevels(raw.Asks)
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		return nil, fmt.Errorf("order book for %s has no parseable levels", symbol)
	}
	return book, nil
}
