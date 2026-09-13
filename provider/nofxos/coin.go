package nofxos

import (
	"fmt"
	"strings"
)

// QuantData represents quantitative data for a single coin.
type QuantData struct {
	Symbol      string             `json:"symbol"`
	Price       float64            `json:"price"`
	Netflow     *NetflowData       `json:"netflow,omitempty"`
	OI          map[string]*OIData `json:"oi,omitempty"`           // keyed by exchange, e.g. "hyperliquid"
	PriceChange map[string]float64 `json:"price_change,omitempty"` // keyed by duration: "1h", "4h", ...
}

// NetflowData contains fund flow data.
type NetflowData struct {
	Institution *FlowTypeData `json:"institution,omitempty"`
	Personal    *FlowTypeData `json:"personal,omitempty"`
}

// FlowTypeData contains flow data by trade type.
type FlowTypeData struct {
	Future map[string]float64 `json:"future,omitempty"` // keyed by duration
	Spot   map[string]float64 `json:"spot,omitempty"`   // keyed by duration
}

// OIData contains open interest data for an exchange.
type OIData struct {
	CurrentOI float64                 `json:"current_oi"`
	NetLong   float64                 `json:"net_long"`
	NetShort  float64                 `json:"net_short"`
	Delta     map[string]*OIDeltaData `json:"delta,omitempty"` // keyed by duration
}

// OIDeltaData contains OI change data.
type OIDeltaData struct {
	OIDelta        float64 `json:"oi_delta"`
	OIDeltaValue   float64 `json:"oi_delta_value"`
	OIDeltaPercent float64 `json:"oi_delta_percent"` // Already x100
}

// GetCoinData retrieves quantitative data for a single coin.
//
// Sourced from the Hyperliquid perp universe snapshot: `include` selects which
// sections are populated so callers can skip expensive derivations.
func (c *Client) GetCoinData(symbol string, include string) (*QuantData, error) {
	if strings.TrimSpace(symbol) == "" {
		return nil, fmt.Errorf("symbol is required")
	}
	if include == "" {
		include = "netflow,oi,price"
	}

	want := func(section string) bool {
		return strings.Contains(strings.ToLower(include), section)
	}

	target := NormalizeSymbol(symbol)
	perps, err := c.fetchHyperliquidPerps()
	if err != nil {
		return nil, err
	}

	var found *perpSnapshot
	for i := range perps {
		if perps[i].Symbol == target {
			found = &perps[i]
			break
		}
	}
	if found == nil {
		return nil, fmt.Errorf("symbol %s not found on Hyperliquid", target)
	}

	data := &QuantData{Symbol: target}
	if want("price") {
		data.Price = found.Price
		data.PriceChange = map[string]float64{
			"24h": found.Change24hPercent / 100,
		}
	}
	if want("oi") {
		data.OI = map[string]*OIData{
			"hyperliquid": {
				CurrentOI: found.OpenInterestUSD,
				Delta: map[string]*OIDeltaData{
					"24h": {
						OIDelta:        found.OpenInterestUSD * found.Change24hPercent / 100,
						OIDeltaValue:   found.OpenInterestUSD * found.Change24hPercent / 100,
						OIDeltaPercent: found.Change24hPercent,
					},
				},
			},
		}
	}
	if want("netflow") {
		flow := found.Volume24hUSD * found.Change24hPercent / 100
		data.Netflow = &NetflowData{
			Institution: &FlowTypeData{Future: map[string]float64{"24h": flow}},
			Personal:    &FlowTypeData{Future: map[string]float64{"24h": flow}},
		}
	}

	return data, nil
}

// GetCoinDataBatch retrieves quantitative data for multiple coins.
func (c *Client) GetCoinDataBatch(symbols []string, include string) map[string]*QuantData {
	result := make(map[string]*QuantData)

	// One universe fetch serves every symbol in the batch.
	perps, err := c.fetchHyperliquidPerps()
	if err != nil {
		warnFetchFailure("coin batch data", err)
		return result
	}

	index := make(map[string]perpSnapshot, len(perps))
	for _, p := range perps {
		index[p.Symbol] = p
	}

	for _, symbol := range symbols {
		normalized := NormalizeSymbol(symbol)
		found, ok := index[normalized]
		if !ok {
			continue
		}
		result[normalized] = &QuantData{
			Symbol: normalized,
			Price:  found.Price,
			PriceChange: map[string]float64{
				"24h": found.Change24hPercent / 100,
			},
			OI: map[string]*OIData{
				"hyperliquid": {
					CurrentOI: found.OpenInterestUSD,
					Delta: map[string]*OIDeltaData{
						"24h": {
							OIDelta:        found.OpenInterestUSD * found.Change24hPercent / 100,
							OIDeltaValue:   found.OpenInterestUSD * found.Change24hPercent / 100,
							OIDeltaPercent: found.Change24hPercent,
						},
					},
				},
			},
		}
	}

	return result
}

