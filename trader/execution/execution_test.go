package execution

import (
	"fmt"
	"testing"

	"nofx/trader/connector"
)

func TestSimulatedModeFillsAtPriceWithFee(t *testing.T) {
	m := NewSimulatedMode(
		func(symbol string) (float64, error) { return 100, nil },
		connector.StaticFeeModel{Taker: 0.001, Asset: "USDT"},
	)

	res, err := m.OpenLong("BTCUSDT", 2, 10)
	if err != nil {
		t.Fatalf("OpenLong: %v", err)
	}
	if res.Status != "FILLED" || res.AvgFillPrice != 100 || res.FilledQty != 2 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.Fee != 0.2 { // 100 * 2 * 0.001
		t.Fatalf("fee = %v, want 0.2", res.Fee)
	}
	if res.OrderID == "" {
		t.Fatal("expected a generated order id")
	}

	// IDs are unique across calls.
	res2, _ := m.OpenShort("BTCUSDT", 1, 10)
	if res2.OrderID == res.OrderID {
		t.Fatalf("expected distinct order ids, both %q", res.OrderID)
	}
}

func TestSimulatedModeNilPriceSource(t *testing.T) {
	m := NewSimulatedMode(nil, nil)
	if _, err := m.OpenLong("X", 1, 1); err == nil {
		t.Fatal("expected error when no price source is configured")
	}
}

func TestSimulatedModePriceErrorPropagates(t *testing.T) {
	m := NewSimulatedMode(
		func(symbol string) (float64, error) { return 0, fmt.Errorf("boom") },
		nil,
	)
	if _, err := m.CloseLong("X", 1); err == nil {
		t.Fatal("expected price error to propagate")
	}
}
