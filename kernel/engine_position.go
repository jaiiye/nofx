package kernel

import (
	"fmt"
	"math"
	"nofx/logger"
	"nofx/market"
)

// ============================================================================
// Decision Validation
// ============================================================================

// normalizeMinRewardRatio maps an unset/invalid configured minimum to the
// product default (3:1).
func normalizeMinRewardRatio(v float64) float64 {
	if v <= 0 {
		return 3.0
	}
	return v
}

// validateDecisions validates every decision. entryPriceFor resolves the
// current market price for a symbol (used for the risk/reward check); when nil
// or returning 0 the risk/reward check is skipped for that symbol.
func validateDecisions(decisions []Decision, accountEquity float64, btcEthLeverage, altcoinLeverage int, btcEthPosRatio, altcoinPosRatio float64, entryPriceFor func(symbol string) float64, minRewardRatio float64) error {
	minRewardRatio = normalizeMinRewardRatio(minRewardRatio)
	for i := range decisions {
		if err := validateDecision(&decisions[i], accountEquity, btcEthLeverage, altcoinLeverage, btcEthPosRatio, altcoinPosRatio, entryPriceFor, minRewardRatio); err != nil {
			return fmt.Errorf("decision #%d validation failed: %w", i+1, err)
		}
	}
	return nil
}

func validateDecision(d *Decision, accountEquity float64, btcEthLeverage, altcoinLeverage int, btcEthPosRatio, altcoinPosRatio float64, entryPriceFor func(symbol string) float64, minRewardRatio float64) error {
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
		maxLeverage := altcoinLeverage
		posRatio := altcoinPosRatio
		maxPositionValue := accountEquity * posRatio
		isMajor := d.Symbol == "BTCUSDT" || d.Symbol == "ETHUSDT" || market.IsXyzDexAsset(d.Symbol)
		if isMajor {
			maxLeverage = btcEthLeverage
			posRatio = btcEthPosRatio
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

		// Risk/reward is measured against the ACTUAL entry (current market
		// price), not a synthesized one. The previous version derived the entry
		// as SL + 20% of the SL->TP span, which made the ratio a tautological
		// 4.0:1 — every SL/TP pair passed, including targets closer to price
		// than the stop (a live decision on xyz:SILVER passed with 0.24:1).
		entryPrice := 0.0
		if entryPriceFor != nil {
			entryPrice = entryPriceFor(d.Symbol)
		}
		if entryPrice <= 0 {
			logger.Infof("⚠️  risk/reward check skipped for %s: no market price available", d.Symbol)
			return nil
		}

		risk := math.Abs(entryPrice - d.StopLoss)
		reward := math.Abs(d.TakeProfit - entryPrice)
		if risk <= 0 {
			return fmt.Errorf("stop loss must differ from the entry price (entry %.4f, stop loss %.4f)", entryPrice, d.StopLoss)
		}
		ratio := reward / risk
		if ratio < minRewardRatio {
			return fmt.Errorf("risk/reward ratio too low (%.2f:1), must be ≥%.1f:1 [entry %.4f stop loss %.4f take profit %.4f → risk %.4f reward %.4f]",
				ratio, minRewardRatio, entryPrice, d.StopLoss, d.TakeProfit, risk, reward)
		}
	}

	return nil
}
