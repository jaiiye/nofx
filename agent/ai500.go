package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"nofx/market"
)

const (
	screenerDefaultLimit = 20
	screenerMaxLimit     = 100
)

// ScreenerEntry is the display shape for one local screener constituent.
type ScreenerEntry struct {
	Pair         string  `json:"pair"`
	Score        float64 `json:"score"`
	Price        float64 `json:"price"`
	Change24hPct float64 `json:"change_24h_pct"`
	Turnover24h  float64 `json:"turnover_24h"`
	OIUSD        float64 `json:"oi_usd"`
}

// fetchScreenerForTool is swappable in tests. It reads the local universe, which
// is fed by one public Bybit tickers poll; there is no vendor client and no
// wallet key involved.
var fetchScreenerForTool = func() ([]market.UniverseCoin, error) {
	service := market.CurrentUniverse()
	if service == nil {
		return nil, fmt.Errorf("market universe service is not running")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	service.Ensure(ctx)

	coins := service.Screener(0)
	if len(coins) == 0 {
		return nil, fmt.Errorf("screener universe is empty")
	}
	return coins, nil
}

// ScreenerBoard returns the screener constituents sorted by score (descending),
// truncated to limit. The tool keeps its historical "AI500" name for API and
// chat compatibility, but the ranking is the local, documented composite.
func ScreenerBoard(limit int) ([]ScreenerEntry, error) {
	if limit <= 0 {
		limit = screenerDefaultLimit
	}
	if limit > screenerMaxLimit {
		limit = screenerMaxLimit
	}
	coins, err := fetchScreenerForTool()
	if err != nil {
		return nil, err
	}
	// Sort defensively: the board must be deterministic even if a different
	// universe source is injected.
	sort.SliceStable(coins, func(i, j int) bool {
		if coins[i].Score != coins[j].Score {
			return coins[i].Score > coins[j].Score
		}
		return coins[i].Symbol < coins[j].Symbol
	})
	if len(coins) > limit {
		coins = coins[:limit]
	}
	entries := make([]ScreenerEntry, 0, len(coins))
	for _, coin := range coins {
		entries = append(entries, ScreenerEntry{
			Pair:         coin.Symbol,
			Score:        coin.Score,
			Price:        coin.Price,
			Change24hPct: coin.Change24hPct,
			Turnover24h:  coin.Turnover24h,
			OIUSD:        coin.OIUSD,
		})
	}
	return entries, nil
}

// toolGetAI500List exposes the screener board to the chat agent.
func (a *Agent) toolGetAI500List(_ string, argsJSON string) string {
	var args struct {
		Limit int `json:"limit"`
	}
	if strings.TrimSpace(argsJSON) != "" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return fmt.Sprintf(`{"error":"invalid arguments: %s"}`, err)
		}
	}

	entries, err := ScreenerBoard(args.Limit)
	if err != nil {
		return fmt.Sprintf(`{"error":"failed to fetch screener list: %s"}`, err)
	}

	payload, err := json.Marshal(map[string]any{
		"status": "ok",
		"count":  len(entries),
		"coins":  entries,
		"note":   "This is the local screener, not an external AI index. score is a composite of 24h turnover, open interest and volatility (higher is better); change_24h_pct is the 24h price change. Present this in chat as a short numbered list, one coin per line, e.g. \"1. BTCUSDT — score 2.31, 24h -1.4%\". NEVER use a markdown table — the chat UI does not render tables.",
	})
	if err != nil {
		return fmt.Sprintf(`{"error":"failed to serialize screener list: %s"}`, err)
	}
	return string(payload)
}
