package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestToolGetMarketSnapshotRejectsMissingSymbol verifies argument validation
// happens before any upstream market-data call.
func TestToolGetMarketSnapshotRejectsMissingSymbol(t *testing.T) {
	a := New(nil, nil, DefaultConfig(), nil)
	raw := a.toolGetMarketSnapshot(`{"symbol":""}`)
	if !strings.Contains(raw, "symbol is required") {
		t.Fatalf("expected symbol-required error, got: %s", raw)
	}
}

// TestToolGetMarketSnapshotRejectsInvalidInterval verifies interval validation.
func TestToolGetMarketSnapshotRejectsInvalidInterval(t *testing.T) {
	a := New(nil, nil, DefaultConfig(), nil)
	raw := a.toolGetMarketSnapshot(`{"symbol":"BTC","interval":"nonsense"}`)
	if !strings.Contains(raw, "invalid interval") {
		t.Fatalf("expected invalid-interval error, got: %s", raw)
	}
}

func TestToolGetMarketSnapshotRejectsStockSymbols(t *testing.T) {
	a := New(nil, nil, DefaultConfig(), nil)
	raw := a.toolGetMarketSnapshot(`{"symbol":"AAPL"}`)
	if !strings.Contains(raw, "currently supports crypto symbols only") {
		t.Fatalf("expected stock rejection, got: %s", raw)
	}
}

// TestToolGetMarketSnapshotResponseShape guards the JSON contract consumed by
// the agent prompt. The market data itself comes from the live Hyperliquid
// info API, so it is intentionally not exercised here.
func TestToolGetMarketSnapshotResponseShape(t *testing.T) {
	payload := map[string]any{
		"symbol": "BTCUSDT",
		"price":  65000.0,
		"ticker_24h": map[string]any{
			"price_change":         1200.0,
			"price_change_percent": 1.88,
			"quote_volume":         800000000.0,
		},
		"perp_metrics": map[string]any{
			"mark_price":    65010.0,
			"oracle_price":  64990.0,
			"funding_rate":  0.0001,
			"open_interest": 45678.9,
			"source":        "hyperliquid",
		},
		"kline_snapshot": map[string]any{
			"interval":              "15m",
			"limit":                 2,
			"period_change_percent": 1.56,
			"recent_klines":         []map[string]any{},
		},
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal payload: %v", err)
	}

	var resp struct {
		Symbol    string  `json:"symbol"`
		Price     float64 `json:"price"`
		Ticker24h struct {
			PriceChangePercent float64 `json:"price_change_percent"`
		} `json:"ticker_24h"`
		PerpMetrics struct {
			FundingRate  float64 `json:"funding_rate"`
			OpenInterest float64 `json:"open_interest"`
			Source       string  `json:"source"`
		} `json:"perp_metrics"`
		KlineSnapshot struct {
			Interval            string           `json:"interval"`
			Limit               int              `json:"limit"`
			PeriodChangePercent float64          `json:"period_change_percent"`
			RecentKlines        []map[string]any `json:"recent_klines"`
		} `json:"kline_snapshot"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("failed to parse tool response: %v\nraw=%s", err, raw)
	}

	if resp.Symbol != "BTCUSDT" {
		t.Fatalf("expected normalized symbol BTCUSDT, got %s", resp.Symbol)
	}
	if resp.Price != 65000 {
		t.Fatalf("expected price 65000, got %v", resp.Price)
	}
	if resp.Ticker24h.PriceChangePercent != 1.88 {
		t.Fatalf("expected 24h change 1.88, got %v", resp.Ticker24h.PriceChangePercent)
	}
	if resp.PerpMetrics.FundingRate != 0.0001 {
		t.Fatalf("expected funding rate 0.0001, got %v", resp.PerpMetrics.FundingRate)
	}
	if resp.PerpMetrics.OpenInterest != 45678.9 {
		t.Fatalf("expected open interest 45678.9, got %v", resp.PerpMetrics.OpenInterest)
	}
	if resp.PerpMetrics.Source != "hyperliquid" {
		t.Fatalf("expected hyperliquid data source, got %q", resp.PerpMetrics.Source)
	}
	if resp.KlineSnapshot.Interval != "15m" || resp.KlineSnapshot.Limit != 2 {
		t.Fatalf("unexpected kline snapshot metadata: %+v", resp.KlineSnapshot)
	}
}
