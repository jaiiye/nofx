package trader

import (
	"fmt"
	"nofx/kernel"
	"nofx/logger"
	"nofx/market"
	"nofx/store"
	"strconv"
	"time"
)

const (
	// marginOverheadFactor and takerFeeRate approximate the total funds an
	// exchange reserves when opening a position:
	// totalRequired ≈ positionSize/leverage + positionSize*takerFeeRate + positionSize/leverage*1%
	//              = positionSize * (marginOverheadFactor/leverage + takerFeeRate)
	marginOverheadFactor = 1.01
	takerFeeRate         = 0.001

	// positionSizeSafetyFactor leaves a buffer below the maximum affordable
	// position size so a price move between sizing and execution cannot
	// trigger an insufficient-margin rejection.
	positionSizeSafetyFactor = 0.98
)

// executeDecisionWithRecord executes AI decision and records detailed information
func (at *AutoTrader) executeDecisionWithRecord(decision *kernel.Decision, actionRecord *store.DecisionAction) error {
	switch decision.Action {
	case "open_long":
		return at.executeOpenLongWithRecord(decision, actionRecord)
	case "open_short":
		return at.executeOpenShortWithRecord(decision, actionRecord)
	case "close_long":
		return at.executeCloseLongWithRecord(decision, actionRecord)
	case "close_short":
		return at.executeCloseShortWithRecord(decision, actionRecord)
	case "hold", "wait":
		// No execution needed, just record
		return nil
	default:
		return fmt.Errorf("unknown action: %s", decision.Action)
	}
}

// executeOpenLongWithRecord executes open long position and records detailed information
func (at *AutoTrader) executeOpenLongWithRecord(decision *kernel.Decision, actionRecord *store.DecisionAction) error {
	logger.Infof("  📈 Open long: %s", decision.Symbol)

	// ⚠️ Get current positions for multiple checks
	positions, err := at.trader.GetPositions()
	if err != nil {
		return fmt.Errorf("failed to get positions: %w", err)
	}

	// Get current price
	marketData, err := market.GetWithExchange(decision.Symbol, at.exchange)
	if err != nil {
		return fmt.Errorf("failed to get market data for %s: %w", decision.Symbol, err)
	}

	// Get balance (needed for the risk gate)
	balance, err := at.trader.GetBalance()
	if err != nil {
		return fmt.Errorf("failed to get account balance: %w", err)
	}
	equity, availableBalance := accountEquityAndAvailable(balance)

	// [CODE ENFORCED] Unified pre-trade risk gate: protections, max positions,
	// position-value cap, balance sufficiency, min size, max margin usage and
	// leverage clamp, all in one place.
	gate := at.checkOpenRiskGate(decision, positions, equity, availableBalance)
	if !gate.Allowed {
		logger.Infof("  🛡️ [RISK GATE] open_long blocked — %s", gate.Reason)
		return fmt.Errorf("❌ [RISK GATE] %s", gate.Reason)
	}
	if gate.AdjustedSizeUSD > 0 && gate.AdjustedSizeUSD != decision.PositionSizeUSD {
		logger.Infof("  ⚠️ [RISK GATE] position size %.2f → %.2f USDT", decision.PositionSizeUSD, gate.AdjustedSizeUSD)
		decision.PositionSizeUSD = gate.AdjustedSizeUSD
	}
	if gate.AdjustedLeverage > 0 && gate.AdjustedLeverage != decision.Leverage {
		logger.Infof("  ⚠️ [RISK GATE] leverage %dx → %dx", decision.Leverage, gate.AdjustedLeverage)
		decision.Leverage = gate.AdjustedLeverage
	}

	// Calculate quantity with adjusted position size
	quantity := decision.PositionSizeUSD / marketData.CurrentPrice
	actionRecord.Quantity = quantity
	actionRecord.Price = marketData.CurrentPrice

	// Set margin mode
	if err := at.trader.SetMarginMode(decision.Symbol, at.config.IsCrossMargin); err != nil {
		logger.Infof("  ⚠️ Failed to set margin mode: %v", err)
		// Continue execution, doesn't affect trading
	}

	// Open position
	order, err := at.execMode.OpenLong(decision.Symbol, quantity, decision.Leverage)
	if err != nil {
		return fmt.Errorf("failed to open long position for %s: %w", decision.Symbol, err)
	}
	if order == nil {
		return fmt.Errorf("exchange returned no result for %s open_long", decision.Symbol)
	}

	// Record order ID
	if orderID, err := strconv.ParseInt(order.OrderID, 10, 64); err == nil {
		actionRecord.OrderID = orderID
	}

	logger.Infof("  ✓ Position opened successfully, order ID: %v, quantity: %.4f", order.OrderID, quantity)

	// Record order to database and poll for confirmation
	at.recordAndConfirmOrder(order, decision.Symbol, "open_long", quantity, marketData.CurrentPrice, decision.Leverage, 0)

	// Record position opening time
	posKey := decision.Symbol + "_long"
	at.positionFirstSeenTime[posKey] = time.Now().UnixMilli()

	// Set stop loss and take profit
	if err := at.trader.SetStopLoss(decision.Symbol, "LONG", quantity, decision.StopLoss); err != nil {
		logger.Infof("  ⚠ Failed to set stop loss: %v", err)
	}
	if err := at.trader.SetTakeProfit(decision.Symbol, "LONG", quantity, decision.TakeProfit); err != nil {
		logger.Infof("  ⚠ Failed to set take profit: %v", err)
	}

	return nil
}

