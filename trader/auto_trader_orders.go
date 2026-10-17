package trader

import (
	"fmt"
	"nofx/kernel"
	"nofx/logger"
	"nofx/market"
	"nofx/store"
	"nofx/trader/connector"
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

// checkMinHold blocks a discretionary AI exit before the configured minimum
// holding period. Exchange-side stop-loss / take-profit and the drawdown
// monitor close positions independently of this path, so protective exits are
// never delayed — only the churn of opening and closing within a cycle or two,
// which paid round-trip fees for noise (a live run: 81% of exits were
// discretionary, 8 of 27 closed inside 30 minutes).
func (at *AutoTrader) checkMinHold(pos *store.TraderPosition, symbol, side string) error {
	if pos == nil || at.config.StrategyConfig == nil {
		return nil
	}
	minHold := at.config.StrategyConfig.RiskControl.MinHoldMinutes
	if minHold <= 0 || pos.EntryTime <= 0 {
		return nil
	}
	age := time.Since(time.UnixMilli(pos.EntryTime))
	if age >= time.Duration(minHold)*time.Minute {
		return nil
	}
	at.logInfof("⏳ Discretionary close of %s %s ignored: held %s, minimum is %d minutes",
		symbol, side, age.Round(time.Second), minHold)
	return fmt.Errorf("minimum holding period not reached for %s %s (held %s of %d minutes) — stop-loss/take-profit exits are unaffected",
		symbol, side, age.Round(time.Second), minHold)
}

// trackSuccessfulClose counts a confirmed close for the current cycle so the
// risk gate can offset the exchange's lagging position count. A NO_POSITION
// result (paper mode closing a non-existent position) does not count.
func (at *AutoTrader) trackSuccessfulClose(order *connector.OrderResult) {
	if order == nil || order.OrderID == "" || order.Status == "NO_POSITION" {
		return
	}
	at.successfulClosesInCycle++
}

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
	equity, availableBalance := accountEquityAndAvailableFor(balance, decision.Symbol)

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

	// Stock session gate: never open into a closed cash market (a gap would jump
	// straight past the stop). Closes are unaffected.
	if err := at.checkStockSession(decision.Symbol); err != nil {
		return err
	}

	// Entry mode: "maker" first rests a post-only order to pay the maker fee and
	// falls back to the aggressive order when it does not fill; the venue must
	// implement MakerEntry for that (duck-typed, so other venues are unchanged).
	makerEnabled, makerTimeout := at.makerEntrySettings()
	makerCapable := false
	if makerEnabled {
		_, makerCapable = at.trader.(makerEntryTrader)
		if !makerCapable {
			at.logInfof("ℹ️ %s does not support post-only entries — submitting the market order", at.exchange)
		}
	}

	// Code-enforced execution-quality guard: reject when walking the venue
	// book would cost more than the slippage ceiling. A passive entry does not
	// walk the book, so the guard runs on the aggressive path (including the
	// fallback) instead.
	if !makerCapable {
		if err := at.checkVenueSlippage(decision.Symbol, decision.Action, quantity*marketData.CurrentPrice); err != nil {
			return err
		}
	}

	// Set margin mode
	if err := at.trader.SetMarginMode(decision.Symbol, at.config.IsCrossMargin); err != nil {
		logger.Infof("  ⚠️ Failed to set margin mode: %v", err)
		// Continue execution, doesn't affect trading
	}

	// Open position
	var order *connector.OrderResult
	makerUsed := false
	if makerCapable {
		makerTrader := at.trader.(makerEntryTrader)
		order, err = makerTrader.MakerEntry(decision.Symbol, quantity, decision.Leverage, true, makerTimeout)
		if err != nil {
			// Not a failed trade: the trade is simply placed aggressively.
			at.logInfof("↪️ Post-only entry for %s not used (%v) — submitting the market order", decision.Symbol, err)
			order = nil
		} else {
			makerUsed = true
		}
	}
	if order == nil {
		if makerCapable {
			// The fallback is a market order after all, so the guard applies.
			if err := at.checkVenueSlippage(decision.Symbol, decision.Action, quantity*marketData.CurrentPrice); err != nil {
				return err
			}
		}
		order, err = at.execMode.OpenLong(decision.Symbol, quantity, decision.Leverage)
		if err != nil {
			return fmt.Errorf("failed to open long position for %s: %w", decision.Symbol, err)
		}
	}
	if order == nil {
		return fmt.Errorf("exchange returned no result for %s open_long", decision.Symbol)
	}

	// A partially filled passive entry means the position is smaller than
	// requested: records and protective orders must use the amount actually
	// held, never the requested size.
	filledQty := quantity
	if makerUsed && order.FilledQty > 0 && order.FilledQty < quantity {
		filledQty = order.FilledQty
		at.logInfof("ℹ️ Post-only entry filled %.8f of %.8f — sizing records and protection to the fill", filledQty, quantity)
	}

	// Record order ID
	if orderID, err := strconv.ParseInt(order.OrderID, 10, 64); err == nil {
		actionRecord.OrderID = orderID
	}

	logger.Infof("  ✓ Position opened successfully, order ID: %v, quantity: %.4f", order.OrderID, filledQty)

	// Record order to database and poll for confirmation
	at.recordAndConfirmOrder(order, decision.Symbol, "open_long", filledQty, marketData.CurrentPrice, decision.Leverage, 0)

	// Record position opening time
	posKey := decision.Symbol + "_long"
	at.positionFirstSeenTime[posKey] = time.Now().UnixMilli()

	// Set stop loss and take profit. The AI's absolute levels are kept as-is:
	// a passive fill is always at or better than the reference price, so the
	// stop only gets closer and the target further — never the reverse.
	if err := at.trader.SetStopLoss(decision.Symbol, "LONG", filledQty, decision.StopLoss); err != nil {
		logger.Infof("  ⚠ Failed to set stop loss: %v", err)
	}
	if err := at.trader.SetTakeProfit(decision.Symbol, "LONG", filledQty, decision.TakeProfit); err != nil {
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
	equity, availableBalance := accountEquityAndAvailableFor(balance, decision.Symbol)

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

	// Stock session gate: never open into a closed cash market (a gap would jump
	// straight past the stop). Closes are unaffected.
	if err := at.checkStockSession(decision.Symbol); err != nil {
		return err
	}

	// Entry mode: "maker" first rests a post-only order to pay the maker fee and
	// falls back to the aggressive order when it does not fill; the venue must
	// implement MakerEntry for that (duck-typed, so other venues are unchanged).
	makerEnabled, makerTimeout := at.makerEntrySettings()
	makerCapable := false
	if makerEnabled {
		_, makerCapable = at.trader.(makerEntryTrader)
		if !makerCapable {
			at.logInfof("ℹ️ %s does not support post-only entries — submitting the market order", at.exchange)
		}
	}

	// Code-enforced execution-quality guard: reject when walking the venue
	// book would cost more than the slippage ceiling. A passive entry does not
	// walk the book, so the guard runs on the aggressive path (including the
	// fallback) instead.
	if !makerCapable {
		if err := at.checkVenueSlippage(decision.Symbol, decision.Action, quantity*marketData.CurrentPrice); err != nil {
			return err
		}
	}

	// Set margin mode
	if err := at.trader.SetMarginMode(decision.Symbol, at.config.IsCrossMargin); err != nil {
		logger.Infof("  ⚠️ Failed to set margin mode: %v", err)
		// Continue execution, doesn't affect trading
	}

	// Open position
	var order *connector.OrderResult
	makerUsed := false
	if makerCapable {
		makerTrader := at.trader.(makerEntryTrader)
		order, err = makerTrader.MakerEntry(decision.Symbol, quantity, decision.Leverage, false, makerTimeout)
		if err != nil {
			// Not a failed trade: the trade is simply placed aggressively.
			at.logInfof("↪️ Post-only entry for %s not used (%v) — submitting the market order", decision.Symbol, err)
			order = nil
		} else {
			makerUsed = true
		}
	}
	if order == nil {
		if makerCapable {
			// The fallback is a market order after all, so the guard applies.
			if err := at.checkVenueSlippage(decision.Symbol, decision.Action, quantity*marketData.CurrentPrice); err != nil {
				return err
			}
		}
		order, err = at.execMode.OpenShort(decision.Symbol, quantity, decision.Leverage)
		if err != nil {
			return fmt.Errorf("failed to open short position for %s: %w", decision.Symbol, err)
		}
	}
	if order == nil {
		return fmt.Errorf("exchange returned no result for %s open_short", decision.Symbol)
	}

	// A partially filled passive entry means the position is smaller than
	// requested: records and protective orders must use the amount actually
	// held, never the requested size.
	filledQty := quantity
	if makerUsed && order.FilledQty > 0 && order.FilledQty < quantity {
		filledQty = order.FilledQty
		at.logInfof("ℹ️ Post-only entry filled %.8f of %.8f — sizing records and protection to the fill", filledQty, quantity)
	}

	// Record order ID
	if orderID, err := strconv.ParseInt(order.OrderID, 10, 64); err == nil {
		actionRecord.OrderID = orderID
	}

	logger.Infof("  ✓ Position opened successfully, order ID: %v, quantity: %.4f", order.OrderID, filledQty)

	// Record order to database and poll for confirmation
	at.recordAndConfirmOrder(order, decision.Symbol, "open_short", filledQty, marketData.CurrentPrice, decision.Leverage, 0)

	// Record position opening time
	posKey := decision.Symbol + "_short"
	at.positionFirstSeenTime[posKey] = time.Now().UnixMilli()

	// Set stop loss and take profit. The AI's absolute levels are kept as-is:
	// a passive fill is always at or better than the reference price, so the
	// stop only gets closer and the target further — never the reverse.
	if err := at.trader.SetStopLoss(decision.Symbol, "SHORT", filledQty, decision.StopLoss); err != nil {
		logger.Infof("  ⚠ Failed to set stop loss: %v", err)
	}
	if err := at.trader.SetTakeProfit(decision.Symbol, "SHORT", filledQty, decision.TakeProfit); err != nil {
		logger.Infof("  ⚠ Failed to set take profit: %v", err)
	}

	return nil
}

// makerEntryTrader is implemented by venues that can rest a post-only entry
// (Hyperliquid does). It is deliberately optional rather than part of the Trader
// interface: the other venues keep submitting the aggressive order unchanged.
type makerEntryTrader interface {
	MakerEntry(symbol string, qty float64, leverage int, isLong bool, timeout time.Duration) (*connector.OrderResult, error)
}

// makerEntrySettings returns whether the strategy asks for passive entries and
// how long to wait for the fill. Both come from one normalizer, so an unset or
// misspelled mode can only ever mean "market".
func (at *AutoTrader) makerEntrySettings() (bool, time.Duration) {
	if at.config.StrategyConfig == nil {
		return false, 0
	}
	return at.config.StrategyConfig.RiskControl.MakerEntry()
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
			if err := at.checkMinHold(openPos, decision.Symbol, "LONG"); err != nil {
				return err
			}
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
	at.trackSuccessfulClose(order)

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
			if err := at.checkMinHold(openPos, decision.Symbol, "SHORT"); err != nil {
				return err
			}
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
	at.trackSuccessfulClose(order)

	// Record order ID
	if orderID, err := strconv.ParseInt(order.OrderID, 10, 64); err == nil {
		actionRecord.OrderID = orderID
	}

	// Record order to database and poll for confirmation
	at.recordAndConfirmOrder(order, decision.Symbol, "close_short", quantity, marketData.CurrentPrice, 0, entryPrice)

	logger.Infof("  ✓ Position closed successfully")
	return nil
}
