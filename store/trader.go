package store

import (
	"fmt"
	"nofx/logger"
	"time"

	"gorm.io/gorm"
)

// TraderStore trader storage
type TraderStore struct {
	db *gorm.DB
}

// NewTraderStore creates a new trader store
func NewTraderStore(db *gorm.DB) *TraderStore {
	return &TraderStore{db: db}
}

// Trader trader configuration
type Trader struct {
	ID             string  `gorm:"primaryKey" json:"id"`
	UserID         string  `gorm:"column:user_id;not null;default:default;index" json:"user_id"`
	Name           string  `gorm:"column:name;not null" json:"name"`
	AIModelID      string  `gorm:"column:ai_model_id;not null" json:"ai_model_id"`
	ExchangeID     string  `gorm:"column:exchange_id;not null" json:"exchange_id"`
	StrategyID     string  `gorm:"column:strategy_id;default:''" json:"strategy_id"`
	InitialBalance float64 `gorm:"column:initial_balance;not null" json:"initial_balance"`
	// Cash-flow neutral PnL baseline: the wallet balance and timestamp of the
	// last reconciliation. Wallet moves that realized PnL does not explain are
	// deposits/withdrawals, and are folded into InitialBalance so PnL and PnL%
	// stay meaningful (a deposit used to show up as profit). A wallet of 0 means
	// the starting point has not been observed yet — the account was re-anchored
	// and the next cycle adopts its wallet without folding the difference.
	PnLBaselineWallet float64 `gorm:"column:pnl_baseline_wallet;default:0" json:"pnl_baseline_wallet"`
	PnLBaselineAtMs   int64   `gorm:"column:pnl_baseline_at_ms;default:0" json:"pnl_baseline_at_ms"`
	// FundingSyncedAtMs is the watermark for venue funding payments already
	// applied to position records (funding is charged hourly on Hyperliquid, so
	// multi-day holds accumulate a real carry cost).
	FundingSyncedAtMs   int64     `gorm:"column:funding_synced_at_ms;default:0" json:"funding_synced_at_ms"`
	ScanIntervalMinutes int       `gorm:"column:scan_interval_minutes;default:3" json:"scan_interval_minutes"`
	IsRunning           bool      `gorm:"column:is_running;default:false" json:"is_running"`
	IsCrossMargin       bool      `gorm:"column:is_cross_margin;default:true" json:"is_cross_margin"`
	ShowInCompetition   bool      `gorm:"column:show_in_competition;default:true" json:"show_in_competition"`
	PaperTrading        bool      `gorm:"column:paper_trading;default:false" json:"paper_trading"`
	CreatedAt           time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt           time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`

	// Following fields are deprecated, kept for backward compatibility, new traders should use StrategyID
	BTCETHLeverage       int    `gorm:"column:btc_eth_leverage;default:5" json:"btc_eth_leverage,omitempty"`
	AltcoinLeverage      int    `gorm:"column:altcoin_leverage;default:5" json:"altcoin_leverage,omitempty"`
	TradingSymbols       string `gorm:"column:trading_symbols;default:''" json:"trading_symbols,omitempty"`
	UseAI500             bool   `gorm:"column:use_coin_pool;default:false" json:"use_ai500,omitempty"`
	UseOITop             bool   `gorm:"column:use_oi_top;default:false" json:"use_oi_top,omitempty"`
	CustomPrompt         string `gorm:"column:custom_prompt;default:''" json:"custom_prompt,omitempty"`
	OverrideBasePrompt   bool   `gorm:"column:override_base_prompt;default:false" json:"override_base_prompt,omitempty"`
	SystemPromptTemplate string `gorm:"column:system_prompt_template;default:default" json:"system_prompt_template,omitempty"`
}

// TableName returns the table name for Trader
func (Trader) TableName() string {
	return "traders"
}

// TraderFullConfig trader full configuration (includes AI model, exchange and strategy)
type TraderFullConfig struct {
	Trader   *Trader
	AIModel  *AIModel
	Exchange *Exchange
	Strategy *Strategy
}

func (s *TraderStore) initTables() error {
	// For PostgreSQL with existing table, skip AutoMigrate
	if s.db.Dialector.Name() == "postgres" {
		var tableExists int64
		s.db.Raw(`SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'traders'`).Scan(&tableExists)
		if tableExists > 0 {
			// AutoMigrate is skipped for pre-existing PG tables, so every column
			// added after the table was created has to be ensured here. A missing
			// baseline column is not cosmetic: the cash-flow ledger cannot read
			// its own state, bails out on every cycle, and deposits are counted
			// as profit again.
			for _, field := range []string{"PaperTrading", "PnLBaselineWallet", "PnLBaselineAtMs", "FundingSyncedAtMs"} {
				if err := s.ensureColumn(field); err != nil {
					return err
				}
			}
			return nil
		}
	}
	// Use GORM AutoMigrate
	if err := s.db.AutoMigrate(&Trader{}); err != nil {
		return fmt.Errorf("failed to migrate traders table: %w", err)
	}
	return nil
}

// ensureColumn adds one column to a traders table that predates it, through the
// dialect-agnostic migrator instead of hand-written DDL.
func (s *TraderStore) ensureColumn(field string) error {
	if s.db.Migrator().HasColumn(&Trader{}, field) {
		return nil
	}
	if err := s.db.Migrator().AddColumn(&Trader{}, field); err != nil {
		return fmt.Errorf("failed to add traders.%s: %w", field, err)
	}
	return nil
}

// Create creates trader
func (s *TraderStore) Create(trader *Trader) error {
	return s.db.Create(trader).Error
}

// List gets user's trader list
func (s *TraderStore) List(userID string) ([]*Trader, error) {
	var traders []*Trader
	err := s.db.Where("user_id = ?", userID).
		Order("created_at DESC").
		Find(&traders).Error
	if err != nil {
		return nil, err
	}
	return traders, nil
}

// UpdateStatus updates trader running status
func (s *TraderStore) UpdateStatus(userID, id string, isRunning bool) error {
	return s.db.Model(&Trader{}).
		Where("id = ? AND user_id = ?", id, userID).
		Update("is_running", isRunning).Error
}

// UpdateShowInCompetition updates trader competition visibility
func (s *TraderStore) UpdateShowInCompetition(userID, id string, showInCompetition bool) error {
	return s.db.Model(&Trader{}).
		Where("id = ? AND user_id = ?", id, userID).
		Update("show_in_competition", showInCompetition).Error
}

// Update updates trader configuration
func (s *TraderStore) Update(trader *Trader) error {
	updates := map[string]interface{}{
		"name":                   trader.Name,
		"ai_model_id":            trader.AIModelID,
		"exchange_id":            trader.ExchangeID,
		"strategy_id":            trader.StrategyID,
		"is_cross_margin":        trader.IsCrossMargin,
		"show_in_competition":    trader.ShowInCompetition,
		"paper_trading":          trader.PaperTrading,
		"btc_eth_leverage":       trader.BTCETHLeverage,
		"altcoin_leverage":       trader.AltcoinLeverage,
		"trading_symbols":        trader.TradingSymbols,
		"use_coin_pool":          trader.UseAI500,
		"use_oi_top":             trader.UseOITop,
		"custom_prompt":          trader.CustomPrompt,
		"override_base_prompt":   trader.OverrideBasePrompt,
		"system_prompt_template": trader.SystemPromptTemplate,
	}

	// Only update these if > 0
	if trader.InitialBalance > 0 {
		updates["initial_balance"] = trader.InitialBalance
	}
	if trader.ScanIntervalMinutes > 0 {
		updates["scan_interval_minutes"] = trader.ScanIntervalMinutes
		logger.Debugf("TraderStore.Update: scan_interval_minutes=%d will be saved", trader.ScanIntervalMinutes)
	} else {
		logger.Debugf("TraderStore.Update: scan_interval_minutes=%d (<=0, NOT updating)", trader.ScanIntervalMinutes)
	}

	return s.db.Model(&Trader{}).
		Where("id = ? AND user_id = ?", trader.ID, trader.UserID).
		Updates(updates).Error
}

// UpdateInitialBalance updates initial balance
func (s *TraderStore) UpdateInitialBalance(userID, id string, newBalance float64) error {
	return s.db.Model(&Trader{}).
		Where("id = ? AND user_id = ?", id, userID).
		Update("initial_balance", newBalance).Error
}

// GetFundingSyncPoint returns the venue-funding watermark (ms) for a trader.
func (s *TraderStore) GetFundingSyncPoint(traderID string) (int64, error) {
	var row struct{ FundingSyncedAtMs int64 }
	res := s.db.Model(&Trader{}).
		Select("funding_synced_at_ms").
		Where("id = ?", traderID).
		Scan(&row)
	if res.Error != nil {
		return 0, res.Error
	}
	return row.FundingSyncedAtMs, nil
}

// AdvanceFundingSyncPoint moves the venue-funding watermark forward.
func (s *TraderStore) AdvanceFundingSyncPoint(traderID string, atMs int64) error {
	return s.db.Model(&Trader{}).
		Where("id = ?", traderID).
		Update("funding_synced_at_ms", atMs).Error
}

// GetPnLBaseline returns the last cash-flow reconciliation point.
func (s *TraderStore) GetPnLBaseline(userID, id string) (wallet float64, atMs int64, err error) {
	// The column tags are load-bearing: GORM derives `pn_l_baseline_wallet` from
	// the field name (the "PnL" initialism breaks its naming rule), which matched
	// no column and silently returned zero values with no error — so the stored
	// point was never adopted and every restart re-anchored the ledger to the
	// current wallet.
	var row struct {
		PnLBaselineWallet float64 `gorm:"column:pnl_baseline_wallet"`
		PnLBaselineAtMs   int64   `gorm:"column:pnl_baseline_at_ms"`
	}
	res := s.db.Model(&Trader{}).
		Select("pnl_baseline_wallet", "pnl_baseline_at_ms").
		Where("id = ? AND user_id = ?", id, userID).
		Scan(&row)
	if res.Error != nil {
		return 0, 0, res.Error
	}
	return row.PnLBaselineWallet, row.PnLBaselineAtMs, nil
}

// SetPnLBaseline writes the cash-flow neutral baseline outright and advances the
// reconciliation point. Callers pass the baseline they want persisted — a
// deposit/withdrawal folded into it, a value re-derived after an upgrade, or a
// re-anchor after the account changed — so the in-memory value and the row
// cannot diverge the way a relative `initial_balance + ?` update allowed.
//
// wallet is the wallet balance the baseline was reconciled against, or 0 when the
// starting point has not been observed yet (a re-anchored account): the next
// cycle then adopts its wallet without folding the difference.
func (s *TraderStore) SetPnLBaseline(userID, id string, baseline, wallet float64, atMs int64) error {
	return s.db.Model(&Trader{}).
		Where("id = ? AND user_id = ?", id, userID).
		Updates(map[string]interface{}{
			"initial_balance":     baseline,
			"pnl_baseline_wallet": wallet,
			"pnl_baseline_at_ms":  atMs,
		}).Error
}

// AdvancePnLBaseline moves the reconciliation point without adjusting the
// baseline (no cash flow detected this cycle).
func (s *TraderStore) AdvancePnLBaseline(userID, id string, wallet float64, atMs int64) error {
	return s.db.Model(&Trader{}).
		Where("id = ? AND user_id = ?", id, userID).
		Updates(map[string]interface{}{
			"pnl_baseline_wallet": wallet,
			"pnl_baseline_at_ms":  atMs,
		}).Error
}

// UpdateCustomPrompt updates custom prompt
func (s *TraderStore) UpdateCustomPrompt(userID, id string, customPrompt string, overrideBase bool) error {
	return s.db.Model(&Trader{}).
		Where("id = ? AND user_id = ?", id, userID).
		Updates(map[string]interface{}{
			"custom_prompt":        customPrompt,
			"override_base_prompt": overrideBase,
		}).Error
}

// Delete deletes trader and associated data
func (s *TraderStore) Delete(userID, id string) error {
	// Delete associated equity snapshots first
	s.db.Where("trader_id = ?", id).Delete(&EquitySnapshot{})

	// Delete the trader
	return s.db.Where("id = ? AND user_id = ?", id, userID).Delete(&Trader{}).Error
}

// GetFullConfig gets trader full configuration
func (s *TraderStore) GetFullConfig(userID, traderID string) (*TraderFullConfig, error) {
	var trader Trader
	err := s.db.Where("id = ? AND user_id = ?", traderID, userID).First(&trader).Error
	if err != nil {
		return nil, err
	}

	// Get AI model
	var aiModel AIModel
	err = s.db.Where("id = ? AND user_id = ?", trader.AIModelID, userID).First(&aiModel).Error
	if err != nil {
		return nil, fmt.Errorf("failed to get AI model: %w", err)
	}

	// Get exchange
	var exchange Exchange
	err = s.db.Where("id = ? AND user_id = ?", trader.ExchangeID, userID).First(&exchange).Error
	if err != nil {
		return nil, fmt.Errorf("failed to get exchange: %w", err)
	}

	// Load associated strategy
	var strategy *Strategy
	if trader.StrategyID != "" {
		strategy, _ = s.getStrategyByID(userID, trader.StrategyID)
	}
	// If no associated strategy, get user's active strategy or default strategy
	if strategy == nil {
		strategy, _ = s.getActiveOrDefaultStrategy(userID)
	}

	return &TraderFullConfig{
		Trader:   &trader,
		AIModel:  &aiModel,
		Exchange: &exchange,
		Strategy: strategy,
	}, nil
}

// getStrategyByID internal method: gets strategy by ID
func (s *TraderStore) getStrategyByID(userID, strategyID string) (*Strategy, error) {
	var strategy Strategy
	err := s.db.Where("id = ? AND (user_id = ? OR is_default = ?)", strategyID, userID, true).
		First(&strategy).Error
	if err != nil {
		return nil, err
	}
	return &strategy, nil
}

// getActiveOrDefaultStrategy internal method: gets user's active strategy or system default strategy
func (s *TraderStore) getActiveOrDefaultStrategy(userID string) (*Strategy, error) {
	var strategy Strategy

	// First try to get user's active strategy
	err := s.db.Where("user_id = ? AND is_active = ?", userID, true).First(&strategy).Error
	if err == nil {
		return &strategy, nil
	}

	// Fallback to system default strategy
	err = s.db.Where("is_default = ?", true).First(&strategy).Error
	if err != nil {
		return nil, err
	}
	return &strategy, nil
}

// GetByID gets a trader by ID without requiring userID (for public APIs)
func (s *TraderStore) GetByID(traderID string) (*Trader, error) {
	var trader Trader
	err := s.db.Where("id = ?", traderID).First(&trader).Error
	if err != nil {
		return nil, err
	}
	return &trader, nil
}

// ListAll gets all traders
func (s *TraderStore) ListAll() ([]*Trader, error) {
	var traders []*Trader
	err := s.db.Order("created_at DESC").Find(&traders).Error
	if err != nil {
		return nil, err
	}
	return traders, nil
}

// ListByExchangeID gets traders that use a specific exchange
func (s *TraderStore) ListByExchangeID(userID, exchangeID string) ([]*Trader, error) {
	var traders []*Trader
	err := s.db.Where("user_id = ? AND exchange_id = ?", userID, exchangeID).Find(&traders).Error
	if err != nil {
		return nil, err
	}
	return traders, nil
}

// ListByAIModelID gets traders that use a specific AI model
func (s *TraderStore) ListByAIModelID(userID, aiModelID string) ([]*Trader, error) {
	var traders []*Trader
	err := s.db.Where("user_id = ? AND ai_model_id = ?", userID, aiModelID).Find(&traders).Error
	if err != nil {
		return nil, err
	}
	return traders, nil
}
