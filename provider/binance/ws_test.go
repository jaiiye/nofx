package binance

import "testing"

// The two venues describe the same event with opposite aggressor semantics.
// These cases are the contract every downstream window depends on.
func TestParseForceOrderNormalizesSide(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want ForceOrder
		ok   bool
	}{
		{
			name: "sell aggressor liquidates a long",
			raw:  `{"e":"forceOrder","E":1568014460893,"o":{"s":"BTCUSDT","S":"SELL","q":"0.014","p":"9910","ap":"9910","z":"0.014","T":1568014460893,"i":42}}`,
			want: ForceOrder{Symbol: "BTCUSDT", Side: "long", Price: 9910, Qty: 0.014, NotionalUSD: 9910 * 0.014, TimeMs: 1568014460893, OrderID: "42"},
			ok:   true,
		},
		{
			name: "buy aggressor liquidates a short",
			raw:  `{"e":"forceOrder","E":1568014460893,"o":{"s":"ETHUSDT","S":"BUY","q":"2","p":"2500","ap":"2501","z":"1.5","T":1568014460893}}`,
			want: ForceOrder{Symbol: "ETHUSDT", Side: "short", Price: 2501, Qty: 1.5, NotionalUSD: 2501 * 1.5, TimeMs: 1568014460893},
			ok:   true,
		},
		{
			name: "other events are ignored",
			raw:  `{"e":"aggTrade","s":"BTCUSDT","p":"9910","q":"1"}`,
			ok:   false,
		},
		{
			name: "malformed payload is ignored",
			raw:  `{`,
			ok:   false,
		},
		{
			name: "unknown aggressor side is ignored",
			raw:  `{"e":"forceOrder","o":{"s":"BTCUSDT","S":"?","q":"1","p":"1","T":1}}`,
			ok:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseForceOrder([]byte(tc.raw))
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if got.Symbol != tc.want.Symbol || got.Side != tc.want.Side {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			if got.Price != tc.want.Price || got.Qty != tc.want.Qty || got.NotionalUSD != tc.want.NotionalUSD {
				t.Fatalf("amounts = %+v, want %+v", got, tc.want)
			}
			if got.TimeMs != tc.want.TimeMs || got.OrderID != tc.want.OrderID {
				t.Fatalf("identity = %+v, want %+v", got, tc.want)
			}
		})
	}
}
