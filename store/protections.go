package store

import (
	"time"
)

// ProtectionConfig holds code-enforced circuit breakers modelled on
// Freqtrade's protections. They run BEFORE any opening order and block
// it outright — unlike the AI-guided limits in RiskControlConfig which
// the model is merely told to respect.
//
// Zero values are treated as "unset" by Normalized() and replaced with
// defaults, so a config that lost these fields (older clients, frontend
// round-trips) still gets working protections rather than none.
type ProtectionConfig struct {
	// MaxDrawdownPct pauses opening while equity is more than this
	// percent below its peak over the lookback window. Recovery is
	// automatic once the drawdown closes (stateless check).
	MaxDrawdownPct       float64 `json:"max_drawdown_pct,omitempty"`
	MaxDrawdownLookbackH int     `json:"max_drawdown_lookback_h,omitempty"`

	// DrawdownBasis picks which curve the max-drawdown breaker watches.
	//   "equity"  — total equity (wallet + unrealized PnL), the default
	//   "balance" — realized/wallet balance only, excluding open positions
	// Modelled on Freqtrade's protection calculation modes.
	DrawdownBasis string `json:"drawdown_basis,omitempty"`

	// LosingClosesGuard pauses opening while at least Count losing
	// closes occurred within the lookback window ("stop trading when
	// the strategy is clearly not working right now").
	LosingClosesGuardCount     int `json:"losing_closes_guard_count,omitempty"`
	LosingClosesGuardLookbackH int `json:"losing_closes_guard_lookback_h,omitempty"`

	// CooldownMinutes blocks reopening the SAME symbol for this long
	// after it was last closed — prevents the "close then immediately
	// re-enter" churn that dominates AI loss patterns.
	CooldownMinutes int `json:"cooldown_minutes,omitempty"`

	// LowProfitPairs blocks symbols whose cumulative realized PnL over
	// the lookback window is non-positive after at least MinTrades
	// closes (drop the instruments that keep bleeding).
	LowProfitLookbackH int `json:"low_profit_lookback_h,omitempty"`
	LowProfitMinTrades int `json:"low_profit_min_trades,omitempty"`
}

// Drawdown basis values accepted by ProtectionConfig.DrawdownBasis.
const (
	DrawdownBasisEquity  = "equity"
	DrawdownBasisBalance = "balance"
)

// DefaultProtectionConfig returns the shipped defaults. Chosen to be
// conservative for a small account running one AI strategy:
//   - halt new entries after a 15% drawdown from the 7-day peak
//   - halt after 3 losing closes within 6h
//   - 60 min cooldown per symbol after any close
//   - drop a symbol after >=4 closes in 7d with non-positive total PnL
func DefaultProtectionConfig() ProtectionConfig {
	return ProtectionConfig{
		MaxDrawdownPct:             15,
		MaxDrawdownLookbackH:       168,
		DrawdownBasis:              DrawdownBasisEquity,
		LosingClosesGuardCount:     3,
		LosingClosesGuardLookbackH: 6,
		CooldownMinutes:            60,
		LowProfitLookbackH:         168,
		LowProfitMinTrades:         4,
	}
}

// Normalized fills zero fields from defaults and clamps ranges. A
// negative value disables the individual protection (documented escape
// hatch for users who want to run without it).
func (p ProtectionConfig) Normalized() ProtectionConfig {
	d := DefaultProtectionConfig()
	switch p.DrawdownBasis {
	case DrawdownBasisEquity, DrawdownBasisBalance:
	default:
		p.DrawdownBasis = d.DrawdownBasis
	}
	if p.MaxDrawdownPct == 0 {
		p.MaxDrawdownPct = d.MaxDrawdownPct
	}
	if p.MaxDrawdownLookbackH == 0 {
		p.MaxDrawdownLookbackH = d.MaxDrawdownLookbackH
	}
	if p.LosingClosesGuardCount == 0 {
		p.LosingClosesGuardCount = d.LosingClosesGuardCount
	}
	if p.LosingClosesGuardLookbackH == 0 {
		p.LosingClosesGuardLookbackH = d.LosingClosesGuardLookbackH
	}
	if p.CooldownMinutes == 0 {
		p.CooldownMinutes = d.CooldownMinutes
	}
	if p.LowProfitLookbackH == 0 {
		p.LowProfitLookbackH = d.LowProfitLookbackH
	}
	if p.LowProfitMinTrades == 0 {
		p.LowProfitMinTrades = d.LowProfitMinTrades
	}
	if p.MaxDrawdownLookbackH > 24*90 {
		p.MaxDrawdownLookbackH = 24 * 90
	}
	if p.LowProfitLookbackH > 24*90 {
		p.LowProfitLookbackH = 24 * 90
	}
	if p.CooldownMinutes > 60*24*7 {
		p.CooldownMinutes = 60 * 24 * 7
	}
	return p
}

