package kernel

import (
	"fmt"
	"math"
	"nofx/logger"
	"nofx/market"
	"nofx/store"
)

// ============================================================================
// Decision Validation
// ============================================================================

// entryContext carries the market state the entry gates need. It is passed
// explicitly rather than read from package state so validation stays a pure
// function of its inputs, which keeps it testable without a live exchange.
//
// A nil or empty context means "no market snapshot available": the structural
// checks (position size, leverage, risk/reward) still run, but the indicator
// gates are skipped with a warning rather than silently passing or failing.
type entryContext struct {
	// Price is the latest close on the primary timeframe.
	Price float64
	// EMA200 is the long-term trend line. 0 means "not enough history".
	EMA200 float64
	// ADX14 is the trend-strength reading. 0 means "not enough history".
	ADX14 float64
	// KCUpper/KCMiddle/KCLower are the Keltner bands.
	KCUpper  float64
	KCMiddle float64
	KCLower  float64
	// ATR14 backs both the stop-distance check and the position size.
	ATR14 float64

	// marketDataBySymbol lets validation resolve the snapshot for whichever
	// symbol the model actually decided on. A batch decision covers many
	// symbols, so a single flat snapshot cannot serve them all.
	marketDataBySymbol map[string]*market.Data
	// primaryTimeframe selects which series to read from each symbol's data.
	primaryTimeframe string
}

// forSymbol narrows a batch context down to one symbol's snapshot. The gate
// checks then read plain scalars, which keeps them easy to test.
//
// A context that already carries flat scalars (built directly by a caller, as
// the tests do) is returned unchanged: only batch contexts with a lookup map
// need resolving. Discarding the flat values here would silently disable every
// gate, which is exactly the failure mode this guards against.
func (c entryContext) forSymbol(symbol string) entryContext {
	if c.marketDataBySymbol == nil {
		return c
	}
	resolved := entryContextForSymbol(c, symbol)
	return resolved
}

// hasTrendData reports whether enough indicator history exists to evaluate the
// gates that this strategy has enabled.
//
// It checks only the indicators the enabled gates actually read. Testing every
// field unconditionally would make an unrelated gate veto the whole check: with
// EnableEMA off the EMA200 is never populated, so requiring EMA200 > 0 would
// return false and silence the ADX and Keltner gates too — gates the user had
// switched on. A gate that is off must not be able to disable one that is on.
//
// A genuinely missing reading for an enabled gate still degrades to a warning
// rather than a rejection, because a zero means "not enough history" (a 200-bar
// warmup on a freshly seen symbol), not "condition failed".
func (c entryContext) hasTrendData(indicators store.IndicatorConfig) bool {
	if indicators.EnableEMA && c.EMA200 <= 0 {
		return false
	}
	if indicators.EnableADX && c.ADX14 <= 0 {
		return false
	}
	if indicators.EnableKeltner && c.KCUpper <= 0 && c.KCLower <= 0 {
		return false
	}
	return true
}

func validateDecisions(decisions []Decision, accountEquity float64, risk store.RiskControlConfig, indicators store.IndicatorConfig, entry entryContext) error {
	for i := range decisions {
		if err := validateDecision(&decisions[i], accountEquity, risk, indicators, entry); err != nil {
			return fmt.Errorf("decision #%d validation failed: %w", i+1, err)
		}
	}
	return nil
}

