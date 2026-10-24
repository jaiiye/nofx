package agent

import (
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"nofx/market"
)

func withStubbedScreener(t *testing.T, fn func() ([]market.UniverseCoin, error)) {
	t.Helper()
	original := fetchScreenerForTool
	fetchScreenerForTool = fn
	t.Cleanup(func() { fetchScreenerForTool = original })
}

func TestToolGetScreenerListSortsAndLimits(t *testing.T) {
	withStubbedScreener(t, func() ([]market.UniverseCoin, error) {
		return []market.UniverseCoin{
			{Symbol: "LOWUSDT", Score: 10, Change24hPct: -3},
			{Symbol: "TOPUSDT", Score: 99, Change24hPct: 42},
			{Symbol: "MIDUSDT", Score: 55, Change24hPct: 7},
		}, nil
	})

	a := New(nil, nil, DefaultConfig(), slog.Default())
	raw := a.toolGetAI500List("default", `{"limit": 2}`)

	var resp struct {
		Status string `json:"status"`
		Count  int    `json:"count"`
		Coins  []struct {
			Pair         string  `json:"pair"`
			Score        float64 `json:"score"`
			Change24hPct float64 `json:"change_24h_pct"`
		} `json:"coins"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("invalid JSON %q: %v", raw, err)
	}
	if resp.Status != "ok" || resp.Count != 2 || len(resp.Coins) != 2 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp.Coins[0].Pair != "TOPUSDT" || resp.Coins[1].Pair != "MIDUSDT" {
		t.Fatalf("expected score-descending order, got %+v", resp.Coins)
	}
}

func TestToolGetScreenerListDefaultLimit(t *testing.T) {
	coins := make([]market.UniverseCoin, 30)
	for i := range coins {
		coins[i] = market.UniverseCoin{Symbol: "C", Score: float64(i)}
	}
	withStubbedScreener(t, func() ([]market.UniverseCoin, error) {
		return coins, nil
	})

	a := New(nil, nil, DefaultConfig(), slog.Default())
	var resp struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal([]byte(a.toolGetAI500List("default", "")), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp.Count != 20 {
		t.Fatalf("default limit = %d, want 20", resp.Count)
	}
}

func TestToolGetScreenerListUpstreamError(t *testing.T) {
	withStubbedScreener(t, func() ([]market.UniverseCoin, error) {
		return nil, errors.New("universe down")
	})

	a := New(nil, nil, DefaultConfig(), slog.Default())
	raw := a.toolGetAI500List("default", "")
	if !strings.Contains(raw, `"error"`) {
		t.Fatalf("expected error payload, got %q", raw)
	}
}

func TestHandleToolCallDispatchesAI500(t *testing.T) {
	withStubbedScreener(t, func() ([]market.UniverseCoin, error) {
		return []market.UniverseCoin{{Symbol: "BTCUSDT", Score: 90}}, nil
	})

	a := New(nil, nil, DefaultConfig(), slog.Default())
	raw := a.handleToolCall(t.Context(), "default", 1, "zh", toolCall("c1", "get_ai500_list", "{}"))
	if !strings.Contains(raw, "BTCUSDT") {
		t.Fatalf("dispatch failed, got %q", raw)
	}
}

func TestAgentToolsIncludeAI500(t *testing.T) {
	for _, tool := range agentTools() {
		if tool.Function.Name == "get_ai500_list" {
			return
		}
	}
	t.Fatal("get_ai500_list missing from agent toolset")
}
