package binance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewClientWithURLs(server.URL, "ws://127.0.0.1:0")
}

func TestPremiumIndexAll(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fapi/v1/premiumIndex" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.URL.Query().Get("symbol") != "" {
			t.Errorf("PremiumIndexAll must not filter by symbol")
		}
		_, _ = w.Write([]byte(`[
			{"symbol":"BTCUSDT","markPrice":"63000.1","indexPrice":"62990.5","lastFundingRate":"0.00010000","nextFundingTime":1700000000000,"time":1699990000000},
			{"symbol":"ETHUSDT","markPrice":"2500.2","indexPrice":"2501.0","lastFundingRate":"-0.00020000","nextFundingTime":1700000000000,"time":1699990000000}
		]`))
	})

	got, err := client.PremiumIndexAll(context.Background())
	if err != nil {
		t.Fatalf("premium index: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("symbols = %d, want 2", len(got))
	}
	btc := got["BTCUSDT"]
	if btc.MarkPrice != 63000.1 || btc.IndexPrice != 62990.5 || btc.LastFundingRate != 0.0001 {
		t.Fatalf("btc = %+v", btc)
	}
	if btc.NextFundingTime != 1700000000000 {
		t.Fatalf("next funding = %d", btc.NextFundingTime)
	}
}

func TestOpenInterest(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("symbol") != "BTCUSDT" {
			t.Errorf("symbol = %q", r.URL.Query().Get("symbol"))
		}
		_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","openInterest":"12345.67","time":1699990000000}`))
	})

	got, err := client.OpenInterest(context.Background(), "btcusdt")
	if err != nil {
		t.Fatalf("open interest: %v", err)
	}
	if got.Symbol != "BTCUSDT" || got.OpenInterest != 12345.67 {
		t.Fatalf("reading = %+v", got)
	}
}

func TestFundingRateHistory(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "2" {
			t.Errorf("limit = %q", r.URL.Query().Get("limit"))
		}
		_, _ = w.Write([]byte(`[
			{"symbol":"BTCUSDT","fundingRate":"0.0001","fundingTime":1699900000000},
			{"symbol":"BTCUSDT","fundingRate":"-0.0002","fundingTime":1699928800000}
		]`))
	})

	points, err := client.FundingRateHistory(context.Background(), "BTCUSDT", 2)
	if err != nil {
		t.Fatalf("funding history: %v", err)
	}
	if len(points) != 2 || points[1].FundingRate != -0.0002 {
		t.Fatalf("points = %+v", points)
	}
}
