package trader

import (
	"math"
	"testing"

	"nofx/store"
)

func almost(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func baseRiskConfig() *store.StrategyConfig {
	return &store.StrategyConfig{
		RiskControl: store.RiskControlConfig{
			MaxPositions:                 3,
			BTCETHMaxLeverage:            5,
			AltcoinMaxLeverage:           5,
			BTCETHMaxPositionValueRatio:  5,
			AltcoinMaxPositionValueRatio: 1,
			MaxMarginUsage:               1.0,
			MinPositionSize:              12,
		},
	}
}

func TestEvaluateRiskGateNilConfigAllows(t *testing.T) {
	res := EvaluateRiskGate(RiskGateInput{Action: "open_long", Symbol: "SOLUSDT", PositionSizeUSD: 500, Leverage: 10})
	if !res.Allowed {
		t.Fatalf("nil config should allow, got reject: %s", res.Reason)
	}
	if res.AdjustedSizeUSD != 500 {
		t.Fatalf("size = %v, want unchanged 500", res.AdjustedSizeUSD)
	}
}

func TestEvaluateRiskGateMaxPositions(t *testing.T) {
	in := RiskGateInput{Action: "open_long", Symbol: "SOLUSDT", PositionSizeUSD: 100, Leverage: 10,
		PositionCount: 3, Equity: 1000, AvailableBalance: 1000, Config: baseRiskConfig()}
	res := EvaluateRiskGate(in)
	if res.Allowed {
		t.Fatalf("expected reject at max positions")
	}
}

func TestEvaluateRiskGateDuplicatePosition(t *testing.T) {
	in := RiskGateInput{Action: "open_short", Symbol: "SOLUSDT", PositionSizeUSD: 100, Leverage: 10,
		PositionCount: 1, HasSamePosition: true, Equity: 1000, AvailableBalance: 1000, Config: baseRiskConfig()}
	res := EvaluateRiskGate(in)
	if res.Allowed {
		t.Fatalf("expected reject for duplicate position")
	}
}

func TestEvaluateRiskGatePositionValueCap(t *testing.T) {
	in := RiskGateInput{Action: "open_long", Symbol: "SOLUSDT", PositionSizeUSD: 5000, Leverage: 10,
		Equity: 1000, AvailableBalance: 100000, Config: baseRiskConfig()}
	res := EvaluateRiskGate(in)
	if !res.Allowed {
		t.Fatalf("expected allow, got: %s", res.Reason)
	}
	if !almost(res.AdjustedSizeUSD, 1000) {
		t.Fatalf("size = %v, want capped to 1000 (equity 1000 x ratio 1)", res.AdjustedSizeUSD)
	}
}

func TestEvaluateRiskGateMinSizeReject(t *testing.T) {
	in := RiskGateInput{Action: "open_long", Symbol: "SOLUSDT", PositionSizeUSD: 5, Leverage: 10,
		Equity: 1000, AvailableBalance: 1000, Config: baseRiskConfig()}
	res := EvaluateRiskGate(in)
	if res.Allowed {
		t.Fatalf("expected reject below min position size")
	}
}

func TestEvaluateRiskGateMaxMarginUsage(t *testing.T) {
	cfg := baseRiskConfig()
	cfg.RiskControl.MaxMarginUsage = 0.9
	in := RiskGateInput{Action: "open_long", Symbol: "SOLUSDT", PositionSizeUSD: 950, Leverage: 1,
		Equity: 1000, AvailableBalance: 100000, Config: cfg}
	res := EvaluateRiskGate(in)
	if res.Allowed {
		t.Fatalf("expected reject: projected margin 95%% exceeds 90%% limit")
	}
}

func TestEvaluateRiskGateLeverageClamp(t *testing.T) {
	in := RiskGateInput{Action: "open_long", Symbol: "BTCUSDT", PositionSizeUSD: 100, Leverage: 20,
		Equity: 100000, AvailableBalance: 100000, Config: baseRiskConfig()}
	res := EvaluateRiskGate(in)
	if !res.Allowed {
		t.Fatalf("expected allow, got: %s", res.Reason)
	}
	if res.AdjustedLeverage != 5 {
		t.Fatalf("leverage = %d, want clamped to 5", res.AdjustedLeverage)
	}
}

func TestEvaluateRiskGateBalanceSufficiency(t *testing.T) {
	// Position value cap is generous for BTC; available balance is the binding
	// constraint here. Raise the leverage cap so the clamp doesn't interfere.
	cfg := baseRiskConfig()
	cfg.RiskControl.BTCETHMaxLeverage = 20
	in := RiskGateInput{Action: "open_long", Symbol: "BTCUSDT", PositionSizeUSD: 10000, Leverage: 10,
		Equity: 100000, AvailableBalance: 100, Config: cfg}
	res := EvaluateRiskGate(in)
	if !res.Allowed {
		t.Fatalf("expected allow (auto-reduced), got: %s", res.Reason)
	}
	marginFactor := marginOverheadFactor/10 + takerFeeRate
	want := 100 / marginFactor * positionSizeSafetyFactor
	if !almost(res.AdjustedSizeUSD, want) {
		t.Fatalf("size = %v, want auto-reduced to %v", res.AdjustedSizeUSD, want)
	}
}

func TestHasPosition(t *testing.T) {
	positions := []map[string]interface{}{
		{"symbol": "SOLUSDT", "side": "long"},
	}
	if !hasPosition(positions, "SOLUSDT", "open_long") {
		t.Fatalf("expected to find SOLUSDT long")
	}
	if hasPosition(positions, "SOLUSDT", "open_short") {
		t.Fatalf("did not expect SOLUSDT short")
	}
}

func TestMarginUsagePct(t *testing.T) {
	positions := []map[string]interface{}{
		{"symbol": "BTCUSDT", "side": "long", "positionAmt": 1.0, "markPrice": 100.0, "leverage": 10.0},
	}
	// margin = 1*100/10 = 10; /equity 100 => 10%.
	if got := marginUsagePct(positions, 100); !almost(got, 10) {
		t.Fatalf("marginUsagePct = %v, want 10", got)
	}
	if got := marginUsagePct(positions, 0); got != 0 {
		t.Fatalf("marginUsagePct with zero equity = %v, want 0", got)
	}
}

func TestAccountEquityAndAvailable(t *testing.T) {
	eq, avail := accountEquityAndAvailable(map[string]interface{}{
		"totalEquity":      1200.0,
		"availableBalance": 800.0,
	})
	if eq != 1200 || avail != 800 {
		t.Fatalf("equity/avail = %v/%v, want 1200/800", eq, avail)
	}
	// Fallback to wallet balance when totalEquity is absent.
	eq, _ = accountEquityAndAvailable(map[string]interface{}{"totalWalletBalance": 500.0})
	if eq != 500 {
		t.Fatalf("equity fallback = %v, want 500", eq)
	}
}

func TestNormalizePosKey(t *testing.T) {
	if normalizePosKey("BTCUSDT", "long") != normalizePosKey("BTCUSDT", "LONG") {
		t.Fatalf("pos key should be case-insensitive on side")
	}
}
