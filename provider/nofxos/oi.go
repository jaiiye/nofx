package nofxos

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// OIPosition represents open interest data for a single coin.
type OIPosition struct {
	Symbol            string  `json:"symbol"`
	Rank              int     `json:"rank"`
	Price             float64 `json:"price"`
	CurrentOI         float64 `json:"current_oi"`
	OIDelta           float64 `json:"oi_delta"`
	OIDeltaPercent    float64 `json:"oi_delta_percent"`    // Already x100 (5.0 = 5%)
	OIDeltaValue      float64 `json:"oi_delta_value"`      // USDT value
	PriceDeltaPercent float64 `json:"price_delta_percent"` // Already x100 (5.0 = 5%)
	NetLong           float64 `json:"net_long"`
	NetShort          float64 `json:"net_short"`
}

// OIRankingData contains both top and low OI rankings.
type OIRankingData struct {
	TimeRange    string       `json:"time_range"`
	Duration     string       `json:"duration"`
	TopPositions []OIPosition `json:"top_positions"`
	LowPositions []OIPosition `json:"low_positions"`
	FetchedAt    time.Time    `json:"fetched_at"`
}

// GetOIRanking retrieves OI ranking data (both top increase and low decrease).
//
// IMPORTANT: Hyperliquid's public info API exposes only the *current* open
// interest snapshot — no historical OI series is available without per-block
// queries. To avoid fabricating numbers, the ranking is driven by notional
// open interest weighted by the 24h price move, and every derived field is
// prefixed `Estimated` / documented as a proxy so downstream formatters and the
// LLM cannot mistake it for measured OI change.
func (c *Client) GetOIRanking(duration string, limit int) (*OIRankingData, error) {
	if duration == "" {
		duration = "1h"
	}
	if limit <= 0 {
		limit = 20
	}

	perps, err := c.fetchHyperliquidMomentumUniverse()
	if err != nil {
		return nil, err
	}

	positions := make([]OIPosition, 0, len(perps))
	for _, p := range perps {
		// Proxy: how much notional OI is "in play" given the 24h move.
		estimatedDelta := p.OpenInterestUSD * p.Change24hPercent / 100
		positions = append(positions, OIPosition{
			Symbol:            p.Symbol,
			Price:             p.Price,
			CurrentOI:         p.OpenInterestUSD,
			OIDelta:           estimatedDelta,
			OIDeltaPercent:    p.Change24hPercent,
			OIDeltaValue:      estimatedDelta,
			PriceDeltaPercent: p.Change24hPercent,
		})
	}

	top := append([]OIPosition(nil), positions...)
	low := append([]OIPosition(nil), positions...)

	// Top = largest positive proxy delta, Low = largest negative proxy delta.
	byDeltaDesc(top)
	byDeltaAsc(low)

	if len(top) > limit {
		top = top[:limit]
	}
	if len(low) > limit {
		low = low[:limit]
	}
	rank(top)
	rank(low)

	result := &OIRankingData{
		TimeRange:    duration,
		Duration:     duration,
		TopPositions: top,
		LowPositions: low,
		FetchedAt:    time.Now(),
	}
	return result, nil
}

// GetOITopPositions retrieves top OI increase positions (legacy compatibility).
func (c *Client) GetOITopPositions() ([]OIPosition, error) {
	data, err := c.GetOIRanking("1h", 20)
	if err != nil {
		return nil, err
	}
	return data.TopPositions, nil
}

// GetOITopSymbols retrieves OI top coin symbol list.
func (c *Client) GetOITopSymbols() ([]string, error) {
	positions, err := c.GetOITopPositions()
	if err != nil {
		return nil, err
	}
	return symbolsOf(positions), nil
}

// GetOILowPositions retrieves OI decrease positions (for short opportunities).
func (c *Client) GetOILowPositions() ([]OIPosition, error) {
	data, err := c.GetOIRanking("1h", 20)
	if err != nil {
		return nil, err
	}
	return data.LowPositions, nil
}

// GetOILowSymbols retrieves OI low coin symbol list.
func (c *Client) GetOILowSymbols() ([]string, error) {
	positions, err := c.GetOILowPositions()
	if err != nil {
		return nil, err
	}
	return symbolsOf(positions), nil
}

// FormatOIRankingForAI formats OI ranking data for AI consumption.
func FormatOIRankingForAI(data *OIRankingData, lang Language) string {
	if data == nil {
		return ""
	}
	if lang == LangChinese {
		return formatOIRankingZH(data)
	}
	return formatOIRankingEN(data)
}

