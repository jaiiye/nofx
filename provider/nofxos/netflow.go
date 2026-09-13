package nofxos

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// NetFlowPosition represents fund flow data for a single coin.
type NetFlowPosition struct {
	Rank   int     `json:"rank"`
	Symbol string  `json:"symbol"`
	Amount float64 `json:"amount"` // Fund flow amount in USDT (positive=inflow, negative=outflow)
	Price  float64 `json:"price"`
}

// NetFlowRankingData contains inflow and outflow rankings.
//
// IMPORTANT: The legacy nofxos.ai service separated institutional vs retail
// futures flow using wallet labels. No free public source publishes
// wallet-labelled flow, so both buckets are intentionally populated with the
// same turnover-direction snapshot. Formatters must therefore present them as
// one dataset rather than implying an institution/retail split.
type NetFlowRankingData struct {
	Duration             string            `json:"duration"`
	TimeRange            string            `json:"time_range"`
	InstitutionFutureTop []NetFlowPosition `json:"institution_future_top"`
	InstitutionFutureLow []NetFlowPosition `json:"institution_future_low"`
	PersonalFutureTop    []NetFlowPosition `json:"personal_future_top"`
	PersonalFutureLow    []NetFlowPosition `json:"personal_future_low"`
	FetchedAt            time.Time         `json:"fetched_at"`
}

// GetNetFlowRanking retrieves NetFlow ranking data (inflow/outflow).
func (c *Client) GetNetFlowRanking(duration string, limit int) (*NetFlowRankingData, error) {
	if duration == "" {
		duration = "1h"
	}
	if limit <= 0 {
		limit = 10
	}

	perps, err := c.fetchHyperliquidMomentumUniverse()
	if err != nil {
		return nil, err
	}

	positions := make([]NetFlowPosition, 0, len(perps))
	for _, p := range perps {
		positions = append(positions, NetFlowPosition{
			Symbol: p.Symbol,
			// Direction of notional turnover over the window.
			Amount: p.Volume24hUSD * p.Change24hPercent / 100,
			Price:  p.Price,
		})
	}

	top := append([]NetFlowPosition(nil), positions...)
	low := append([]NetFlowPosition(nil), positions...)
	sortNetFlowDesc(top)
	sortNetFlowAsc(low)

	if len(top) > limit {
		top = top[:limit]
	}
	if len(low) > limit {
		low = low[:limit]
	}
	rankNetFlow(top)
	rankNetFlow(low)

	result := &NetFlowRankingData{
		Duration:             duration,
		TimeRange:            duration,
		InstitutionFutureTop: top,
		InstitutionFutureLow: low,
		PersonalFutureTop:    top,
		PersonalFutureLow:    low,
		FetchedAt:            time.Now(),
	}
	return result, nil
}

// FormatNetFlowRankingForAI formats NetFlow ranking data for AI consumption.
func FormatNetFlowRankingForAI(data *NetFlowRankingData, lang Language) string {
	if data == nil {
		return ""
	}
	if lang == LangChinese {
		return formatNetFlowRankingZH(data)
	}
	return formatNetFlowRankingEN(data)
}

func formatNetFlowRankingZH(data *NetFlowRankingData) string {
	var sb strings.Builder

	sb.WriteString("## 资金流向排行 (24h)\n\n")

	if len(data.InstitutionFutureTop) > 0 {
		sb.WriteString("### 资金流入榜\n")
		sb.WriteString("买入信号:\n\n")
		sb.WriteString("| 排名 | 币种 | 净流入(USDT) | 价格 |\n")
		sb.WriteString("|------|------|--------------|------|\n")
		for _, pos := range data.InstitutionFutureTop {
			sb.WriteString(fmt.Sprintf("| %d | %s | %s | %s |\n",
				pos.Rank, pos.Symbol, formatValue(pos.Amount), formatPrice(pos.Price)))
		}
		sb.WriteString("\n")
	}

	if len(data.InstitutionFutureLow) > 0 {
		sb.WriteString("### 资金流出榜\n")
		sb.WriteString("卖出信号:\n\n")
		sb.WriteString("| 排名 | 币种 | 净流出(USDT) | 价格 |\n")
		sb.WriteString("|------|------|--------------|------|\n")
		for _, pos := range data.InstitutionFutureLow {
			sb.WriteString(fmt.Sprintf("| %d | %s | %s | %s |\n",
				pos.Rank, pos.Symbol, formatValue(pos.Amount), formatPrice(pos.Price)))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("**数据来源**: Hyperliquid 全市场永续合约成交额与价格方向推算\n")
	sb.WriteString("**解读**: 资金流入+价格上涨=看多 | 资金流出+价格下跌=看空\n\n")
	return sb.String()
}

func formatNetFlowRankingEN(data *NetFlowRankingData) string {
	var sb strings.Builder

	sb.WriteString("## Fund Flow Ranking (24h)\n\n")

	if len(data.InstitutionFutureTop) > 0 {
		sb.WriteString("### Net Inflow\n")
		sb.WriteString("Buying signals:\n\n")
		sb.WriteString("| Rank | Symbol | Net Inflow (USDT) | Price |\n")
		sb.WriteString("|------|--------|-------------------|-------|\n")
		for _, pos := range data.InstitutionFutureTop {
			sb.WriteString(fmt.Sprintf("| %d | %s | %s | %s |\n",
				pos.Rank, pos.Symbol, formatValue(pos.Amount), formatPrice(pos.Price)))
		}
		sb.WriteString("\n")
	}

	if len(data.InstitutionFutureLow) > 0 {
		sb.WriteString("### Net Outflow\n")
		sb.WriteString("Selling signals:\n\n")
		sb.WriteString("| Rank | Symbol | Net Outflow (USDT) | Price |\n")
		sb.WriteString("|------|--------|--------------------|-------|\n")
		for _, pos := range data.InstitutionFutureLow {
			sb.WriteString(fmt.Sprintf("| %d | %s | %s | %s |\n",
				pos.Rank, pos.Symbol, formatValue(pos.Amount), formatPrice(pos.Price)))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("**Source**: Derived from Hyperliquid public perpetual turnover and price direction\n")
	sb.WriteString("**Key**: Inflow + price up = Bullish | Outflow + price down = Bearish\n\n")
	return sb.String()
}

func sortNetFlowDesc(items []NetFlowPosition) {
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Amount > items[j].Amount
	})
}

func sortNetFlowAsc(items []NetFlowPosition) {
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Amount < items[j].Amount
	})
}

func rankNetFlow(items []NetFlowPosition) {
	for i := range items {
		items[i].Rank = i + 1
	}
}
