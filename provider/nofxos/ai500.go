package nofxos

import (
	"sort"
	"strings"
)

// CoinData represents one entry of the AI-scored "AI500" board.
//
// The legacy nofxos.ai scoring service is unavailable in this build, so the
// board is derived locally: the top liquid Hyperliquid perps are ranked by a
// momentum score computed from 24h price change, turnover and open interest.
// The field semantics are preserved so existing consumers keep working:
//   - Score is normalised to 0-100.
//   - IncreasePercent is the 24h price change in percent (e.g. 4.2 = +4.2%).
//   - StartPrice is the current mark price; StartTime is the universe 24h base.
type CoinData struct {
	Pair            string  `json:"pair"`             // Trading pair symbol (e.g.: BTCUSDT)
	Score           float64 `json:"score"`            // Current AI score (0-100)
	StartTime       int64   `json:"start_time"`       // Ranking window start (Unix timestamp)
	StartPrice      float64 `json:"start_price"`      // Current mark price
	LastScore       float64 `json:"last_score"`       // Latest score
	MaxScore        float64 `json:"max_score"`        // Highest score in the window
	MaxPrice        float64 `json:"max_price"`        // Highest price in the window
	IncreasePercent float64 `json:"increase_percent"` // 24h change in percent
	IsAvailable     bool    `json:"-"`                // Whether tradable (internal use)
}

// GetAI500List returns the locally computed AI500 board.
func (c *Client) GetAI500List() ([]CoinData, error) {
	perps, err := c.fetchHyperliquidMomentumUniverse()
	if err != nil {
		return nil, err
	}

	coins := make([]CoinData, 0, len(perps))
	for _, p := range perps {
		score := momentumScore(p)
		coins = append(coins, CoinData{
			Pair:            p.Symbol,
			Score:           score,
			StartTime:       p.WindowStart,
			StartPrice:      p.Price,
			LastScore:       score,
			MaxScore:        score,
			MaxPrice:        p.Price,
			IncreasePercent: p.Change24hPercent,
			IsAvailable:     true,
		})
	}

	sort.SliceStable(coins, func(i, j int) bool {
		return coins[i].Score > coins[j].Score
	})

	return coins, nil
}

// GetTopRatedCoins retrieves the top N coin symbols by score (sorted descending).
func (c *Client) GetTopRatedCoins(limit int) ([]string, error) {
	coins, err := c.GetAI500List()
	if err != nil {
		return nil, err
	}

	if limit <= 0 || limit > len(coins) {
		limit = len(coins)
	}

	symbols := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		symbols = append(symbols, NormalizeSymbol(coins[i].Pair))
	}
	return symbols, nil
}

// GetAvailableCoins retrieves all available coin symbols.
func (c *Client) GetAvailableCoins() ([]string, error) {
	coins, err := c.GetAI500List()
	if err != nil {
		return nil, err
	}

	symbols := make([]string, 0, len(coins))
	for _, coin := range coins {
		if coin.IsAvailable {
			symbols = append(symbols, NormalizeSymbol(coin.Pair))
		}
	}
	return symbols, nil
}

// NormalizeSymbol normalizes coin symbol to XXXUSDT format.
func NormalizeSymbol(symbol string) string {
	symbol = strings.TrimSpace(symbol)
	symbol = strings.ToUpper(symbol)
	if symbol == "" {
		return symbol
	}
	if !strings.HasSuffix(symbol, "USDT") {
		symbol = symbol + "USDT"
	}
	return symbol
}
