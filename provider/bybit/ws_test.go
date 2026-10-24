package bybit

import "testing"

// Bybit reports the position side, which is the inverse of Binance's aggressor
// side. These cases pin the mapping the liquidation windows depend on.
func TestParseAllLiquidationNormalizesSide(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []AllLiquidation
	}{
		{
			name: "buy side means a long was liquidated",
			raw:  `{"topic":"allLiquidation.BTCUSDT","type":"snapshot","ts":1739502303204,"data":[{"T":1739502302929,"s":"BTCUSDT","S":"Buy","v":"2","p":"63000"}]}`,
			want: []AllLiquidation{{Symbol: "BTCUSDT", Side: "long", Price: 63000, Qty: 2, NotionalUSD: 126000, TimeMs: 1739502302929}},
		},
		{
			name: "sell side means a short was liquidated",
			raw:  `{"topic":"allLiquidation.ETHUSDT","type":"snapshot","ts":1739502303204,"data":[{"T":1739502302929,"s":"ETHUSDT","S":"Sell","v":"1.5","p":"2500"}]}`,
			want: []AllLiquidation{{Symbol: "ETHUSDT", Side: "short", Price: 2500, Qty: 1.5, NotionalUSD: 3750, TimeMs: 1739502302929}},
		},
		{
			name: "pong and subscribe acks carry no prints",
			raw:  `{"success":true,"op":"pong"}`,
			want: nil,
		},
		{
			name: "malformed payload is ignored",
			raw:  `{`,
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseAllLiquidation([]byte(tc.raw))
			if len(got) != len(tc.want) {
				t.Fatalf("prints = %d, want %d", len(got), len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("print %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}
