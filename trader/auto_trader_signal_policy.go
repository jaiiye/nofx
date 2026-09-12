package trader

import (
	"fmt"
	"strings"

	"nofx/kernel"
	"nofx/logger"
	"nofx/provider/hyperliquid"
	"nofx/trader/types"
)

type positionCacheInvalidator interface {
	InvalidatePositionCache()
}

func (at *AutoTrader) usesVergexSignalPolicy() bool {
	return at != nil &&
		at.config.StrategyConfig != nil &&
		strings.EqualFold(strings.TrimSpace(at.config.StrategyConfig.CoinSource.SourceType), "vergex_signal")
}

// usesSignalManagedExit keeps ordinary profit-taking under the direction
// state machine. A protective stop remains on the exchange for hard-risk
// containment, but an unchanged board signal must not be closed by a fixed TP.
func (at *AutoTrader) usesSignalManagedExit() bool {
	return at.usesVergexSignalPolicy()
}

// needsSignalTPCleanup reports whether a signal-managed hold should still check
// the exchange for legacy fixed take-profits. Under the signal policy new
// positions never receive a fixed TP, so the cleanup is only needed once per
// symbol per process lifetime — repeating the exchange queries every cycle
// would be wasted work.
func (at *AutoTrader) needsSignalTPCleanup(symbol string) bool {
	if !at.usesSignalManagedExit() {
		return false
	}
	if at.signalTPCleared == nil {
		return true
	}
	return !at.signalTPCleared[universeBaseKey(symbol)]
}

// markSignalTPCleared records that the exchange no longer carries a fixed
// take-profit for this symbol, so subsequent holds skip the cleanup queries.
func (at *AutoTrader) markSignalTPCleared(symbol string) {
	if at.signalTPCleared == nil {
		at.signalTPCleared = make(map[string]bool)
	}
	at.signalTPCleared[universeBaseKey(symbol)] = true
}

func validateProtectionPrices(action string, marketPrice, stopLoss, takeProfit float64, signalManaged bool) error {
	if marketPrice <= 0 || stopLoss <= 0 {
		return fmt.Errorf("market price and stop loss must be positive")
	}
	switch action {
	case "open_long":
		if stopLoss >= marketPrice {
			return fmt.Errorf("long stop loss %.8f must be below market price %.8f", stopLoss, marketPrice)
		}
		if !signalManaged && takeProfit <= marketPrice {
			return fmt.Errorf("long take profit %.8f must be above market price %.8f", takeProfit, marketPrice)
		}
	case "open_short":
		if stopLoss <= marketPrice {
			return fmt.Errorf("short stop loss %.8f must be above market price %.8f", stopLoss, marketPrice)
		}
		if !signalManaged && (takeProfit <= 0 || takeProfit >= marketPrice) {
			return fmt.Errorf("short take profit %.8f must be positive and below market price %.8f", takeProfit, marketPrice)
		}
	default:
		return fmt.Errorf("unsupported open action %q", action)
	}
	if signalManaged && takeProfit != 0 {
		return fmt.Errorf("signal-managed take profit must be 0")
	}
	return nil
}

func (at *AutoTrader) closeUnprotectedPosition(symbol, side string, quantity float64, protectionErr error) error {
	logger.Infof("  🚨 %v; emergency-closing %s %s", protectionErr, symbol, side)
	if closeErr := at.emergencyClosePositionAndVerify(symbol, side, quantity); closeErr != nil {
		return fmt.Errorf("%w; emergency close failed: %v", protectionErr, closeErr)
	}
	return fmt.Errorf("%w; opened position was emergency-closed", protectionErr)
}

func (at *AutoTrader) emergencyClosePositionAndVerify(symbol, side string, quantity float64) error {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		var err error
		if side == "long" {
			_, err = at.trader.CloseLong(symbol, quantity)
		} else if side == "short" {
			_, err = at.trader.CloseShort(symbol, quantity)
		} else {
			return fmt.Errorf("unknown position direction: %s", side)
		}
		if err != nil {
			lastErr = err
		} else {
			positions, err := getFreshPositions(at.trader)
			if err != nil {
				lastErr = fmt.Errorf("verify flat position: %w", err)
			} else {
				residual := false
				for _, pos := range positions {
					if universeBaseKey(fmt.Sprint(pos["symbol"])) == universeBaseKey(symbol) && strings.EqualFold(fmt.Sprint(pos["side"]), side) {
						residual = true
						break
					}
				}
				if !residual {
					if err := at.trader.CancelAllOrders(symbol); err != nil {
						return fmt.Errorf("position is flat but protection-order cleanup failed: %w", err)
					}
					openOrders, err := at.trader.GetOpenOrders(symbol)
					if err != nil {
						return fmt.Errorf("position is flat but protection-order verification failed: %w", err)
					}
					if len(openOrders) != 0 {
						return fmt.Errorf("position is flat but %d open orders remain for %s", len(openOrders), symbol)
					}
					return nil
				}
				lastErr = fmt.Errorf("residual %s position remains after close attempt %d", side, attempt)
			}
		}
	}
	return lastErr
}