// ---------------------------------------------------------------------------
// Queries backing the protections. All are cheap single-row aggregates
// over the existing tables; the trading cycle runs every few minutes so
// no caching is needed.
// ---------------------------------------------------------------------------

// PeakAndCurrentEquity returns the maximum total_equity in the window
// and the most recent value. ok=false when the window has no snaps.
func (s *EquityStore) PeakAndCurrentEquity(traderID string, since time.Time) (peak, current float64, ok bool, err error) {
	var res struct {
		Peak    *float64
		Current *float64
	}
	row := s.db.Model(&EquitySnapshot{}).
		Select("MAX(total_equity) AS peak").
		Where("trader_id = ? AND timestamp >= ?", traderID, since).
		Scan(&res)
	if row.Error != nil {
		return 0, 0, false, row.Error
	}
	if res.Peak == nil {
		return 0, 0, false, nil
	}
	// Latest snapshot in the window.
	var latest EquitySnapshot
	if err := s.db.Where("trader_id = ? AND timestamp >= ?", traderID, since).
		Order("timestamp DESC").Limit(1).Find(&latest).Error; err != nil {
		return 0, 0, false, err
	}
	return *res.Peak, latest.TotalEquity, true, nil
}

// PeakAndCurrentBalance mirrors PeakAndCurrentEquity but watches the
// realized/wallet balance column (total_equity minus unrealized PnL), so
// open-position swings don't trigger the drawdown breaker. Used when
// ProtectionConfig.DrawdownBasis == "balance".
func (s *EquityStore) PeakAndCurrentBalance(traderID string, since time.Time) (peak, current float64, ok bool, err error) {
	var res struct {
		Peak *float64
	}
	row := s.db.Model(&EquitySnapshot{}).
		Select("MAX(balance) AS peak").
		Where("trader_id = ? AND timestamp >= ?", traderID, since).
		Scan(&res)
	if row.Error != nil {
		return 0, 0, false, row.Error
	}
	if res.Peak == nil {
		return 0, 0, false, nil
	}
	var latest EquitySnapshot
	if err := s.db.Where("trader_id = ? AND timestamp >= ?", traderID, since).
		Order("timestamp DESC").Limit(1).Find(&latest).Error; err != nil {
		return 0, 0, false, err
	}
	return *res.Peak, latest.Balance, true, nil
}

// CountLosingClosesSince counts closed positions with negative net PnL
// (realized minus fees and funding) that exited within the window.
func (s *PositionStore) CountLosingClosesSince(traderID string, sinceMs int64) (int, error) {
	var n int64
	err := s.db.Model(&TraderPosition{}).
		Where("trader_id = ? AND status = ? AND exit_time >= ? AND (realized_pnl - fee - funding_fee) < 0",
			traderID, "CLOSED", sinceMs).
		Count(&n).Error
	return int(n), err
}

// LastCloseTime returns the most recent exit_time for a symbol, or 0
// when it has never been closed.
func (s *PositionStore) LastCloseTime(traderID, symbol string) (int64, error) {
	var last *int64
	err := s.db.Model(&TraderPosition{}).
		Select("MAX(exit_time)").
		Where("trader_id = ? AND symbol = ? AND status = ?", traderID, symbol, "CLOSED").
		Scan(&last).Error
	if err != nil || last == nil {
		return 0, err
	}
	return *last, nil
}

// SymbolPnLSince returns cumulative realized PnL and trade count for a
// symbol within the window.
func (s *PositionStore) SymbolPnLSince(traderID, symbol string, sinceMs int64) (pnl float64, trades int, err error) {
	var res struct {
		PnL    *float64
		Trades int
	}
	err = s.db.Model(&TraderPosition{}).
		Select("SUM(realized_pnl - fee - funding_fee) AS pn_l, COUNT(*) AS trades").
		Where("trader_id = ? AND symbol = ? AND status = ? AND exit_time >= ?",
			traderID, symbol, "CLOSED", sinceMs).
		Scan(&res).Error
	if err != nil {
		return 0, 0, err
	}
	if res.PnL != nil {
		pnl = *res.PnL
	}
	return pnl, res.Trades, nil
}
