package bybit

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

func TestTickers(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v5/market/tickers" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.URL.Query().Get("category") != "linear" {
			t.Errorf("category = %q", r.URL.Query().Get("category"))
		}
		_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"category":"linear","list":[
			{"symbol":"BTCUSDT","lastPrice":"63000","markPrice":"63010","indexPrice":"62990","openInterest":"1000.5","openInterestValue":"63000000","fundingRate":"0.0001","nextFundingTime":"1700000000000"},
			{"symbol":"ETHUSDT","lastPrice":"2500","markPrice":"2501","indexPrice":"2500","openInterest":"5000","openInterestValue":"12500000","fundingRate":"-0.0002","nextFundingTime":"1700000000000"}
		]},"time":1699990000000}`))
	})

	got, err := client.Tickers(context.Background())
	if err != nil {
		t.Fatalf("tickers: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("tickers = %d, want 2", len(got))
	}
	btc := got["BTCUSDT"]
	if btc.MarkPrice != 63010 || btc.OpenInterest != 1000.5 || btc.OpenInterestValue != 63000000 {
		t.Fatalf("btc = %+v", btc)
	}
	if btc.FundingRate != 0.0001 || btc.NextFundingTimeMs != 1700000000000 {
		t.Fatalf("funding = %+v", btc)
	}
}

func TestTickersRejectsNonZeroRetCode(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"retCode":10001,"retMsg":"params error","result":{}}`))
	})

	if _, err := client.Tickers(context.Background()); err == nil {
		t.Fatal("expected an error for a non-zero retCode")
	}
}

func TestInstrumentsInfoPaginatesAndReadsFundingInterval(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") == "" {
			_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"list":[
				{"symbol":"BTCUSDT","contractType":"LinearPerpetual","status":"Trading","fundingInterval":480}
			],"nextPageCursor":"page-2"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"list":[
			{"symbol":"ETHUSDT","contractType":"LinearPerpetual","status":"Trading","fundingInterval":"240"}
		],"nextPageCursor":""}}`))
	})

	got, err := client.InstrumentsInfo(context.Background())
	if err != nil {
		t.Fatalf("instruments: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("instruments = %d, want 2", len(got))
	}
	if got["BTCUSDT"].FundingIntervalMinutes != 480 {
		t.Fatalf("btc interval = %d", got["BTCUSDT"].FundingIntervalMinutes)
	}
	if got["ETHUSDT"].FundingIntervalMinutes != 240 {
		t.Fatalf("eth interval = %d", got["ETHUSDT"].FundingIntervalMinutes)
	}
}
