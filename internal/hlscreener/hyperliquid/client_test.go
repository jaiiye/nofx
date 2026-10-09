package hyperliquid

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestClient wires a Client to an httptest server. The server records
// every request body so tests can assert on what we sent.
func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *[]string) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Client{apiURL: srv.URL, http: srv.Client()}, nil
}

func TestMeta(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		if got["type"] != "meta" {
			t.Errorf("want type=meta, got %v", got["type"])
		}
		_ = json.NewEncoder(w).Encode(Meta{Universe: []UniverseAsset{
			{Name: "BTC", SzDecimals: 5, MaxLeverage: 50},
			{Name: "ETH", SzDecimals: 4, MaxLeverage: 50},
		}})
	})
	m, err := c.Meta(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Universe) != 2 {
		t.Fatalf("universe len = %d, want 2", len(m.Universe))
	}
	if m.Universe[0].Name != "BTC" || m.Universe[0].MaxLeverage != 50 {
		t.Errorf("BTC meta wrong: %+v", m.Universe[0])
	}
}

func TestAllMids(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"BTC":"60000.5","ETH":"3000.0","SOL":"150.25"}`))
	})
	mids, err := c.AllMids(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mids["BTC"] != "60000.5" {
		t.Errorf("BTC = %q, want 60000.5", mids["BTC"])
	}
	if len(mids) != 3 {
		t.Errorf("got %d mids, want 3", len(mids))
	}
}

func TestCandles(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var got map[string]any
		_ = json.Unmarshal(body, &got)
		if got["type"] != "candleSnapshot" {
			t.Errorf("type = %v, want candleSnapshot", got["type"])
		}
		req, ok := got["req"].(map[string]any)
		if !ok {
			t.Fatalf("missing req: %v", got)
		}
		if req["coin"] != "BTC" {
			t.Errorf("coin = %v", req["coin"])
		}
		if req["interval"] != "1h" {
			t.Errorf("interval = %v", req["interval"])
		}
		_, _ = w.Write([]byte(`[
		  {"t":1700000000000,"T":1700003600000,"s":"BTC","i":"1h",
		   "o":"60000","c":"60100","h":"60200","l":"59900","v":"123.5","n":42}
		]`))
	})
	cs, err := c.Candles(context.Background(), "BTC", "1h", 1700000000000)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 {
		t.Fatalf("got %d candles, want 1", len(cs))
	}
	if cs[0].Close() != 60100 || cs[0].N != 42 {
		t.Errorf("candle fields wrong: %+v", cs[0])
	}
}

func TestRecentTrades(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// Real HL shape: px/sz are strings.
		_, _ = w.Write([]byte(`[
		  {"coin":"BTC","side":"B","px":"60100","sz":"0.5",
		   "hash":"0xabc","tid":1234,"users":["0x1","0x2"],"time":1700000000000},
		  {"coin":"BTC","side":"A","px":"60099","sz":"0.3",
		   "hash":"0xdef","tid":1235,"users":["0x3","0x4"],"time":1700000001000}
		]`))
	})
	tr, err := c.RecentTrades(context.Background(), "BTC")
	if err != nil {
		t.Fatal(err)
	}
	if len(tr) != 2 {
		t.Fatalf("got %d trades, want 2", len(tr))
	}
	if tr[0].Side != "B" || tr[1].Side != "A" {
		t.Errorf("side mapping wrong: %+v", tr)
	}
	if tr[0].Price() != 60100 || tr[0].Size() != 0.5 {
		t.Errorf("accessors wrong: price=%v size=%v", tr[0].Price(), tr[0].Size())
	}
}

func TestOpenInterest(t *testing.T) {
	// OpenInterest(coin) now goes through AssetContexts (metaAndAssetCtxs)
	// which returns [[meta], [ctx, ctx, ...]].
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// Verify the request type
		var got map[string]any
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		if got["type"] != "metaAndAssetCtxs" {
			t.Errorf("type = %v, want metaAndAssetCtxs", got["type"])
		}
		_, _ = w.Write([]byte(`[
			{"universe":[
			  {"name":"BTC","szDecimals":5,"maxLeverage":50,"onlyPerp":true}
			]},
			[{"funding":"0.0001","markPx":"60000","oraclePx":"60100","midPx":"60050",
			  "openInterest":"12345.67","premium":"0.00005","prevDayPx":"59500",
			  "dayBaseVlm":"100","dayNtlVlm":"6000000","impactPxs":["59990","60010"]}]
		]`))
	})
	oi, err := c.OpenInterest(context.Background(), "BTC")
	if err != nil {
		t.Fatal(err)
	}
	if oi.OpenInterest != 12345.67 {
		t.Errorf("OI = %v, want 12345.67", oi.OpenInterest)
	}
}

func TestAssetContexts(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"universe":[
			  {"name":"BTC","szDecimals":5,"maxLeverage":50,"onlyPerp":true},
			  {"name":"ETH","szDecimals":4,"maxLeverage":50,"onlyPerp":true}
			]},
			[
			  {"funding":"0.0001","markPx":"60000","oraclePx":"60100","midPx":"60050",
			   "openInterest":"100","premium":"0","prevDayPx":"59500",
			   "dayBaseVlm":"50","dayNtlVlm":"3000000","impactPxs":[]},
			  {"funding":"0.0002","markPx":"3000","oraclePx":"3010","midPx":"3005",
			   "openInterest":"500","premium":"0","prevDayPx":"2900",
			   "dayBaseVlm":"200","dayNtlVlm":"600000","impactPxs":[]}
			]
		]`))
	})
	ctxs, err := c.AssetContexts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ctxs.Universe) != 2 {
		t.Fatalf("universe = %d, want 2", len(ctxs.Universe))
	}
	if len(ctxs.Contexts) != 2 {
		t.Fatalf("contexts = %d, want 2", len(ctxs.Contexts))
	}
	if ctxs.Contexts[0].Mark() != 60000 {
		t.Errorf("BTC mark = %v", ctxs.Contexts[0].Mark())
	}
	if ctxs.Contexts[1].DayNtlVlmFloat() != 600000 {
		t.Errorf("ETH dayNtlVlm = %v", ctxs.Contexts[1].DayNtlVlmFloat())
	}
}

func TestFundingHistory(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
		  {"coin":"BTC","fundingRate":"0.0001","premium":"0.0001","time":1700000000000},
		  {"coin":"BTC","fundingRate":"0.00012","premium":"0.0001","time":1700003600000}
		]`))
	})
	fs, err := c.FundingHistory(context.Background(), "BTC", 1699999000000)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 2 {
		t.Fatalf("got %d fundings, want 2", len(fs))
	}
	if fs[1].Rate() != 0.00012 {
		t.Errorf("funding[1] = %v", fs[1].Rate())
	}
}

func TestErrorPropagates(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`rate limited`))
	})
	_, err := c.Meta(context.Background())
	if err == nil {
		t.Fatal("expected error on 429")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error should mention status: %v", err)
	}
}

func TestNon200WithBody(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	})
	_, err := c.Candles(context.Background(), "BTC", "1m", 0)
	if err == nil {
		t.Fatal("expected error on 500")
	}
}
