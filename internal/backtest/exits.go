package backtest

import (
	"sort"
	"time"

	"nofx/market"
)

// simulate walks one signal forward from the decision time and returns the round
// trip. Which protective levels apply depends on the configured exit mode.
func simulate(s signal, candles []market.Kline, cfg Config) (Trade, bool) {
	if len(candles) == 0 || s.Symbol == "" || !isOpen(s.Action) {
		return Trade{}, false
	}
	side, dir := "long", 1.0
	if s.Action == "open_short" {
		side, dir = "short", -1.0
	}

	// First candle that opens strictly after the decision.
	start := sort.Search(len(candles), func(i int) bool { return candles[i].OpenTime > s.DecidedAt.UnixMilli() })
	if start >= len(candles) {
		return Trade{}, false
	}
	entry := candles[start].Open
	if entry <= 0 {
		return Trade{}, false
	}

	// Effective protective levels: recorded, or overridden to a fixed percentage
	// from entry (for the parameter sweep). Sizing runs after the overrides so
	// risk mode divides by the stop it will actually use.
	sl, tp := s.StopLoss, s.TakeProfit
	if cfg.OverrideStopPct > 0 {
		if side == "long" {
			sl = entry * (1 - cfg.OverrideStopPct/100)
		} else {
			sl = entry * (1 + cfg.OverrideStopPct/100)
		}
	}
	if cfg.OverrideTakePct > 0 {
		if side == "long" {
			tp = entry * (1 + cfg.OverrideTakePct/100)
		} else {
			tp = entry * (1 - cfg.OverrideTakePct/100)
		}
	}

	quantity := quantityFor(s, entry, sl, cfg)
	if quantity <= 0 {
		return Trade{}, false
	}
	notional := entry * quantity
	if notional < 1 {
		return Trade{}, false
	}

	t := Trade{
		Symbol: s.Symbol, Side: side, EntryTime: time.UnixMilli(candles[start].OpenTime).UTC(),
		EntryPrice: entry, Quantity: quantity, Notional: notional,
		Confidence: s.Confidence,
	}
	if cfg.RegimeSource != nil {
		regime := cfg.RegimeSource(s.Symbol, s.DecidedAt)
		t.EntryCascade = regime.Cascade
		t.EntryCrowdedSide = regime.CrowdedSide
	}

	exit, reason, hold, exitAt := simulateExit(candles, start, entry, side, sl, tp, cfg)
	t.ExitPrice = exit
	t.ExitReason = reason
	t.HoldCandles = hold
	t.ExitTime = exitAt
	t.GrossPnL = (exit - entry) * quantity * dir
	t.Fee = cfg.Fees.Estimate(entry*quantity, false).Amount + cfg.Fees.Estimate(exit*quantity, false).Amount
	t.NetPnL = t.GrossPnL - t.Fee
	return t, true
}

// simulateExit returns the exit price, its reason, the candles held, and the
// exit timestamp. Stop-loss is evaluated first so a candle spanning several
// levels is scored as the worst case.
//
// Giveback mode trails the peak instead of taking a fixed target: once the price
// has moved GivebackArmPct in favor, it exits when GivebackPct of the peak profit
// is given back. The peak is updated after the checks, so a candle that both sets
// a new extreme and reverses cannot trigger the trailing exit on its own range
// (that intrabar ordering is unknowable at this resolution) — the exit fires on
// the next candle, which is the conservative choice.
func simulateExit(candles []market.Kline, start int, entry float64, side string, sl, tp float64, cfg Config) (float64, string, int, time.Time) {
	last := start + cfg.MaxHoldCandles
	if last >= len(candles) {
		last = len(candles) - 1
	}

	peak := entry
	for i := start; i <= last; i++ {
		c := candles[i]
		if hitStop(c, side, sl) {
			return sl, "stop_loss", i - start + 1, time.UnixMilli(c.OpenTime).UTC()
		}
		if cfg.ExitMode == ExitGiveback {
			if trigger, armed := givebackTrigger(entry, peak, side, cfg); armed && crosses(c, side, trigger) {
				return trigger, "giveback", i - start + 1, time.UnixMilli(c.OpenTime).UTC()
			}
		} else if hitTarget(c, side, tp) {
			return tp, "take_profit", i - start + 1, time.UnixMilli(c.OpenTime).UTC()
		}

		if side == "long" {
			if c.High > peak {
				peak = c.High
			}
		} else if c.Low < peak {
			peak = c.Low
		}
	}
	return candles[last].Close, "timeout", last - start + 1, time.UnixMilli(candles[last].OpenTime).UTC()
}

// givebackTrigger returns the price at which the trailing exit fires, and
// whether the peak has moved far enough in favor to arm it.
func givebackTrigger(entry, peak float64, side string, cfg Config) (float64, bool) {
	if entry <= 0 {
		return 0, false
	}
	peakPct := (peak - entry) / entry * 100
	if side == "short" {
		peakPct = (entry - peak) / entry * 100
	}
	if peakPct < cfg.GivebackArmPct {
		return 0, false
	}
	keptPct := peakPct * (1 - cfg.GivebackPct/100)
	if side == "long" {
		return entry * (1 + keptPct/100), true
	}
	return entry * (1 - keptPct/100), true
}

// crosses reports whether the candle's range touches a level from the side that
// would close the position.
func crosses(c market.Kline, side string, level float64) bool {
	if level <= 0 {
		return false
	}
	if side == "long" {
		return c.Low <= level
	}
	return c.High >= level
}

func hitStop(c market.Kline, side string, sl float64) bool {
	return crosses(c, side, sl)
}

func hitTarget(c market.Kline, side string, tp float64) bool {
	if tp <= 0 {
		return false
	}
	if side == "long" {
		return c.High >= tp
	}
	return c.Low <= tp
}