// executeOpenShortWithRecord executes open short position and records detailed information
func (at *AutoTrader) executeOpenShortWithRecord(decision *kernel.Decision, actionRecord *store.DecisionAction) error {
	logger.Infof("  📉 Open short: %s", decision.Symbol)

	// ⚠️ Get current positions for multiple checks
	positions, err := at.trader.GetPositions()
	if err != nil {
		return fmt.Errorf("failed to get positions: %w", err)
	}

	// Get current price
	marketData, err := market.GetWithExchange(decision.Symbol, at.exchange)
	if err != nil {
		return fmt.Errorf("failed to get market data for %s: %w", decision.Symbol, err)
	}

	// Get balance (needed for the risk gate)
	balance, err := at.trader.GetBalance()
	if err != nil {
		return fmt.Errorf("failed to get account balance: %w", err)
	}
	equity, availableBalance := accountEquityAndAvailable(balance)

	// [CODE ENFORCED] Unified pre-trade risk gate: protections, max positions,
	// position-value cap, balance sufficiency, min size, max margin usage and
	// leverage clamp, all in one place.
	gate := at.checkOpenRiskGate(decision, positions, equity, availableBalance)
	if !gate.Allowed {
		logger.Infof("  🛡️ [RISK GATE] open_short blocked — %s", gate.Reason)
		return fmt.Errorf("❌ [RISK GATE] %s", gate.Reason)
	}
	if gate.AdjustedSizeUSD > 0 && gate.AdjustedSizeUSD != decision.PositionSizeUSD {
		logger.Infof("  ⚠️ [RISK GATE] position size %.2f → %.2f USDT", decision.PositionSizeUSD, gate.AdjustedSizeUSD)
		decision.PositionSizeUSD = gate.AdjustedSizeUSD
	}
	if gate.AdjustedLeverage > 0 && gate.AdjustedLeverage != decision.Leverage {
		logger.Infof("  ⚠️ [RISK GATE] leverage %dx → %dx", decision.Leverage, gate.AdjustedLeverage)
		decision.Leverage = gate.AdjustedLeverage
	}

	// Calculate quantity with adjusted position size
	quantity := decision.PositionSizeUSD / marketData.CurrentPrice
	actionRecord.Quantity = quantity
	actionRecord.Price = marketData.CurrentPrice

	// Set margin mode
	if err := at.trader.SetMarginMode(decision.Symbol, at.config.IsCrossMargin); err != nil {
		logger.Infof("  ⚠️ Failed to set margin mode: %v", err)
		// Continue execution, doesn't affect trading
	}

	// Open position
	order, err := at.execMode.OpenShort(decision.Symbol, quantity, decision.Leverage)
	if err != nil {
		return fmt.Errorf("failed to open short position for %s: %w", decision.Symbol, err)
	}
	if order == nil {
		return fmt.Errorf("exchange returned no result for %s open_short", decision.Symbol)
	}

	// Record order ID
	if orderID, err := strconv.ParseInt(order.OrderID, 10, 64); err == nil {
		actionRecord.OrderID = orderID
	}

	logger.Infof("  ✓ Position opened successfully, order ID: %v, quantity: %.4f", order.OrderID, quantity)

	// Record order to database and poll for confirmation
	at.recordAndConfirmOrder(order, decision.Symbol, "open_short", quantity, marketData.CurrentPrice, decision.Leverage, 0)

	// Record position opening time
	posKey := decision.Symbol + "_short"
	at.positionFirstSeenTime[posKey] = time.Now().UnixMilli()

	// Set stop loss and take profit
	if err := at.trader.SetStopLoss(decision.Symbol, "SHORT", quantity, decision.StopLoss); err != nil {
		logger.Infof("  ⚠ Failed to set stop loss: %v", err)
	}
	if err := at.trader.SetTakeProfit(decision.Symbol, "SHORT", quantity, decision.TakeProfit); err != nil {
		logger.Infof("  ⚠ Failed to set take profit: %v", err)
	}

	return nil
}

