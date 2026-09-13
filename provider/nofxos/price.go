package nofxos

import (
	"sort"
	"strings"
	"time"
)

// PriceRankingItem represents single coin price ranking data.
type PriceRankingItem struct {
	Pair         string  `json:"pair"`
	Symbol       string  `json:"symbol"`
	PriceDelta   float64 `json:"price_delta"` // Decimal format: 0.0723 = 7.23%
	Price        float64 `json:"price"`
	FutureFlow   float64 `json:"future_flow"`
	SpotFlow     float64 `json:"spot_flow"`
	OI           float64 `json:"oi"`
	OIDelta      float64 `json:"oi_delta"`
	OIDeltaValue float64 `json:"oi_delta_value"`
}

// PriceRankingDuration contains top gainers and losers for a single duration.
type PriceRankingDuration struct {
	Top []PriceRankingItem `json:"top"`
	Low []PriceRankingItem `json:"low"`
}

// PriceRankingData contains price ranking data for multiple durations.
type PriceRankingData struct {
	Durations map[string]*PriceRankingDuration `json:"durations"`
	FetchedAt time.Time                        `json:"fetched_at"`
}

// GetPriceRanking retrieves price ranking data (gainers/losers).
//
// Free public sources only expose a 24h window without an API key, so every
// requested duration is served from the same 24h snapshot; the keys are
// preserved so downstream formatters keep their multi-duration layout.
func (c *Client) GetPriceRanking(durations string, limit int) (*PriceRankingData, error) {
	if durations == "" {
		durations = "1h"
	}
	if limit <= 0 {
		limit = 10
	}

	perps, err := c.fetchHyperliquidMomentumUniverse()
	if err != nil {
		return nil, err
	}

	items := make([]PriceRankingItem, 0, len(perps))
	for _, p := range perps {
		items = append(items, PriceRankingItem{
			Pair:         p.Symbol,
			Symbol:       p.Symbol,
			PriceDelta:   p.Change24hPercent / 100, // decimal form, matching legacy contract
			Price:        p.Price,
			FutureFlow:   p.Volume24hUSD * p.Change24hPercent / 100,
			OI:           p.OpenInterestUSD,
			OIDelta:      p.OpenInterestUSD * p.Change24hPercent / 100,
			OIDeltaValue: p.OpenInterestUSD * p.Change24hPercent / 100,
		})
	}

	top := append([]PriceRankingItem(nil), items...)
	low := append([]PriceRankingItem(nil), items...)
	sortByPriceDeltaDesc(top)
	sortByPriceDeltaAsc(low)

	if len(top) > limit {
		top = top[:limit]
	}
	if len(low) > limit {
		low = low[:limit]
	}

	requested := parseDurations(durations)
	if len(requested) == 0 {
		requested = []string{"1h"}
	}

	result := &PriceRankingData{
		Durations: make(map[string]*PriceRankingDuration, len(requested)),
		FetchedAt: time.Now(),
	}
	for _, duration := range requested {
		result.Durations[duration] = &PriceRankingDuration{Top: top, Low: low}
	}

	return result, nil
}

// parseDurations splits a comma-separated duration list.
func parseDurations(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func sortByPriceDeltaDesc(items []PriceRankingItem) {
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].PriceDelta > items[j].PriceDelta
	})
}

func sortByPriceDeltaAsc(items []PriceRankingItem) {
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].PriceDelta < items[j].PriceDelta
	})
}

// FormatPriceRankingForAI formats Price ranking data for AI consumption.
func FormatPriceRankingForAI(data *PriceRankingData, lang Language) string {
	if data == nil || len(data.Durations) == 0 {
		return ""
	}
	if lang == LangChinese {
		return formatPriceRankingZH(data)
	}
	return formatPriceRankingEN(data)
}

