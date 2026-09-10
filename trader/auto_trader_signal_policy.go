package trader

import (
	"fmt"
	"strings"

	"nofx/kernel"
)

// usesVergexSignalPolicy reports whether the configured coin source is the
// Vergex signal-ranking board — the only source carrying a per-symbol
// directional bias that can drive the position state machine.
func (at *AutoTrader) usesVergexSignalPolicy() bool {
	return at != nil &&
		at.config.StrategyConfig != nil &&
		strings.EqualFold(strings.TrimSpace(at.config.StrategyConfig.CoinSource.SourceType), "vergex_signal")
}

// usesSignalManagedExit keeps ordinary profit-taking under the direction state
// machine. A protective stop remains on the exchange for hard-risk containment,
// but an unchanged board signal must not be closed by a fixed take-profit.
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

// enforceVergexSignalPolicy turns the current Vergex signal-ranking board into a
// strict position state machine. Detail data can explain a signal, but cannot
// reverse or prematurely exit it.
func (at *AutoTrader) enforceVergexSignalPolicy(decisions []kernel.Decision, ctx *kernel.Context) []kernel.Decision {
	if !at.usesVergexSignalPolicy() || at.strategyEngine == nil || ctx == nil || !at.strategyEngine.HasVergexSignalSnapshot() {
		return decisions
	}

	filtered, blocked := applyVergexSignalPolicy(
		decisions,
		ctx.Positions,
		at.strategyEngine.VergexSignalBias,
	)
	for _, decision := range blocked {
		at.logWarnf("🧭 Blocked %s %s: action conflicts with the current Claw402 direction signal", decision.Symbol, decision.Action)
	}
	return filtered
}

func applyVergexSignalPolicy(
	decisions []kernel.Decision,
	positions []kernel.PositionInfo,
	biasFor func(string) (string, bool),
) (filtered []kernel.Decision, blocked []kernel.Decision) {
	positionActions := make(map[string]string, len(positions))
	filtered = make([]kernel.Decision, 0, len(decisions)+len(positions))

	// Existing positions are managed only by the current board direction.
	// A matching signal always holds; a changed, neutral, or absent signal exits.
	for _, position := range positions {
		base := universeBaseKey(position.Symbol)
		bias, present := biasFor(position.Symbol)
		side := strings.ToLower(strings.TrimSpace(position.Side))
		matches := (side == "long" && present && bias == "bullish") ||
			(side == "short" && present && bias == "bearish")

		action := "hold"
		reasoning := fmt.Sprintf("Claw402 direction remains %s; hold the existing %s position", bias, side)
		if !matches {
			switch side {
			case "long":
				action = "close_long"
			case "short":
				action = "close_short"
			}
			if !present {
				reasoning = "Symbol is no longer present on the current Claw402 direction board"
			} else {
				reasoning = fmt.Sprintf("Claw402 direction changed to %s", bias)
			}
		}
		if base != "" {
			positionActions[base] = action
		}
		filtered = append(filtered, kernel.Decision{
			Symbol:     position.Symbol,
			Action:     action,
			Confidence: 100,
			Reasoning:  reasoning,
		})
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
		allowed := present &&
			((action == "open_long" && bias == "bullish") ||
				(action == "open_short" && bias == "bearish"))
		if allowed {
			filtered = append(filtered, decision)
		} else {
			blocked = append(blocked, decision)
		}
	}
	return filtered, blocked
}
