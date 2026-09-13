package nofxos

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// perpSnapshot is one Hyperliquid perp contract with the metrics needed to
// derive rankings and momentum scores.
type perpSnapshot struct {
	Symbol           string  // e.g. "BTCUSDT"
	Coin             string  // Hyperliquid coin name, e.g. "BTC"
	Price            float64 // mark price
	Change24hPercent float64 // 24h change in percent
	OpenInterestUSD  float64 // open interest notional in USD
	Volume24hUSD     float64 // 24h notional volume in USD
	WindowStart      int64   // Unix seconds marking the start of the 24h window
}

// hyperliquidMeta holds the perp universe definition.
type hyperliquidMeta struct {
	Universe []struct {
		Name         string `json:"name"`
		SzDecimals   int    `json:"szDecimals"`
		MaxLeverage  int    `json:"maxLeverage"`
		OnlyIsolated bool   `json:"onlyIsolated"`
		IsDelisted   bool   `json:"isDelisted"`
	} `json:"universe"`
}

// hyperliquidAssetCtx holds the per-asset market context, index-aligned with
// the universe array returned alongside it.
type hyperliquidAssetCtx struct {
	Funding      string `json:"funding"`
	OpenInterest string `json:"openInterest"`
	PrevDayPx    string `json:"prevDayPx"`
	DayNtlVlm    string `json:"dayNtlVlm"`
	Premium      string `json:"premium"`
	OraclePx     string `json:"oraclePx"`
	MarkPx       string `json:"markPx"`
	MidPx        string `json:"midPx"`
}

// fetchHyperliquidPerps pulls the full perp universe plus mark/oracle prices,
// 24h volume and open interest in a single info-API round trip.
func (c *Client) fetchHyperliquidPerps() ([]perpSnapshot, error) {
	raw, err := c.postJSON(hyperliquidInfoURL, map[string]string{"type": "metaAndAssetCtxs"})
	if err != nil {
		return nil, fmt.Errorf("hyperliquid metaAndAssetCtxs failed: %w", err)
	}

	var payload []json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("failed to parse hyperliquid response: %w", err)
	}
	if len(payload) < 2 {
		return nil, fmt.Errorf("unexpected hyperliquid response shape (%d parts)", len(payload))
	}

	var meta hyperliquidMeta
	if err := json.Unmarshal(payload[0], &meta); err != nil {
		return nil, fmt.Errorf("failed to parse hyperliquid meta: %w", err)
	}

	var ctxs []hyperliquidAssetCtx
	if err := json.Unmarshal(payload[1], &ctxs); err != nil {
		return nil, fmt.Errorf("failed to parse hyperliquid asset contexts: %w", err)
	}

	windowStart := time.Now().Add(-24 * time.Hour).Unix()
	out := make([]perpSnapshot, 0, len(meta.Universe))
	for i, asset := range meta.Universe {
		if i >= len(ctxs) {
			break
		}
		if asset.IsDelisted {
			continue
		}

		ctx := ctxs[i]
		mark := parseFloat(ctx.MarkPx)
		if mark <= 0 {
			mark = parseFloat(ctx.OraclePx)
		}
		if mark <= 0 {
			continue
		}

		prevDay := parseFloat(ctx.PrevDayPx)
		changePercent := 0.0
		if prevDay > 0 {
			changePercent = (mark - prevDay) / prevDay * 100
		}

		openInterest := parseFloat(ctx.OpenInterest) * mark
		volume := parseFloat(ctx.DayNtlVlm)

		out = append(out, perpSnapshot{
			Symbol:           NormalizeSymbol(asset.Name),
			Price:            mark,
			Change24hPercent: changePercent,
			OpenInterestUSD:  openInterest,
			Volume24hUSD:     volume,
			WindowStart:      windowStart,
		})
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("hyperliquid returned no tradable perps")
	}
	return out, nil
}

// fetchHyperliquidMomentumUniverse returns the perps that pass the minimum
// liquidity bar required to be ranked on the AI500 board.
func (c *Client) fetchHyperliquidMomentumUniverse() ([]perpSnapshot, error) {
	perps, err := c.fetchHyperliquidPerps()
	if err != nil {
		return nil, err
	}

	filtered := make([]perpSnapshot, 0, len(perps))
	for _, p := range perps {
		// Skip thin books — they produce noisy, non-actionable scores.
		if p.Volume24hUSD < minRankedVolumeUSD || p.OpenInterestUSD < minRankedOpenInterestUSD {
			continue
		}
		filtered = append(filtered, p)
	}
	if len(filtered) == 0 {
		// Liquidity thresholds were too strict for current conditions;
		// fall back to the full universe rather than returning an empty board.
		return perps, nil
	}
	return filtered, nil
}

const (
	// minRankedVolumeUSD is the 24h notional volume floor for the AI500 board.
	minRankedVolumeUSD = 2_000_000
	// minRankedOpenInterestUSD is the open-interest floor for the AI500 board.
	minRankedOpenInterestUSD = 500_000
)

// momentumScore maps a perp's momentum and liquidity profile onto 0-100.
//
// Weighting rationale: price momentum is the primary signal the AI500 board is
// meant to surface, with turnover and open interest used as confidence
// multipliers so illiquid pumps do not top the list.
func momentumScore(p perpSnapshot) float64 {
	momentum := clamp01((p.Change24hPercent + 15) / 30) // -15%..+15% -> 0..1
	liquidity := clamp01(math.Log10(1+p.Volume24hUSD) / math.Log10(1+1e9))
	interest := clamp01(math.Log10(1+p.OpenInterestUSD) / math.Log10(1+1e8))

	score := 0.60*momentum + 0.25*liquidity + 0.15*interest
	return math.Round(score*10000) / 100 // 0-100, two decimals
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// parseFloat parses an upstream numeric string, tolerating empty input.
func parseFloat(raw string) float64 {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0
	}
	value, err := strconv.ParseFloat(trimmed, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return value
}