// executeCloseLongWithRecord executes close long position and records detailed information
func (at *AutoTrader) executeCloseLongWithRecord(decision *kernel.Decision, actionRecord *store.DecisionAction) error {
	logger.Infof("  🔄 Close long: %s", decision.Symbol)

	// Get current price
	marketData, err := market.GetWithExchange(decision.Symbol, at.exchange)
	if err != nil {
		return fmt.Errorf("failed to get market data for %s: %w", decision.Symbol, err)
	}
	actionRecord.Price = marketData.CurrentPrice

	// Normalize symbol for database lookup
	normalizedSymbol := market.Normalize(decision.Symbol)

	// Get entry price and quantity - prioritize local database for accurate quantity
	var entryPrice float64
	var quantity float64

	// First try to get from local database (more accurate for quantity)
	if at.store != nil {
		if openPos, err := at.store.Position().GetOpenPositionBySymbol(at.id, normalizedSymbol, "LONG"); err == nil && openPos != nil {
			quantity = openPos.Quantity
			entryPrice = openPos.EntryPrice
			logger.Infof("  📊 Using local position data: qty=%.8f, entry=%.2f", quantity, entryPrice)
		}
	}

	// Fallback to exchange API if local data not found
	if quantity == 0 {
		positions, err := at.trader.GetPositions()
		if err == nil {
			for _, pos := range positions {
				if pos["symbol"] == decision.Symbol && pos["side"] == "long" {
					if ep, ok := pos["entryPrice"].(float64); ok {
						entryPrice = ep
					}
					if amt, ok := pos["positionAmt"].(float64); ok && amt > 0 {
						quantity = amt
					}
					break
				}
			}
		}
		logger.Infof("  📊 Using exchange position data: qty=%.8f, entry=%.2f", quantity, entryPrice)
	}

	// Close position
	order, err := at.execMode.CloseLong(decision.Symbol, 0) // 0 = close all
	if err != nil {
		return fmt.Errorf("failed to close long position for %s: %w", decision.Symbol, err)
	}
	if order == nil {
		return fmt.Errorf("exchange returned no result for %s close_long", decision.Symbol)
	}

	// Record order ID
	if orderID, err := strconv.ParseInt(order.OrderID, 10, 64); err == nil {
		actionRecord.OrderID = orderID
	}

	// Record order to database and poll for confirmation
	at.recordAndConfirmOrder(order, decision.Symbol, "close_long", quantity, marketData.CurrentPrice, 0, entryPrice)

	logger.Infof("  ✓ Position closed successfully")
	return nil
}

// executeCloseShortWithRecord executes close short position and records detailed information
func (at *AutoTrader) executeCloseShortWithRecord(decision *kernel.Decision, actionRecord *store.DecisionAction) error {
	logger.Infof("  🔄 Close short: %s", decision.Symbol)

	// Get current price
	marketData, err := market.GetWithExchange(decision.Symbol, at.exchange)
	if err != nil {
		return fmt.Errorf("failed to get market data for %s: %w", decision.Symbol, err)
	}
	actionRecord.Price = marketData.CurrentPrice

	// Normalize symbol for database lookup
	normalizedSymbol := market.Normalize(decision.Symbol)

	// Get entry price and quantity - prioritize local database for accurate quantity
	var entryPrice float64
	var quantity float64

	// First try to get from local database (more accurate for quantity)
	if at.store != nil {
		if openPos, err := at.store.Position().GetOpenPositionBySymbol(at.id, normalizedSymbol, "SHORT"); err == nil && openPos != nil {
			quantity = openPos.Quantity
			entryPrice = openPos.EntryPrice
			logger.Infof("  📊 Using local position data: qty=%.8f, entry=%.2f", quantity, entryPrice)
		}
	}

	// Fallback to exchange API if local data not found
	if quantity == 0 {
		positions, err := at.trader.GetPositions()
		if err == nil {
			for _, pos := range positions {
				if pos["symbol"] == decision.Symbol && pos["side"] == "short" {
					if ep, ok := pos["entryPrice"].(float64); ok {
						entryPrice = ep
					}
					if amt, ok := pos["positionAmt"].(float64); ok {
						quantity = -amt // positionAmt is negative for short
					}
					break
				}
			}
		}
		logger.Infof("  📊 Using exchange position data: qty=%.8f, entry=%.2f", quantity, entryPrice)
	}

	// Close position
	order, err := at.execMode.CloseShort(decision.Symbol, 0) // 0 = close all
	if err != nil {
		return fmt.Errorf("failed to close short position for %s: %w", decision.Symbol, err)
	}
	if order == nil {
		return fmt.Errorf("exchange returned no result for %s close_short", decision.Symbol)
	}

	// Record order ID
	if orderID, err := strconv.ParseInt(order.OrderID, 10, 64); err == nil {
		actionRecord.OrderID = orderID
	}

	// Record order to database and poll for confirmation
	at.recordAndConfirmOrder(order, decision.Symbol, "close_short", quantity, marketData.CurrentPrice, 0, entryPrice)

	logger.Infof("  ✓ Position closed successfully")
	return nil
}
