package coinalyze

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOIHistory_RequestShape(t *testing.T) {
	var gotPath, gotKey string
	gotQ := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("api_key")
		for k, v := range r.URL.Query() {
			gotQ[k] = v[0]
		}
		// New nested response shape: [{symbol, history: [{t,o,h,l,c}]}]
		_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT_PERP.H","history":[{"t":1700000000,"o":12345.6,"h":12350,"l":12340,"c":12348.2}]}]`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey")
	hist, err := c.OIHistory(context.Background(), []string{"BTCUSDT_PERP.H"}, "1hour", 1700000000, 1700003600)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/open-interest-history" {
		t.Errorf("path = %q", gotPath)
	}
	if gotKey != "testkey" {
		t.Errorf("api_key = %q", gotKey)
	}
	if gotQ["interval"] != "1hour" {
		t.Errorf("interval = %q (want 1hour)", gotQ["interval"])
	}
	if gotQ["symbols"] != "BTCUSDT_PERP.H" {
		t.Errorf("symbols = %q", gotQ["symbols"])
	}
	if len(hist) != 1 {
		t.Fatalf("hist len = %d, want 1", len(hist))
	}
	if hist[0].Symbol != "BTCUSDT_PERP.H" {
		t.Errorf("Symbol = %q", hist[0].Symbol)
	}
	if hist[0].OI != 12348.2 {
		t.Errorf("OI (c) = %v, want 12348.2", hist[0].OI)
	}
	if hist[0].Time != 1700000000 {
		t.Errorf("Time = %d, want 1700000000 (seconds)", hist[0].Time)
	}
}

func TestEmptyKey(t *testing.T) {
	c := New("", "")
	_, err := c.FundingHistory(context.Background(), []string{"BTCUSDT_PERP.H"}, 0, 0)
	if err == nil {
		t.Fatal("expected error when api key empty")
	}
}

func TestSymbolMapper(t *testing.T) {
	if got := HLToCoinalyze("BTC"); got != "BTCUSDT_PERP.H" {
		t.Errorf("mapper BTC = %q, want BTCUSDT_PERP.H", got)
	}
	if got := HLToCoinalyze("ETH"); got != "ETHUSDT_PERP.H" {
		t.Errorf("mapper ETH = %q", got)
	}
}

func TestJoinCSV(t *testing.T) {
	if got := joinCSV([]string{"a", "b", "c"}); got != "a,b,c" {
		t.Errorf("joinCSV = %q", got)
	}
	if got := joinCSV([]string{}); got != "" {
		t.Errorf("empty = %q", got)
	}
}