func getFreshPositions(tr types.Trader) ([]map[string]interface{}, error) {
	if invalidator, ok := tr.(positionCacheInvalidator); ok {
		invalidator.InvalidatePositionCache()
	}
	return tr.GetPositions()
}

// Signal-strength thresholds, in absolute board z-score. The values mirror the
// tuned floors this fork already used for forced coverage: |z| below the weak
// floor was noise, and 0.75 was the strong-signal gate. They only ever scale an
// existing directional signal down; they never flip or open a direction.
const (
	// signalStrongScore is the minimum |z| for an intact direction: a held
	// position at or above it is never trimmed.
	signalStrongScore = 0.75
	// signalOpenScore is the minimum |z| for a NEW entry. It sits above the
	// hold floor on purpose: a signal intact enough to keep is not necessarily
	// strong enough to build. The recorded-board replay (scripts/optimize
	// signals.csv, 2026-08-31..09-12) scored entries by their z at entry —
	// legs at |z| >= 1.0 averaged +1.21 USDT against -0.64 for 0.75-1.0 legs —
	// and 0.75 sits at the board's 10th percentile, so the old shared floor
	// gated almost nothing (90% of rows passed, one reduce in twelve days).
	signalOpenScore = 1.0
	// below this the signal is treated as decayed and the position is trimmed
	signalWeakScore = 0.40
	// fractions of the current position closed at each decay tier
	signalTrimMediumPct = 1.0 / 3.0
	signalTrimWeakPct   = 2.0 / 3.0
	// consecutive board cycles a symbol may be absent before it is closed, so a
	// pure ranking drop-out does not trigger an immediate exit
	signalAbsentGraceCycles = 2
)

// a trim whose remainder is below this notional closes the whole position
// instead of stranding exchange-minimum dust (mirrors the general minimum
// opening size used by decision validation)
const minReduceRemainNotionalUSD = 12.0

// closePlan describes how a close decision is executed and recorded.
type closePlan struct {
	exchangeQty float64 // quantity sent to the exchange; 0 means close all
	recordQty   float64 // quantity recorded for accounting
	dustClose   bool    // a trim escalated to a full close by the dust guard
}

// planClose computes the execution plan for a close decision. A plain close
// sends 0 to the exchange (the close-all convention) and records the held
// quantity. A reduce closes only its fraction — unless the remainder would be
// exchange-minimum dust, in which case it escalates to a full close.
func planClose(action, symbol string, held, reducePct, price float64) (closePlan, error) {
	if !isReduceAction(action) {
		return closePlan{recordQty: held}, nil
	}
	if held <= 0 {
		return closePlan{}, fmt.Errorf("%s %s: no quantity to close (held %.8f)", action, symbol, held)
	}
	trim, fullClose := trimQuantity(held, reducePct, price)
	if fullClose {
		return closePlan{recordQty: held, dustClose: true}, nil
	}
	return closePlan{exchangeQty: trim, recordQty: trim}, nil
}

// trimQuantity converts a fraction of a held quantity into the quantity to
// close, reporting whether the plan escalated to a full close — either because
// the fraction is not a real trim, or because the remainder would strand
// exchange-minimum dust that would later fail the minimum order size.
func trimQuantity(held, pct, price float64) (qty float64, fullClose bool) {
	if pct <= 0 || pct >= 1 {
		return 0, true
	}
	trim := held * pct
	if price > 0 && (held-trim)*price < minReduceRemainNotionalUSD {
		return 0, true
	}
	return trim, false
}

// signalAbsentBudget returns how many consecutive board absences are tolerated
// for a symbol before it is closed.
func (at *AutoTrader) signalAbsentBudget() int {
	return signalAbsentGraceCycles
}

// countSignalAbsence records that a held symbol was missing from this board
// snapshot and reports the new consecutive-absence count.
func (at *AutoTrader) countSignalAbsence(symbol string) int {
	key := universeBaseKey(symbol)
	if key == "" {
		return 0
	}
	if at.signalAbsentCycles == nil {
		at.signalAbsentCycles = make(map[string]int)
	}
	at.signalAbsentCycles[key]++
	return at.signalAbsentCycles[key]
}