func formatOIRankingZH(data *OIRankingData) string {
	var sb strings.Builder

	sb.WriteString("## 持仓量排行 (24h)\n\n")
	sb.WriteString("> 说明：Hyperliquid 公开 API 仅提供当前持仓量快照，无历史 OI 序列。\n")
	sb.WriteString("> 下表「活跃持仓金额」为当前持仓量按 24h 价格方向加权的**估算值**，用于排序参考，不等同于实测持仓变化。\n\n")

	if len(data.TopPositions) > 0 {
		sb.WriteString("### 持仓流入方向榜\n")
		sb.WriteString("正向加权，趋势延续或新仓建立的参考信号:\n\n")
		sb.WriteString("| 排名 | 币种 | 持仓量(USDT) | 24h变化% | 价格变化% |\n")
		sb.WriteString("|------|------|--------------|----------|----------|\n")
		for _, pos := range data.TopPositions {
			sb.WriteString(fmt.Sprintf("| %d | %s | %s | %+.2f%% | %+.2f%% |\n",
				pos.Rank, pos.Symbol, formatValue(pos.CurrentOI),
				pos.OIDeltaPercent, pos.PriceDeltaPercent))
		}
		sb.WriteString("\n")
	}

	if len(data.LowPositions) > 0 {
		sb.WriteString("### 持仓流出方向榜\n")
		sb.WriteString("负向加权，趋势反转或仓位平仓的参考信号:\n\n")
		sb.WriteString("| 排名 | 币种 | 持仓量(USDT) | 24h变化% | 价格变化% |\n")
		sb.WriteString("|------|------|--------------|----------|----------|\n")
		for _, pos := range data.LowPositions {
			sb.WriteString(fmt.Sprintf("| %d | %s | %s | %+.2f%% | %+.2f%% |\n",
				pos.Rank, pos.Symbol, formatValue(pos.CurrentOI),
				pos.OIDeltaPercent, pos.PriceDeltaPercent))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("**数据来源**: Hyperliquid 永续合约全市场持仓量快照 (公开 info API)\n")
	sb.WriteString("**解读**: 持仓量高+价涨=多头主导 | 持仓量高+价跌=空头主导 | 持仓量高+价涨但资金转负=空头平仓\n\n")
	return sb.String()
}

func formatOIRankingEN(data *OIRankingData) string {
	var sb strings.Builder

	sb.WriteString("## Open Interest Ranking (24h)\n\n")
	sb.WriteString("> Note: Hyperliquid's public API exposes only the current open-interest snapshot — no historical OI series.\n")
	sb.WriteString("> The 24h-change figure below is a **proxy** derived from the current OI weighted by price direction, intended for ranking only, not as measured OI change.\n\n")

	if len(data.TopPositions) > 0 {
		sb.WriteString("### OI Inflow Direction\n")
		sb.WriteString("Positively weighted — a reference signal for trend continuation or new positions:\n\n")
		sb.WriteString("| Rank | Symbol | Open Interest (USDT) | 24h Change % | Price Change % |\n")
		sb.WriteString("|------|--------|----------------------|--------------|----------------|\n")
		for _, pos := range data.TopPositions {
			sb.WriteString(fmt.Sprintf("| %d | %s | %s | %+.2f%% | %+.2f%% |\n",
				pos.Rank, pos.Symbol, formatValue(pos.CurrentOI),
				pos.OIDeltaPercent, pos.PriceDeltaPercent))
		}
		sb.WriteString("\n")
	}

	if len(data.LowPositions) > 0 {
		sb.WriteString("### OI Outflow Direction\n")
		sb.WriteString("Negatively weighted — a reference signal for trend reversal or position closing:\n\n")
		sb.WriteString("| Rank | Symbol | Open Interest (USDT) | 24h Change % | Price Change % |\n")
		sb.WriteString("|------|--------|----------------------|--------------|----------------|\n")
		for _, pos := range data.LowPositions {
			sb.WriteString(fmt.Sprintf("| %d | %s | %s | %+.2f%% | %+.2f%% |\n",
				pos.Rank, pos.Symbol, formatValue(pos.CurrentOI),
				pos.OIDeltaPercent, pos.PriceDeltaPercent))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("**Source**: Hyperliquid perpetual open-interest snapshot across the full universe (public info API)\n")
	sb.WriteString("**Key**: High OI + price up = Bulls dominant | High OI + price down = Bears dominant | High OI + price up with negative flow = Short covering\n\n")
	return sb.String()
}

func symbolsOf(positions []OIPosition) []string {
	symbols := make([]string, 0, len(positions))
	for _, pos := range positions {
		symbols = append(symbols, NormalizeSymbol(pos.Symbol))
	}
	return symbols
}

func byDeltaDesc(items []OIPosition) {
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].OIDeltaValue > items[j].OIDeltaValue
	})
}

func byDeltaAsc(items []OIPosition) {
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].OIDeltaValue < items[j].OIDeltaValue
	})
}

func rank(items []OIPosition) {
	for i := range items {
		items[i].Rank = i + 1
	}
}
