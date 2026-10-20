package trader

import (
	"math"
	"strings"
	"testing"

	"nofx/store"
	"nofx/trader/connector"
	"time"
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
	res := EvaluateRiskGate(RiskGateInput{Action: "open_long", Confidence: 85, Symbol: "SOLUSDT", PositionSizeUSD: 500, Leverage: 10})
	if !res.Allowed {
		t.Fatalf("nil config should allow, got reject: %s", res.Reason)
	}
	if res.AdjustedSizeUSD != 500 {
		t.Fatalf("size = %v, want unchanged 500", res.AdjustedSizeUSD)
	}
}

func TestEvaluateRiskGateMaxPositions(t *testing.T) {
	in := RiskGateInput{Action: "open_long", Confidence: 85, Symbol: "SOLUSDT", PositionSizeUSD: 100, Leverage: 10,
		PositionCount: 3, Equity: 1000, AvailableBalance: 1000, Config: baseRiskConfig()}
	res := EvaluateRiskGate(in)
	if res.Allowed {
		t.Fatalf("expected reject at max positions")
	}
}

func TestEvaluateRiskGateDuplicatePosition(t *testing.T) {
	in := RiskGateInput{Action: "open_short", Confidence: 85, Symbol: "SOLUSDT", PositionSizeUSD: 100, Leverage: 10,
		PositionCount: 1, HasSamePosition: true, Equity: 1000, AvailableBalance: 1000, Config: baseRiskConfig()}
	res := EvaluateRiskGate(in)
	if res.Allowed {
		t.Fatalf("expected reject for duplicate position")
	}
}