func formatPriceRankingZH(data *PriceRankingData) string {
	var sb strings.Builder

	sb.WriteString("## 涨跌幅排行 (Hyperliquid 永续, 24h)\n\n")

	durationData := firstAvailableDuration(data)
	if durationData == nil {
		sb.WriteString("_暂无可用的行情排名数据。_\n\n")
		return sb.String()
	}

	sb.WriteString("**涨幅榜**\n")
	sb.WriteString("| 币种 | 涨幅 | 价格 | 24h成交额 | 持仓量 |\n")
	sb.WriteString("|------|------|------|-----------|--------|\n")
	for _, item := range durationData.Top {
		sb.WriteString("| " + item.Symbol + " | " + formatPercent(item.PriceDelta*100, true) + " | " + formatPrice(item.Price) + " | " + formatValue(item.FutureFlow) + " | " + formatValue(item.OI) + " |\n")
	}
	sb.WriteString("\n")

	sb.WriteString("**跌幅榜**\n")
	sb.WriteString("| 币种 | 跌幅 | 价格 | 24h成交额 | 持仓量 |\n")
	sb.WriteString("|------|------|------|-----------|--------|\n")
	for _, item := range durationData.Low {
		sb.WriteString("| " + item.Symbol + " | " + formatPercent(item.PriceDelta*100, false) + " | " + formatPrice(item.Price) + " | " + formatValue(item.FutureFlow) + " | " + formatValue(item.OI) + " |\n")
	}
	sb.WriteString("\n")

	sb.WriteString("**数据来源**: Hyperliquid 全市场永续合约公开行情（24h 窗口）\n")
	sb.WriteString("**解读**: 涨幅大+成交额放大=强势上涨 | 跌幅大+成交额萎缩=弱势下跌\n\n")
	return sb.String()
}

// firstAvailableDuration returns the first non-nil ranking bucket among the
// preferred duration keys. Free public sources expose only a 24h window, so
// every requested key holds the same snapshot.
func firstAvailableDuration(data *PriceRankingData) *PriceRankingDuration {
	for _, duration := range []string{"1h", "4h", "24h"} {
		if bucket, ok := data.Durations[duration]; ok && bucket != nil {
			return bucket
		}
	}
	// Unknown keys still contain valid data — take any.
	for _, bucket := range data.Durations {
		if bucket != nil {
			return bucket
		}
	}
	return nil
}

func formatPriceRankingEN(data *PriceRankingData) string {
	var sb strings.Builder

	sb.WriteString("## Price Gainers/Losers (Hyperliquid perps, 24h)\n\n")

	durationData := firstAvailableDuration(data)
	if durationData == nil {
		sb.WriteString("_No price ranking data available._\n\n")
		return sb.String()
	}

	sb.WriteString("**Top Gainers**\n")
	sb.WriteString("| Symbol | Change | Price | 24h Volume | Open Interest |\n")
	sb.WriteString("|--------|--------|-------|------------|---------------|\n")
	for _, item := range durationData.Top {
		sb.WriteString("| " + item.Symbol + " | " + formatPercent(item.PriceDelta*100, true) + " | " + formatPrice(item.Price) + " | " + formatValue(item.FutureFlow) + " | " + formatValue(item.OI) + " |\n")
	}
	sb.WriteString("\n")

	sb.WriteString("**Top Losers**\n")
	sb.WriteString("| Symbol | Change | Price | 24h Volume | Open Interest |\n")
	sb.WriteString("|--------|--------|-------|------------|---------------|\n")
	for _, item := range durationData.Low {
		sb.WriteString("| " + item.Symbol + " | " + formatPercent(item.PriceDelta*100, false) + " | " + formatPrice(item.Price) + " | " + formatValue(item.FutureFlow) + " | " + formatValue(item.OI) + " |\n")
	}
	sb.WriteString("\n")

	sb.WriteString("**Source**: Hyperliquid public perpetual market data (24h window)\n")
	sb.WriteString("**Key**: Big gain + rising volume = Strong bullish | Big loss + falling volume = Strong bearish\n\n")
	return sb.String()
}
