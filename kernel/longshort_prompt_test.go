package kernel

import (
	"strings"
	"testing"

	"nofx/provider/nofxos"
	"nofx/store"
)

func longShortEngine(t *testing.T) *StrategyEngine {
	t.Helper()
	cfg := store.GetDefaultStrategyConfig("en")
	cfg.Indicators.EnableQuantData = true
	cfg.Indicators.EnableQuantOI = true
	return NewStrategyEngine(&cfg)
}

// The long/short exposure ships inside the OI payload; the conversion used to
// drop it, so the model never saw a signal the client had already fetched.
func TestConvertQuantDataCarriesLongShort(t *testing.T) {
	got := convertQuantData(&nofxos.QuantData{
		Symbol: "BTCUSDT",
		Price:  63000,
		OI: map[string]*nofxos.OIData{
			"binance": {
				CurrentOI: 5_000_000,
				NetLong:   120,
				NetShort:  60,
				Delta: map[string]*nofxos.OIDeltaData{
					"1h": {OIDelta: 1, OIDeltaValue: 1000, OIDeltaPercent: 2.5},
				},
			},
		},
	})

	if got == nil {
		t.Fatal("expected converted data")
	}
	oi := got.OI["binance"]
	if oi == nil {
		t.Fatal("binance OI missing")
	}
	if oi.NetLong != 120 || oi.NetShort != 60 {
		t.Fatalf("long/short = %v/%v, want 120/60", oi.NetLong, oi.NetShort)
	}
	if oi.Delta["1h"] == nil || oi.Delta["1h"].OIDeltaPercent != 2.5 {
		t.Fatalf("delta mapping regressed: %+v", oi.Delta)
	}
}

func TestFormatQuantDataRendersLongShort(t *testing.T) {
	engine := longShortEngine(t)
	got := engine.formatQuantData(&QuantData{
		Symbol: "BTCUSDT",
		OI: map[string]*OIData{
			"binance": {CurrentOI: 5_000_000, NetLong: 120, NetShort: 60},
		},
	})

	if !strings.Contains(got, "Long/Short (binance): long 120.00 / short 60.00 = 2.00x") {
		t.Fatalf("long/short line missing:\n%s", got)
	}
}

func TestFormatQuantDataRendersAllLongWithoutRatio(t *testing.T) {
	engine := longShortEngine(t)
	got := engine.formatQuantData(&QuantData{
		Symbol: "BTCUSDT",
		OI: map[string]*OIData{
			"binance": {NetLong: 120},
		},
	})

	if !strings.Contains(got, "long 120.00 / short 0.00 = all long") {
		t.Fatalf("all-long line missing:\n%s", got)
	}
}

// A venue that reports neither side must not produce a fabricated 0/0 line.
func TestFormatQuantDataOmitsZeroLongShort(t *testing.T) {
	engine := longShortEngine(t)
	got := engine.formatQuantData(&QuantData{
		Symbol: "BTCUSDT",
		OI: map[string]*OIData{
			"binance": {CurrentOI: 5_000_000, Delta: map[string]*OIDeltaData{
				"1h": {OIDeltaPercent: 1.2, OIDeltaValue: 500},
			}},
		},
	})

	if strings.Contains(got, "Long/Short") {
		t.Fatalf("long/short line must be omitted when both sides are zero:\n%s", got)
	}
	if !strings.Contains(got, "Open Interest (binance)") {
		t.Fatalf("OI block should still render:\n%s", got)
	}
}

func TestLongShortRatio(t *testing.T) {
	cases := []struct {
		long, short float64
		want        string
		ok          bool
	}{
		{120, 60, "2.00x", true},
		{60, 120, "0.50x", true},
		{10, 0, "all long", true},
		{0, 0, "", false},
	}
	for _, tc := range cases {
		got, ok := longShortRatio(tc.long, tc.short)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("longShortRatio(%v, %v) = %q, %v; want %q, %v", tc.long, tc.short, got, ok, tc.want, tc.ok)
		}
	}
}