func validateDecision(d *Decision, accountEquity float64, risk store.RiskControlConfig, indicators store.IndicatorConfig, entry entryContext) error {
	validActions := map[string]bool{
		"open_long":   true,
		"open_short":  true,
		"close_long":  true,
		"close_short": true,
		"hold":        true,
		"wait":        true,
	}

	if !validActions[d.Action] {
		return fmt.Errorf("invalid action: %s", d.Action)
	}

	if d.Action == "open_long" || d.Action == "open_short" {
		// Asset tiering for validation:
		//   - BTC/ETH crypto perps use the BTC/ETH tier (typically 5x equity).
		//   - Hyperliquid XYZ assets (US equities, commodities, forex) are
		//     also treated as the higher tier — they are not crypto altcoins
		//     and the user's quick-trade flow shows them at the higher cap,
		//     so the validator must match.
		//   - Everything else is altcoin (1x equity by default).
		maxLeverage := risk.AltcoinMaxLeverage
		posRatio := risk.AltcoinMaxPositionValueRatio
		maxPositionValue := accountEquity * posRatio
		isMajor := d.Symbol == "BTCUSDT" || d.Symbol == "ETHUSDT" || market.IsXyzDexAsset(d.Symbol)
		if isMajor {
			maxLeverage = risk.BTCETHMaxLeverage
			posRatio = risk.BTCETHMaxPositionValueRatio
			maxPositionValue = accountEquity * posRatio
		}

		if d.Leverage <= 0 {
			return fmt.Errorf("leverage must be greater than 0: %d", d.Leverage)
		}
		if d.Leverage > maxLeverage {
			logger.Infof("⚠️  [Leverage Fallback] %s leverage exceeded (%dx > %dx), auto-adjusting to limit %dx",
				d.Symbol, d.Leverage, maxLeverage, maxLeverage)
			d.Leverage = maxLeverage
		}
		if d.PositionSizeUSD <= 0 {
			return fmt.Errorf("position size must be greater than 0: %.2f", d.PositionSizeUSD)
		}

		const minPositionSizeGeneral = 12.0
		const minPositionSizeBTCETH = 60.0

		if d.Symbol == "BTCUSDT" || d.Symbol == "ETHUSDT" {
			if d.PositionSizeUSD < minPositionSizeBTCETH {
				return fmt.Errorf("%s opening amount too small (%.2f USDT), must be ≥%.2f USDT", d.Symbol, d.PositionSizeUSD, minPositionSizeBTCETH)
			}
		} else {
			if d.PositionSizeUSD < minPositionSizeGeneral {
				return fmt.Errorf("opening amount too small (%.2f USDT), must be ≥%.2f USDT", d.PositionSizeUSD, minPositionSizeGeneral)
			}
		}

		tolerance := maxPositionValue * 0.01
		if d.PositionSizeUSD > maxPositionValue+tolerance {
			switch {
			case d.Symbol == "BTCUSDT" || d.Symbol == "ETHUSDT":
				return fmt.Errorf("BTC/ETH single coin position value cannot exceed %.0f USDT (%.1fx account equity), actual: %.0f", maxPositionValue, posRatio, d.PositionSizeUSD)
			case market.IsXyzDexAsset(d.Symbol):
				return fmt.Errorf("%s position value cannot exceed %.0f USDT (%.1fx account equity), actual: %.0f", d.Symbol, maxPositionValue, posRatio, d.PositionSizeUSD)
			default:
				return fmt.Errorf("altcoin single coin position value cannot exceed %.0f USDT (%.1fx account equity), actual: %.0f", maxPositionValue, posRatio, d.PositionSizeUSD)
			}
		}
		if d.StopLoss <= 0 || d.TakeProfit <= 0 {
			return fmt.Errorf("stop loss and take profit must be greater than 0")
		}

		if d.Action == "open_long" {
			if d.StopLoss >= d.TakeProfit {
				return fmt.Errorf("for long positions, stop loss price must be less than take profit price")
			}
		} else {
			if d.StopLoss <= d.TakeProfit {
				return fmt.Errorf("for short positions, stop loss price must be greater than take profit price")
			}
		}

		var entryPrice float64
		if d.Action == "open_long" {
			entryPrice = d.StopLoss + (d.TakeProfit-d.StopLoss)*0.2
		} else {
			entryPrice = d.StopLoss - (d.StopLoss-d.TakeProfit)*0.2
		}

		var riskPercent, rewardPercent, riskRewardRatio float64
		if d.Action == "open_long" {
			riskPercent = (entryPrice - d.StopLoss) / entryPrice * 100
			rewardPercent = (d.TakeProfit - entryPrice) / entryPrice * 100
			if riskPercent > 0 {
				riskRewardRatio = rewardPercent / riskPercent
			}
		} else {
			riskPercent = (d.StopLoss - entryPrice) / entryPrice * 100
			rewardPercent = (entryPrice - d.TakeProfit) / entryPrice * 100
			if riskPercent > 0 {
				riskRewardRatio = rewardPercent / riskPercent
			}
		}

		minRiskReward := risk.MinRiskRewardRatio
		if minRiskReward <= 0 {
			minRiskReward = 3.0 // Fallback when the strategy leaves it unset
		}
		if riskRewardRatio < minRiskReward {
			return fmt.Errorf("risk/reward ratio too low (%.2f:1), must be ≥%.1f:1 [risk: %.2f%% reward: %.2f%%] [stop loss: %.2f take profit: %.2f]",
				riskRewardRatio, minRiskReward, riskPercent, rewardPercent, d.StopLoss, d.TakeProfit)
		}

		// --- Entry gates -------------------------------------------------
		// Confidence is deliberately NOT a gate any more. It was a model
		// self-assessment, which is poorly calibrated, and it was competing
		// with the objective rules below: a model that scored a valid breakout
		// at 60 would have had a structurally sound entry rejected. The score
		// is still recorded on the decision for post-hoc review.
		if err := checkEntryGates(d, indicators, entry); err != nil {
			return err
		}

		// --- Risk-based position sizing ----------------------------------
		// The position must not risk more than the configured share of equity
		// at the stated stop. This is what turns stop_loss from a suggestion
		// into the thing that actually determines size.
		if err := checkRiskBasedSize(d, accountEquity, risk, entry); err != nil {
			return err
		}
	}

	return nil
}

