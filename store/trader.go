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
	// stay meaningful (a deposit used to show up as profit).
	PnLBaselineWallet   float64   `gorm:"column:pnl_baseline_wallet;default:0" json:"pnl_baseline_wallet"`
	PnLBaselineAtMs     int64     `gorm:"column:pnl_baseline_at_ms;default:0" json:"pnl_baseline_at_ms"`
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
			// Add newer columns to pre-existing tables (AutoMigrate is skipped for PG).
			if err := s.db.Exec(`ALTER TABLE traders ADD COLUMN IF NOT EXISTS paper_trading BOOLEAN DEFAULT false`).Error; err != nil {
				return fmt.Errorf("failed to add paper_trading column: %w", err)
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

// GetPnLBaseline returns the last cash-flow reconciliation point.
func (s *TraderStore) GetPnLBaseline(userID, id string) (wallet float64, atMs int64, err error) {
	var row struct {
		PnLBaselineWallet float64
		PnLBaselineAtMs   int64
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

// ApplyCashFlow folds an external deposit/withdrawal into the PnL baseline and
// advances the reconciliation point. Adjusting InitialBalance keeps PnL and
// PnL% meaningful without touching every consumer.
func (s *TraderStore) ApplyCashFlow(userID, id string, cashFlow, wallet float64, atMs int64) error {
	return s.db.Model(&Trader{}).
		Where("id = ? AND user_id = ?", id, userID).
		Updates(map[string]interface{}{
			"initial_balance":     gorm.Expr("initial_balance + ?", cashFlow),
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
