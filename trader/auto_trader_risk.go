package trader

import (
	"fmt"
	"nofx/logger"
	"nofx/market"
	"nofx/store"
	"strings"
	"time"
)

// dailyLossHaltLimit returns the daily net-loss fraction (0.10 = 10%) at
// which trading pauses. Configured values are fractions (0.1 = 10%, negative
// normalized); a percentage-style value (>1) is divided by 100. Defaults to
// 10%, matching the default advertised to the AI in the kernel schema.
func (at *AutoTrader) dailyLossHaltLimit() float64 {
	v := at.config.MaxDailyLoss
	if v < 0 {
		v = -v
	}
	if v > 0 && v <= 1 {
		return v
	}
	if v > 1 {
		return v / 100
	}
	return 0.10
}

// checkDailyLossHalt pauses trading once the day's net realized loss breaches
// the configured fraction of the initial balance. The halt relies on the
// position records every close path lands in (direct recording or OrderSync).
// Returns true when trading is paused.
func (at *AutoTrader) checkDailyLossHalt(record *store.DecisionRecord) bool {
	if at.store == nil || at.initialBalance <= 0 {
		return false
	}
	netPnL, err := at.store.Position().GetNetRealizedPnLSince(at.id, at.lastResetTime.UTC().UnixMilli())
	if err != nil {
		// Fail-open: this halt is a comfort layer on top of the fail-closed
		// position-level protections; a query gap must not freeze trading.
		at.logWarnf("⚠ Daily loss check failed, skipping: %v", err)
		return false
	}
	at.dailyPnL = netPnL

	limit := at.dailyLossHaltLimit()
	if netPnL > -at.initialBalance*limit {
		return false
	}

	pause := at.config.StopTradingTime
	if pause <= 0 {
		// Hold until the daily window rolls over (24h cadence).
		pause = 24 * time.Hour
	}
	at.stopUntil = time.Now().Add(pause)
	at.logWarnf("🛑 Daily loss halt: net realized PnL %.2f USDT (%.1f%%) breached the %.1f%% limit — pausing trading until %s",
		netPnL, netPnL/at.initialBalance*100, limit*100, at.stopUntil.Format("2006-01-02 15:04"))
	record.Success = false
	record.ErrorMessage = fmt.Sprintf("Daily loss halt: net realized PnL %.2f USDT (%.1f%%) breaches the %.1f%% daily limit",
		netPnL, netPnL/at.initialBalance*100, limit*100)
	return true
}

// drawdownSettings is the resolved profit-protection monitor configuration.
type drawdownSettings struct {
	enabled        bool
	interval       time.Duration
	minProfitPct   float64 // start monitoring above this profit (%)
	closeThreshold float64 // close when profit drops this far from peak (%)
}

// drawdownMonitorSettings resolves the monitor settings from the strategy
// config. Nil enabled flag and zero thresholds keep the historical defaults,
// so configs stored before these fields existed stay active and unchanged.
func (at *AutoTrader) drawdownMonitorSettings() drawdownSettings {
	s := drawdownSettings{
		enabled:        true,
		interval:       time.Minute,
		minProfitPct:   5.0,
		closeThreshold: 40.0,
	}
	sc := at.GetStrategyConfig()
	if sc == nil {
		return s
	}
	rc := sc.RiskControl
	if rc.DrawdownMonitoringEnabled != nil {
		s.enabled = *rc.DrawdownMonitoringEnabled
	}
	intervalSec := rc.DrawdownCheckIntervalSec
	if intervalSec <= 0 {
		intervalSec = 60
	}
	if intervalSec < 15 {
		intervalSec = 15
	}
	if intervalSec > 300 {
		intervalSec = 300
	}
	s.interval = time.Duration(intervalSec) * time.Second
	if rc.MinProfitThresholdPct > 0 {
		s.minProfitPct = rc.MinProfitThresholdPct
	}
	if rc.DrawdownCloseThresholdPct > 0 {
		s.closeThreshold = rc.DrawdownCloseThresholdPct
	}
	return s
}