// checkEntryGates enforces the trend direction, chop filter and breakout
// trigger.
//
// Gating is staged deliberately: the indicator rules shipped in log-only mode
// first so their real-world pass rate could be measured before they were
// allowed to block orders. Three conditions in AND rarely align, and enabling
// the block immediately risked a strategy that never opens a position.
func checkEntryGates(d *Decision, indicators store.IndicatorConfig, entry entryContext) error {
	// Resolve the snapshot for the symbol this decision is actually about.
	entry = entry.forSymbol(d.Symbol)
	if !entry.hasTrendData(indicators) {
		logger.Infof("⚠️  [Entry Gate] Skipped for %s: an enabled gate has no reading yet "+
			"(EMA200=%.4f, ADX=%.2f, KC=%.4f/%.4f). Structural checks still applied.",
			d.Symbol, entry.EMA200, entry.ADX14, entry.KCUpper, entry.KCLower)
		return nil
	}

	if !indicators.EntryGatesEnforced {
		if reason := evaluateEntryGates(d, indicators, entry); reason != "" {
			logger.Infof("📋 [Entry Gate][LOG-ONLY] %s would be blocked: %s", d.Symbol, reason)
		}
		return nil
	}

	if reason := evaluateEntryGates(d, indicators, entry); reason != "" {
		return fmt.Errorf("%s blocked: %s", d.Symbol, reason)
	}
	return nil
}