// clearSignalAbsence resets the absence streak once a symbol is back on the board.
func (at *AutoTrader) clearSignalAbsence(symbol string) {
	key := universeBaseKey(symbol)
	if key == "" || at.signalAbsentCycles == nil {
		return
	}
	delete(at.signalAbsentCycles, key)
}

// enforceVergexSignalPolicy turns the current direction board into a strict
// position state machine. Detail data can explain a signal, but cannot reverse
// or prematurely exit it.
func (at *AutoTrader) enforceVergexSignalPolicy(decisions []kernel.Decision, ctx *kernel.Context) []kernel.Decision {
	if !at.usesVergexSignalPolicy() {
		return decisions
	}
	if at.strategyEngine == nil || ctx == nil || !at.strategyEngine.HasVergexSignalSnapshot() {
		return at.guardBlindSignalCycle(decisions)
	}
	if at.signalBoardBlindCycles > 0 {
		at.logInfof("🧭 Claw402 direction board recovered after %d blind cycle(s)", at.signalBoardBlindCycles)
		at.signalBoardBlindCycles = 0
	}

	filtered, blocked := applyVergexSignalPolicy(
		decisions,
		ctx.Positions,
		at.strategyEngine.VergexSignalBias,
		at.strategyEngine.VergexSignalStrength,
		at,
	)
	for _, decision := range blocked {
		at.logWarnf("🧭 Blocked %s %s: action conflicts with the current Claw402 direction signal", decision.Symbol, decision.Action)
	}
	return filtered
}

// guardBlindSignalCycle runs when the strategy depends on the direction board
// but no snapshot is available. The state machine cannot evaluate anything, so
// letting the AI's decisions through unfiltered would allow opening positions
// against a board nobody can see — the 2026-09-04..09-08 stretch shows this can
// persist for days while every cycle "succeeds". Blind cycles therefore fail
// closed on entries; closes and holds still pass, because exiting without the
// board is defensive and the per-minute drawdown monitor keeps covering exits.
func (at *AutoTrader) guardBlindSignalCycle(decisions []kernel.Decision) []kernel.Decision {
	at.signalBoardBlindCycles++
	at.logWarnf("🧭 Claw402 direction board unavailable for %d cycle(s): state machine is blind, new entries are blocked", at.signalBoardBlindCycles)
	filtered := make([]kernel.Decision, 0, len(decisions))
	for _, decision := range decisions {
		if isOpenAction(decision.Action) {
			at.logWarnf("🧭 Blocked %s %s: the direction board is unavailable, so the entry direction cannot be verified", decision.Action, decision.Symbol)
			continue
		}
		filtered = append(filtered, decision)
	}
	return filtered
}

// logCorrelatedOpenBlocked records an open rejected because the book already
// carries the same correlation group. A nil receiver (pure unit tests) is a
// no-op so the decision logic stays testable without a logger.
func (at *AutoTrader) logCorrelatedOpenBlocked(symbol, group string) {
	if at == nil {
		return
	}
	at.logWarnf("🚫 Blocked %s: the book already holds the %s group (one position per correlated risk)", symbol, group)
}

// signalExitState is the board-driven disposition of a held position.
type signalExitState struct {
	action    string
	reducePct float64
	reasoning string
}

// signalExitFor decides how a held position reacts to the current board. The
// direction is never flipped here: a reversed signal closes the position and a
// decayed-but-still-matching signal trims it, which keeps the exit progressive
// instead of binary.
func (at *AutoTrader) signalExitFor(position kernel.PositionInfo, bias string, present bool, strength float64) signalExitState {
	side := strings.ToLower(strings.TrimSpace(position.Side))
	if side != "long" && side != "short" {
		return signalExitState{action: "hold"}
	}
	closeAction := "close_long"
	reduceAction := "reduce_long"
	if side == "short" {
		closeAction = "close_short"
		reduceAction = "reduce_short"
	}

	matches := present &&
		((side == "long" && bias == "bullish") || (side == "short" && bias == "bearish"))

	if present {
		if !matches {
			return signalExitState{action: closeAction, reasoning: fmt.Sprintf("Claw402 direction changed to %s", bias)}
		}
		if at != nil {
			at.clearSignalAbsence(position.Symbol)
		}
		switch {
		case strength >= signalStrongScore:
			return signalExitState{action: "hold", reasoning: fmt.Sprintf("Claw402 direction remains %s (strength %.2f); hold the existing %s position", bias, strength, side)}
		case strength >= signalWeakScore:
			return signalExitState{
				action:    reduceAction,
				reducePct: signalTrimMediumPct,
				reasoning: fmt.Sprintf("Claw402 direction %s is decaying (strength %.2f < %.2f); trim %.0f%%", bias, strength, signalStrongScore, signalTrimMediumPct*100),
			}
		default:
			return signalExitState{
				action:    reduceAction,
				reducePct: signalTrimWeakPct,
				reasoning: fmt.Sprintf("Claw402 direction %s is weak (strength %.2f < %.2f); trim %.0f%%", bias, strength, signalWeakScore, signalTrimWeakPct*100),
			}
		}
	}

	// Absent from the board. A pure ranking drop-out looks identical to a real
	// exit, so tolerate a couple of cycles before closing.
	absent := 0
	if at != nil {
		absent = at.countSignalAbsence(position.Symbol)
	}
	if at != nil && absent > at.signalAbsentBudget() {
		return signalExitState{action: closeAction, reasoning: "Symbol is no longer present on the current Claw402 direction board"}
	}
	return signalExitState{
		action:    reduceAction,
		reducePct: signalTrimMediumPct,
		reasoning: fmt.Sprintf("Symbol is missing from the Claw402 board (absence %d/%d); trimming %.0f%% while confirming", absent, signalAbsentGraceCycles, signalTrimMediumPct*100),
	}
}

