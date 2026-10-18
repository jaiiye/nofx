package hyperliquid

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetClearinghouseStateSendsDexAndParsesPositions(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"assetPositions":[{"position":{"coin":"NVDA","szi":"-0.5","entryPx":"224.5","liquidationPx":"260.1","leverage":{"type":"cross","value":10}}}],"marginSummary":{"accountValue":"150.5"},"time":1}`))
	}))
	defer server.Close()

	previous := clearinghouseStateURL
	clearinghouseStateURL = server.URL
	defer func() { clearinghouseStateURL = previous }()

	state, err := GetClearinghouseState(context.Background(), " 0xabc ", "xyz")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if payload["type"] != "clearinghouseState" || payload["user"] != "0xabc" || payload["dex"] != "xyz" {
		t.Fatalf("payload = %v", payload)
	}
	if len(state.AssetPositions) != 1 {
		t.Fatalf("positions = %d, want 1", len(state.AssetPositions))
	}
	position := state.AssetPositions[0].Position
	if StringFloat(position.Size) != -0.5 || StringFloat(position.LiquidationPx) != 260.1 {
		t.Fatalf("position = %+v", position)
	}
	if StringFloat(state.MarginSummary.AccountValue) != 150.5 {
		t.Fatalf("account value = %v", state.MarginSummary.AccountValue)
	}
}

func TestGetClearinghouseStateErrors(t *testing.T) {
	if _, err := GetClearinghouseState(context.Background(), "   ", ""); err == nil {
		t.Fatal("an empty address must be rejected before any request")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("slow down"))
	}))
	defer server.Close()

	previous := clearinghouseStateURL
	clearinghouseStateURL = server.URL
	defer func() { clearinghouseStateURL = previous }()

	if _, err := GetClearinghouseState(context.Background(), "0xabc", ""); err == nil {
		t.Fatal("a non-200 response must surface as an error")
	}
}

func TestStringFloatDegradesMalformedNumbers(t *testing.T) {
	if got := StringFloat(" 63000.5 "); got != 63000.5 {
		t.Fatalf("value = %v, want 63000.5", got)
	}
	if got := StringFloat("not-a-number"); got != 0 {
		t.Fatalf("malformed value = %v, want 0", got)
	}
}