// startDrawdownMonitor starts drawdown monitoring. Settings are re-read every
// iteration so strategy-config changes take effect without a restart.
func (at *AutoTrader) startDrawdownMonitor() {
	at.monitorWg.Add(1)
	go func() {
		defer at.monitorWg.Done()

		logger.Info("📊 Started position drawdown monitoring")

		wasEnabled := true
		for {
			settings := at.drawdownMonitorSettings()
			if settings.enabled != wasEnabled {
				if settings.enabled {
					logger.Info("▶️ Drawdown monitoring enabled by strategy config")
				} else {
					logger.Info("⏸ Drawdown monitoring disabled by strategy config")
				}
				wasEnabled = settings.enabled
			}

			var wait time.Duration
			if settings.enabled {
				wait = settings.interval
			} else {
				// Disabled: poll in short steps so re-enabling applies quickly.
				wait = time.Minute
			}
			select {
			case <-at.runCtx.Done():
				logger.Info("⏹ Stopped position drawdown monitoring")
				return
			case <-time.After(wait):
			}
			if settings.enabled {
				at.checkPositionDrawdown(settings)
			}
		}
	}()
}

// checkPositionDrawdown checks position drawdown situation
func (at *AutoTrader) checkPositionDrawdown(settings drawdownSettings) {
	// Get current positions
	positions, err := at.trader.GetPositions()
	if err != nil {
		logger.Infof("❌ Drawdown monitoring: failed to get positions: %v", err)
		return
	}

	for _, pos := range positions {
		symbol, _ := pos["symbol"].(string)
		side, _ := pos["side"].(string)
		entryPrice, _ := pos["entryPrice"].(float64)
		markPrice, _ := pos["markPrice"].(float64)
		quantity, _ := pos["positionAmt"].(float64)
		if quantity < 0 {
			quantity = -quantity // Short position quantity is negative, convert to positive
		}
		if symbol == "" || side == "" {
			continue
		}

		// Guard: skip if entry price is zero (prevents division by zero panic)
		if entryPrice <= 0 {
			logger.Warnf("⚠️ Drawdown monitoring: %s %s has zero entry price, skipping", symbol, side)
			continue
		}

		// Calculate current P&L percentage
		leverage := 10 // Default value
		if lev, ok := pos["leverage"].(float64); ok {
			leverage = int(lev)
		}

		var currentPnLPct float64
		if side == "long" {
			currentPnLPct = ((markPrice - entryPrice) / entryPrice) * float64(leverage) * 100
		} else {
			currentPnLPct = ((entryPrice - markPrice) / entryPrice) * float64(leverage) * 100
		}

		// Construct unique position identifier (distinguish long/short)
		posKey := symbol + "_" + side

		// Get historical peak profit for this position
		at.peakPnLCacheMutex.RLock()
		peakPnLPct, exists := at.peakPnLCache[posKey]
		at.peakPnLCacheMutex.RUnlock()

		if !exists {
			// If no historical peak record, use current P&L as initial value
			peakPnLPct = currentPnLPct
			at.UpdatePeakPnL(symbol, side, currentPnLPct)
		} else {
			// Update peak cache
			at.UpdatePeakPnL(symbol, side, currentPnLPct)
		}

		// Calculate drawdown (magnitude of decline from peak)
		var drawdownPct float64
		if peakPnLPct > 0 && currentPnLPct < peakPnLPct {
			drawdownPct = ((peakPnLPct - currentPnLPct) / peakPnLPct) * 100
		}

		// Check close position condition: profit above threshold and drawdown from peak breaches the configured limit
		if currentPnLPct > settings.minProfitPct && drawdownPct >= settings.closeThreshold {
			logger.Infof("🚨 Drawdown close position condition triggered: %s %s | Current profit: %.2f%% | Peak profit: %.2f%% | Drawdown: %.2f%% (thresholds: min profit %.1f%%, close drawdown %.1f%%)",
				symbol, side, currentPnLPct, peakPnLPct, drawdownPct, settings.minProfitPct, settings.closeThreshold)

			// Execute close position
			if err := at.emergencyClosePosition(symbol, side); err != nil {
				logger.Infof("❌ Drawdown close position failed (%s %s): %v", symbol, side, err)
			} else {
				logger.Infof("✅ Drawdown close position succeeded: %s %s", symbol, side)
				// Clear cache for this position after closing
				at.ClearPeakPnLCache(symbol, side)
			}
		} else if currentPnLPct > settings.minProfitPct {
			// Record situations close to close position condition (for debugging)
			logger.Infof("📊 Drawdown monitoring: %s %s | Profit: %.2f%% | Peak: %.2f%% | Drawdown: %.2f%%",
				symbol, side, currentPnLPct, peakPnLPct, drawdownPct)
		}
	}
}