func applyVergexSignalPolicy(
	decisions []kernel.Decision,
	positions []kernel.PositionInfo,
	biasFor func(string) (string, bool),
	strengthFor func(string) (float64, bool),
	at *AutoTrader,
) (filtered []kernel.Decision, blocked []kernel.Decision) {
	positionActions := make(map[string]string, len(positions))
	filtered = make([]kernel.Decision, 0, len(decisions)+len(positions))

	// Existing positions are managed solely by the current board signal. A
	// matching strong signal holds; a decaying signal trims; a changed signal
	// closes. Price-drawdown protection is intentionally NOT handled here: the
	// per-minute drawdown monitor in auto_trader_risk.go already covers it for
	// every strategy (and clears the peak cache on close), so duplicating it in
	// this state machine caused double exits and stale-peak re-trigger loops.
	for _, position := range positions {
		base := universeBaseKey(position.Symbol)
		bias, present := biasFor(position.Symbol)
		strength := 0.0
		if strengthFor != nil {
			strength, _ = strengthFor(position.Symbol)
		}

		state := signalExitState{action: "hold"}
		if at != nil {
			state = at.signalExitFor(position, bias, present, strength)
		}

		action, reasoning := state.action, state.reasoning
		reducePct := 0.0
		if isReduceAction(action) {
			reducePct = state.reducePct
		}

		if base != "" {
			positionActions[base] = action
		}
		filtered = append(filtered, kernel.Decision{
			Symbol:     position.Symbol,
			Action:     action,
			ReducePct:  reducePct,
			Confidence: 100,
			Reasoning:  reasoning,
		})
	}

	// Held exposure per correlation group, so a new open cannot double up on a
	// macro risk the book already carries (e.g. WTI and Brent crude are one bet
	// on oil, not two diversified positions).
	groupHeld := make(map[string]bool, len(positions))
	for _, position := range positions {
		if g := hyperliquid.CorrelationGroup(position.Symbol); g != "" {
			groupHeld[g] = true
		}
	}

	// Flat symbols may only open in the exact direction advertised by the board.
	for _, decision := range decisions {
		base := universeBaseKey(decision.Symbol)
		if positionAction, exists := positionActions[base]; base != "" && exists {
			if strings.ToLower(strings.TrimSpace(decision.Action)) != positionAction {
				blocked = append(blocked, decision)
			}
			continue
		}
		if !isOpenAction(decision.Action) {
			filtered = append(filtered, decision)
			continue
		}

		bias, present := biasFor(decision.Symbol)
		action := strings.ToLower(strings.TrimSpace(decision.Action))
		strength := 0.0
		if strengthFor != nil {
			strength, _ = strengthFor(decision.Symbol)
		}
		directionMatches := present &&
			((action == "open_long" && bias == "bullish") ||
				(action == "open_short" && bias == "bearish"))
		if !directionMatches {
			blocked = append(blocked, decision)
			continue
		}
		// Entries demand more strength than holds: this is what stops a faded
		// or flip-flopping board from rebuilding a position the exit side just
		// closed, and what keeps weak-signal entries out of the book at all.
		if strength < signalOpenScore {
			blocked = append(blocked, decision)
			continue
		}
		// One position per correlation group: taking a second instrument that
		// tracks the same underlying doubles the risk without adding an edge.
		if group := hyperliquid.CorrelationGroup(decision.Symbol); group != "" {
			if groupHeld[group] {
				at.logCorrelatedOpenBlocked(decision.Symbol, group)
				blocked = append(blocked, decision)
				continue
			}
			groupHeld[group] = true
		}
		filtered = append(filtered, decision)
	}
	return filtered, blocked
}