func TestEvaluateRiskGatePositionValueCap(t *testing.T) {
	in := RiskGateInput{Action: "open_long", Confidence: 85, Symbol: "SOLUSDT", PositionSizeUSD: 5000, Leverage: 10,
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
	in := RiskGateInput{Action: "open_long", Confidence: 85, Symbol: "SOLUSDT", PositionSizeUSD: 5, Leverage: 10,
		Equity: 1000, AvailableBalance: 1000, Config: baseRiskConfig()}
	res := EvaluateRiskGate(in)
	if res.Allowed {
		t.Fatalf("expected reject below min position size")
	}
}

func TestEvaluateRiskGateMaxMarginUsage(t *testing.T) {
	cfg := baseRiskConfig()
	cfg.RiskControl.MaxMarginUsage = 0.9
	in := RiskGateInput{Action: "open_long", Confidence: 85, Symbol: "SOLUSDT", PositionSizeUSD: 950, Leverage: 1,
		Equity: 1000, AvailableBalance: 100000, Config: cfg}
	res := EvaluateRiskGate(in)
	if res.Allowed {
		t.Fatalf("expected reject: projected margin 95%% exceeds 90%% limit")
	}
}

func TestEvaluateRiskGateLeverageClamp(t *testing.T) {
	in := RiskGateInput{Action: "open_long", Confidence: 85, Symbol: "BTCUSDT", PositionSizeUSD: 100, Leverage: 20,
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
	in := RiskGateInput{Action: "open_long", Confidence: 85, Symbol: "BTCUSDT", PositionSizeUSD: 10000, Leverage: 10,
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

func TestEvaluateRiskGateUsesProvidedFeeRate(t *testing.T) {
	cfg := baseRiskConfig()
	cfg.RiskControl.AltcoinMaxLeverage = 20
	cfg.RiskControl.MaxMarginUsage = 1.0
	const feeRate = 0.01
	in := RiskGateInput{Action: "open_long", Confidence: 85, Symbol: "SOLUSDT", PositionSizeUSD: 1e9, Leverage: 20,
		Equity: 1e6, AvailableBalance: 100, TakerFeeRate: feeRate, Config: cfg}
	res := EvaluateRiskGate(in)
	if !res.Allowed {
		t.Fatalf("expected allow (auto-reduced), got: %s", res.Reason)
	}
	marginFactor := marginOverheadFactor/20 + feeRate
	want := 100 / marginFactor * positionSizeSafetyFactor
	if !almost(res.AdjustedSizeUSD, want) {
		t.Fatalf("size = %v, want %v (fee-rate-aware affordability)", res.AdjustedSizeUSD, want)
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

// The xyz dex (HIP-3) settles against its own USDC pool: sizing an xyz order
// off the combined account equity is what let funded-account orders reach the
// venue and get rejected for insufficient margin in the dex's own pool.
func TestAccountEquityAndAvailableForPools(t *testing.T) {
	balance := map[string]interface{}{
		"totalEquity":      1500.0, // perp + spot + xyz combined
		"availableBalance": 900.0,  // main perp pool only
		"xyzDexBalance":    500.0,  // xyz dex equity
		"xyzDexAvailable":  40.0,   // xyz dex free margin
	}

	// Non-xyz symbols keep the main-pool numbers.
	eq, avail := accountEquityAndAvailableFor(balance, "BTCUSDT")
	if eq != 1500.0 || avail != 900.0 {
		t.Fatalf("crypto pool = %.2f/%.2f, want 1500/900", eq, avail)
	}

	// xyz symbols switch to the dex pool for BOTH equity and available.
	eq, avail = accountEquityAndAvailableFor(balance, "xyz:SILVER")
	if eq != 500.0 || avail != 40.0 {
		t.Fatalf("xyz pool = %.2f/%.2f, want 500/40", eq, avail)
	}
}

// Missing xyz fields must not zero the numbers out (older venues/endpoints
// simply do not return them), and in that case the gate falls back to the
// main-pool figures rather than denying funding.
func TestAccountEquityAndAvailableForMissingXyzFields(t *testing.T) {
	balance := map[string]interface{}{
		"totalEquity":      1000.0,
		"availableBalance": 600.0,
	}
	eq, avail := accountEquityAndAvailableFor(balance, "xyz:GOLD")
	if eq != 1000.0 || avail != 600.0 {
		t.Fatalf("fallback = %.2f/%.2f, want 1000/600", eq, avail)
	}
}

// A zero max-margin-usage must NOT disable the cap: the gate normalizes it to
// the product default, so configs written by a path that skipped ClampLimits
// still cannot trade past the default limit.
func TestEvaluateRiskGateMarginUsageZeroValueStillEnforced(t *testing.T) {
	cfg := baseRiskConfig()
	cfg.RiskControl.MaxMarginUsage = 0 // previously skipped the whole check

	allowed := RiskGateInput{Action: "open_long", Confidence: 85, Symbol: "SOLUSDT", PositionSizeUSD: 800, Leverage: 1,
		Equity: 1000, AvailableBalance: 100000, Config: cfg}
	if res := EvaluateRiskGate(allowed); !res.Allowed {
		t.Fatalf("80%% projected margin should be allowed under the 90%% default, got: %s", res.Reason)
	}

	rejected := RiskGateInput{Action: "open_long", Confidence: 85, Symbol: "SOLUSDT", PositionSizeUSD: 950, Leverage: 1,
		Equity: 1000, AvailableBalance: 100000, Config: cfg}
	if res := EvaluateRiskGate(rejected); res.Allowed {
		t.Fatal("95% projected margin must be rejected under the 90% default")
	}
}

// An explicit user value is respected (and clamped) between 10% and 100%.
func TestEvaluateRiskGateMarginUsageEditableValue(t *testing.T) {
	cfg := baseRiskConfig()
	cfg.RiskControl.MaxMarginUsage = 0.3 // user set 30%

	in := RiskGateInput{Action: "open_long", Confidence: 85, Symbol: "SOLUSDT", PositionSizeUSD: 400, Leverage: 1,
		Equity: 1000, AvailableBalance: 100000, Config: cfg}
	res := EvaluateRiskGate(in)
	if res.Allowed {
		t.Fatal("40% projected margin must be rejected under an explicit 30% cap")
	}
}

// fee rate: prefer the venue's real rate for this account, fall back to the
// static model. A wrong fee rate skews the balance-sufficiency sizing.
type feeRateTraderStub struct {
	Trader // embedded (nil) — only TakerFeeRate is exercised
	rate   float64
}

func (s feeRateTraderStub) TakerFeeRate() float64 { return s.rate }

func TestTakerFeeRatePrefersVenueRate(t *testing.T) {
	at := &AutoTrader{trader: feeRateTraderStub{rate: 0.00023}, exchange: "hyperliquid"}
	if got := at.takerFeeRate(); got != 0.00023 {
		t.Fatalf("takerFeeRate() = %v, want the venue rate 0.00023", got)
	}

	// Unknown (0) falls back to the static model.
	at = &AutoTrader{trader: feeRateTraderStub{rate: 0}, exchange: "hyperliquid"}
	fallback := connector.FeeModelFor("hyperliquid").TakerRate()
	if got := at.takerFeeRate(); got != fallback {
		t.Fatalf("takerFeeRate() = %v, want the model fallback %v", got, fallback)
	}
}

// The minimum-hold rule blocks discretionary exits before the configured
// period; stop-loss/take-profit and the drawdown monitor close positions
// through other paths and are never delayed by it.
func TestCheckMinHold(t *testing.T) {
	trader := &AutoTrader{}
	now := time.Now().UTC().UnixMilli()

	// Without a strategy config there is nothing to enforce.
	if err := trader.checkMinHold(&store.TraderPosition{EntryTime: now}, "SOLUSDT", "LONG"); err != nil {
		t.Fatalf("missing config must allow closes: %v", err)
	}

	// 0 = off (the default): the operator opts in from the strategy studio.
	trader.config.StrategyConfig = &store.StrategyConfig{RiskControl: store.RiskControlConfig{}}
	if err := trader.checkMinHold(&store.TraderPosition{EntryTime: now - 5*60_000}, "SOLUSDT", "LONG"); err != nil {
		t.Fatalf("an unset minimum hold must not block: %v", err)
	}

	trader.config.StrategyConfig = &store.StrategyConfig{RiskControl: store.RiskControlConfig{MinHoldMinutes: 60}}
	young := &store.TraderPosition{EntryTime: now - 5*60_000}
	err := trader.checkMinHold(young, "SOLUSDT", "LONG")
	if err == nil {
		t.Fatal("a 5-minute-old position must not be closed under a 60-minute rule")
	}
	if !strings.Contains(err.Error(), "60 minutes") {
		t.Fatalf("error should state the requirement, got: %v", err)
	}

	old := &store.TraderPosition{EntryTime: now - 90*60_000}
	if err := trader.checkMinHold(old, "SOLUSDT", "LONG"); err != nil {
		t.Fatalf("a 90-minute-old position must be closable: %v", err)
	}

	// Missing entry time (untracked position) must not block a close.
	if err := trader.checkMinHold(&store.TraderPosition{}, "SOLUSDT", "LONG"); err != nil {
		t.Fatalf("unknown age must not block: %v", err)
	}
}

// The three causes behind a sub-minimum size need three different fixes. In
// particular a zero available margin must name the empty pool: reporting "0.00
// USDT below minimum" for an xyz symbol reads as if the model had sized nothing,
// while the actual fix is to transfer USDC into the independent xyz dex account.
func TestRiskGateExplainsWhySizeIsZero(t *testing.T) {
	cfg := baseRiskConfig()

	// 1. The decision did not size the trade.
	res := EvaluateRiskGate(RiskGateInput{
		Action: "open_long", Confidence: 85, Symbol: "xyz:CL", PositionSizeUSD: 0, Leverage: 3,
		Equity: 150, AvailableBalance: 150, Config: cfg,
	})
	if res.Allowed || !strings.Contains(res.Reason, "did not specify a position size") {
		t.Fatalf("zero-size decision: allowed=%v reason=%q", res.Allowed, res.Reason)
	}

	// 2. Zero available margin on an xyz symbol: the message must not assert a
	// separate pool (unified accounts do not have one), it must name both modes.
	res = EvaluateRiskGate(RiskGateInput{
		Action: "open_long", Confidence: 85, Symbol: "xyz:CL", PositionSizeUSD: 90, Leverage: 3,
		Equity: 150, AvailableBalance: 0, Config: cfg,
	})
	if res.Allowed {
		t.Fatal("zero margin must be rejected")
	}
	for _, want := range []string{"single USDC balance", "standard-mode accounts keep a separate xyz dex balance"} {
		if !strings.Contains(res.Reason, want) {
			t.Errorf("xyz zero-margin reason missing %q: %s", want, res.Reason)
		}
	}

	// 3. A crypto symbol with no margin says so without the xyz wording.
	res = EvaluateRiskGate(RiskGateInput{
		Action: "open_long", Confidence: 85, Symbol: "SOLUSDT", PositionSizeUSD: 90, Leverage: 3,
		Equity: 150, AvailableBalance: 0, Config: cfg,
	})
	if res.Allowed || !strings.Contains(res.Reason, "no available margin") || strings.Contains(res.Reason, "xyz dex") {
		t.Fatalf("crypto zero-margin reason = %q", res.Reason)
	}

	// 4. Margin exists but reduces the order below the minimum: explain the cut.
	res = EvaluateRiskGate(RiskGateInput{
		Action: "open_long", Confidence: 85, Symbol: "SOLUSDT", PositionSizeUSD: 90, Leverage: 10,
		Equity: 150, AvailableBalance: 1, Config: cfg,
	})
	if res.Allowed || !strings.Contains(res.Reason, "reduced to") {
		t.Fatalf("reduced-size reason = %q", res.Reason)
	}

	// 5. The plain case keeps its original wording.
	res = EvaluateRiskGate(RiskGateInput{
		Action: "open_long", Confidence: 85, Symbol: "SOLUSDT", PositionSizeUSD: 5, Leverage: 3,
		Equity: 150, AvailableBalance: 150, Config: cfg,
	})
	if res.Allowed || !strings.Contains(res.Reason, "below minimum") {
		t.Fatalf("plain min-size reason = %q", res.Reason)
	}
}

// Whether HIP-3 assets have their own collateral pool is an account-mode
// question (unified/portfolio vs manual-standard). A zero per-dex state must not
// be read as "no margin" — that mistake rejected a valid xyz:CL order on a
// unified account whose 150 USDC sat in the single perps balance.
func TestAccountPoolForXYZDexRespectsAccountMode(t *testing.T) {
	main := map[string]interface{}{
		"availableBalance": 150.55,
		"totalEquity":      150.55,
		// Unified/portfolio accounts report per-dex states as meaningless zeros.
		"xyzDexAvailable": 0.0,
		"xyzDexBalance":   0.0,
	}
	if equity, available := accountEquityAndAvailableFor(main, "xyz:CL"); equity != 150.55 || available != 150.55 {
		t.Fatalf("unified account: equity/available = %v/%v, want the single balance 150.55", equity, available)
	}

	// Keys absent entirely (non-Hyperliquid venues) behave the same way.
	if _, available := accountEquityAndAvailableFor(map[string]interface{}{
		"availableBalance": 150.55, "totalEquity": 150.55,
	}, "xyz:CL"); available != 150.55 {
		t.Fatalf("missing dex state: available = %v, want 150.55", available)
	}

	// Standard mode with a funded dex: the dex balance is authoritative.
	standard := map[string]interface{}{
		"availableBalance": 150.55,
		"totalEquity":      150.55,
		"xyzDexAvailable":  35.0,
		"xyzDexBalance":    40.0,
	}
	if equity, available := accountEquityAndAvailableFor(standard, "xyz:CL"); equity != 40 || available != 35 {
		t.Fatalf("standard mode: equity/available = %v/%v, want 40/35", equity, available)
	}

	// Crypto symbols never use the dex pool.
	if equity, available := accountEquityAndAvailableFor(standard, "BTCUSDT"); equity != 150.55 || available != 150.55 {
		t.Fatalf("crypto: equity/available = %v/%v, want the main balance", equity, available)
	}
}

// Confidence was documented as a floor for as long as the prompt has existed,
// but nothing enforced it: Decision.Confidence was only copied into the record,
// so a model could open at any confidence as long as size and R:R passed.
func TestEvaluateRiskGateConfidenceFloor(t *testing.T) {
	cfg := baseRiskConfig() // MinConfidence 0 -> product minimum (50)
	if res := EvaluateRiskGate(RiskGateInput{Action: "open_long", Symbol: "SOLUSDT", Confidence: 49,
		PositionSizeUSD: 100, Leverage: 5, Equity: 1000, AvailableBalance: 1000, Config: cfg}); res.Allowed {
		t.Fatal("49 must be rejected under the 50 product minimum")
	}
	if res := EvaluateRiskGate(RiskGateInput{Action: "open_long", Symbol: "SOLUSDT", Confidence: 50,
		PositionSizeUSD: 100, Leverage: 5, Equity: 1000, AvailableBalance: 1000, Config: cfg}); !res.Allowed {
		t.Fatalf("50 should pass the product minimum, got: %s", res.Reason)
	}

	cfg.RiskControl.MinConfidence = 80
	if res := EvaluateRiskGate(RiskGateInput{Action: "open_short", Symbol: "SOLUSDT", Confidence: 79,
		PositionSizeUSD: 100, Leverage: 5, Equity: 1000, AvailableBalance: 1000, Config: cfg}); res.Allowed {
		t.Fatal("79 must be rejected under an explicit 80 floor")
	}
	if res := EvaluateRiskGate(RiskGateInput{Action: "open_short", Symbol: "SOLUSDT", Confidence: 80,
		PositionSizeUSD: 100, Leverage: 5, Equity: 1000, AvailableBalance: 1000, Config: cfg}); !res.Allowed {
		t.Fatalf("80 should pass an explicit 80 floor, got: %s", res.Reason)
	}
}