// emergencyClosePosition emergency close position function
func (at *AutoTrader) emergencyClosePosition(symbol, side string) error {
	switch side {
	case "long":
		order, err := at.execMode.CloseLong(symbol, 0) // 0 = close all
		if err != nil {
			return err
		}
		logger.Infof("✅ Emergency close long position succeeded, order ID: %v", order.OrderID)
	case "short":
		order, err := at.execMode.CloseShort(symbol, 0) // 0 = close all
		if err != nil {
			return err
		}
		logger.Infof("✅ Emergency close short position succeeded, order ID: %v", order.OrderID)
	default:
		return fmt.Errorf("unknown position direction: %s", side)
	}

	return nil
}

// GetPeakPnLCache gets peak profit cache
func (at *AutoTrader) GetPeakPnLCache() map[string]float64 {
	at.peakPnLCacheMutex.RLock()
	defer at.peakPnLCacheMutex.RUnlock()

	// Return a copy of the cache
	cache := make(map[string]float64)
	for k, v := range at.peakPnLCache {
		cache[k] = v
	}
	return cache
}

// UpdatePeakPnL updates peak profit cache
func (at *AutoTrader) UpdatePeakPnL(symbol, side string, currentPnLPct float64) {
	at.peakPnLCacheMutex.Lock()
	defer at.peakPnLCacheMutex.Unlock()

	posKey := symbol + "_" + side
	if peak, exists := at.peakPnLCache[posKey]; exists {
		// Update peak (if long, take larger value; if short, currentPnLPct is negative, also compare)
		if currentPnLPct > peak {
			at.peakPnLCache[posKey] = currentPnLPct
		}
	} else {
		// First time recording
		at.peakPnLCache[posKey] = currentPnLPct
	}
}

// ClearPeakPnLCache clears peak cache for specified position
func (at *AutoTrader) ClearPeakPnLCache(symbol, side string) {
	at.peakPnLCacheMutex.Lock()
	defer at.peakPnLCacheMutex.Unlock()

	posKey := symbol + "_" + side
	delete(at.peakPnLCache, posKey)
}

// ============================================================================
// Risk Control Helpers
// ============================================================================

// isBTCETH checks if a symbol is BTC or ETH
func isBTCETH(symbol string) bool {
	symbol = strings.ToUpper(symbol)
	return strings.HasPrefix(symbol, "BTC") || strings.HasPrefix(symbol, "ETH")
}

// isMajorAsset returns true for assets that should use the BTC/ETH higher
// position-value tier rather than the altcoin (1x equity) tier. This covers
// BTC/ETH crypto perps AND Hyperliquid XYZ assets (US equities, commodities,
// forex) — none of which are "altcoins" and all of which deserve the higher
// per-position cap so the AI can actually take meaningful positions.
func isMajorAsset(symbol string) bool {
	if isBTCETH(symbol) {
		return true
	}
	return market.IsXyzDexAsset(symbol)
}

// getSideFromAction converts order action to side (BUY/SELL)
func getSideFromAction(action string) string {
	switch action {
	case "open_long", "close_short":
		return "BUY"
	case "open_short", "close_long":
		return "SELL"
	default:
		return "BUY"
	}
}