// evaluateEntryGates runs the trend/chop/breakout rules and returns the first
// failure reason, or "" when the setup is acceptable.
//
// It is separated from the enforcement decision so the same logic can run in
// log-only mode: the reason text is produced either way, and only the caller
// decides whether it is fatal. Keeping one implementation means the logged
// reason and the enforced reason can never drift apart.
func evaluateEntryGates(d *Decision, indicators store.IndicatorConfig, entry entryContext) string {

	// Gate 1: long-term trend direction. Longs require price above the EMA200,
	// shorts below it. Only the price/EMA200 relationship is tested.
	//
	// An earlier revision also required EMA50 to agree with EMA200 as a
	// confirmation. That layer was dropped: it was never part of the intended
	// strategy (which specifies a single long-term average), and because EMA50
	// and price are both lagging views of the same trend, the extra condition
	// added little filter value while making it harder to explain why an
	// otherwise valid breakout was refused.
	if indicators.EnableEMA {
		if d.Action == "open_long" {
			if entry.Price <= entry.EMA200 {
				return fmt.Sprintf("price %.4f is below the EMA200 trend line %.4f (long entries require an uptrend)",
					entry.Price, entry.EMA200)
			}
		} else {
			if entry.Price >= entry.EMA200 {
				return fmt.Sprintf("price %.4f is above the EMA200 trend line %.4f (short entries require a downtrend)",
					entry.Price, entry.EMA200)
			}
		}
	}

	// Gate 2: chop filter. A low ADX means the market is ranging, where
	// breakout entries are the classic way to get chopped up.
	if indicators.EnableADX {
		threshold := float64(indicators.EffectiveADXThreshold())
		if entry.ADX14 < threshold {
			return fmt.Sprintf("ADX %.2f is below the trend threshold %.0f (market is ranging, breakout entries are unreliable)",
				entry.ADX14, threshold)
		}
	}

	// Gate 3: breakout trigger. The entry must actually be a breakout, not a
	// mid-channel entry that merely happens to have a stop attached.
	if indicators.EnableKeltner && entry.KCUpper > 0 && entry.KCLower > 0 {
		if d.Action == "open_long" && entry.Price <= entry.KCUpper {
			return fmt.Sprintf("price %.4f has not broken the Keltner upper band %.4f",
				entry.Price, entry.KCUpper)
		}
		if d.Action == "open_short" && entry.Price >= entry.KCLower {
			return fmt.Sprintf("price %.4f has not broken the Keltner lower band %.4f",
				entry.Price, entry.KCLower)
		}
	}

	return ""
}

// checkRiskBasedSize verifies the requested position does not exceed the
// configured per-trade risk budget at the stated stop distance.
//
// Sizing is derived, not provider-supplied: the model states a stop, and the
// maximum permissible size follows from how much equity we are willing to lose
// if that stop is hit. Without this the model could pair a very tight stop with
// an oversized position and risk far more than intended.
func checkRiskBasedSize(d *Decision, accountEquity float64, risk store.RiskControlConfig, entry entryContext) error {
	riskPct := risk.EffectiveRiskPerTradePct()
	if riskPct <= 0 || accountEquity <= 0 {
		return nil // Risk sizing disabled for this strategy.
	}

	// Prefer the live price when available; the entry estimate is a fallback
	// used only because validation may run before market data is attached.
	referencePrice := entry.Price
	if referencePrice <= 0 {
		if d.Action == "open_long" {
			referencePrice = d.StopLoss + (d.TakeProfit-d.StopLoss)*0.2
		} else {
			referencePrice = d.StopLoss - (d.StopLoss-d.TakeProfit)*0.2
		}
	}
	if referencePrice <= 0 {
		return nil
	}

	stopDistance := math.Abs(referencePrice - d.StopLoss)
	if stopDistance <= 0 {
		return nil // A zero stop distance is caught by the price-ordering checks.
	}

	maxRiskUSD := accountEquity * riskPct / 100
	// Loss if the stop is hit, expressed in USD.
	impliedRiskUSD := d.PositionSizeUSD * stopDistance / referencePrice

	// A small tolerance keeps rounding in the model's arithmetic from tripping
	// the check on an otherwise compliant order.
	const tolerance = 1.01
	if impliedRiskUSD > maxRiskUSD*tolerance {
		maxAllowedSize := maxRiskUSD * referencePrice / stopDistance
		return fmt.Errorf(
			"%s risks %.2f USDT at the stated stop, above the %.2f USDT budget (%.2fx equity); "+
				"reduce position_size_usd to at most %.2f or widen the stop",
			d.Symbol, impliedRiskUSD, maxRiskUSD, riskPct/100, maxAllowedSize)
	}

	return nil
}