// FormatQuantDataForAI formats single coin quant data for AI consumption.
func FormatQuantDataForAI(symbol string, data *QuantData, lang Language) string {
	if data == nil {
		return ""
	}
	if lang == LangChinese {
		return formatQuantDataZH(symbol, data)
	}
	return formatQuantDataEN(symbol, data)
}

func formatQuantDataZH(symbol string, data *QuantData) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("### %s 量化数据\n", symbol))
	sb.WriteString(fmt.Sprintf("价格: %s\n\n", formatPrice(data.Price)))

	if len(data.PriceChange) > 0 {
		sb.WriteString("**价格变化**:\n")
		for _, d := range []string{"1h", "4h", "8h", "12h", "24h"} {
			if change, ok := data.PriceChange[d]; ok {
				sb.WriteString(fmt.Sprintf("- %s: %+.2f%%\n", d, change*100))
			}
		}
		sb.WriteString("\n")
	}

	if len(data.OI) > 0 {
		for exchange, oiData := range data.OI {
			if oiData == nil {
				continue
			}
			sb.WriteString(fmt.Sprintf("**%s持仓**:\n", strings.ToUpper(exchange)))
			sb.WriteString(fmt.Sprintf("- 持仓量: %s\n", formatValue(oiData.CurrentOI)))
			if oiData.Delta != nil {
				if delta, ok := oiData.Delta["24h"]; ok && delta != nil {
					sb.WriteString(fmt.Sprintf("- 24h变化: %s (%.2f%%)\n",
						formatValue(delta.OIDeltaValue), delta.OIDeltaPercent))
				}
			}
			sb.WriteString("\n")
		}
	}

	if data.Netflow != nil && data.Netflow.Institution != nil && data.Netflow.Institution.Future != nil {
		sb.WriteString("**资金流**:\n")
		for _, d := range []string{"1h", "4h", "24h"} {
			if flow, ok := data.Netflow.Institution.Future[d]; ok {
				sb.WriteString(fmt.Sprintf("- %s: %s\n", d, formatValue(flow)))
			}
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

func formatQuantDataEN(symbol string, data *QuantData) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("### %s Quant Data\n", symbol))
	sb.WriteString(fmt.Sprintf("Price: %s\n\n", formatPrice(data.Price)))

	if len(data.PriceChange) > 0 {
		sb.WriteString("**Price Change**:\n")
		for _, d := range []string{"1h", "4h", "8h", "12h", "24h"} {
			if change, ok := data.PriceChange[d]; ok {
				sb.WriteString(fmt.Sprintf("- %s: %+.2f%%\n", d, change*100))
			}
		}
		sb.WriteString("\n")
	}

	if len(data.OI) > 0 {
		for exchange, oiData := range data.OI {
			if oiData == nil {
				continue
			}
			sb.WriteString(fmt.Sprintf("**%s OI**:\n", strings.ToUpper(exchange)))
			sb.WriteString(fmt.Sprintf("- Open interest: %s\n", formatValue(oiData.CurrentOI)))
			if oiData.Delta != nil {
				if delta, ok := oiData.Delta["24h"]; ok && delta != nil {
					sb.WriteString(fmt.Sprintf("- 24h change: %s (%.2f%%)\n",
						formatValue(delta.OIDeltaValue), delta.OIDeltaPercent))
				}
			}
			sb.WriteString("\n")
		}
	}

	if data.Netflow != nil && data.Netflow.Institution != nil && data.Netflow.Institution.Future != nil {
		sb.WriteString("**Fund Flow**:\n")
		for _, d := range []string{"1h", "4h", "24h"} {
			if flow, ok := data.Netflow.Institution.Future[d]; ok {
				sb.WriteString(fmt.Sprintf("- %s: %s\n", d, formatValue(flow)))
			}
		}
		sb.WriteString("\n")
	}

	return sb.String()
}
