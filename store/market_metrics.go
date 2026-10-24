package store

import (
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// VenueMetricsRow is one venue's positioning reading for one market at one
// moment. Venues are stored separately rather than summed: the same position
// can be opened on one venue and hedged on another, so an "all-market OI" total
// double-counts and must never be presented as the market's real exposure.
type VenueMetricsRow struct {
	ID          int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	Venue       string    `gorm:"column:venue;not null;uniqueIndex:idx_venue_metrics_unique,priority:1;index:idx_venue_metrics_symbol_time,priority:1" json:"venue"`
	Symbol      string    `gorm:"column:symbol;not null;uniqueIndex:idx_venue_metrics_unique,priority:2;index:idx_venue_metrics_symbol_time,priority:2" json:"symbol"`
	VenueSymbol string    `gorm:"column:venue_symbol;default:''" json:"venue_symbol"`
	Timestamp   time.Time `gorm:"not null;uniqueIndex:idx_venue_metrics_unique,priority:3;index:idx_venue_metrics_symbol_time,priority:3,sort:desc;index:idx_venue_metrics_time,sort:desc" json:"timestamp"`
	MarkPrice   float64   `gorm:"column:mark_price;default:0" json:"mark_price"`
	OIBase      float64   `gorm:"column:oi_base;default:0" json:"oi_base"`
	OIUSD       float64   `gorm:"column:oi_usd;default:0" json:"oi_usd"`
	// FundingRate1hPct is already normalized to an hourly rate so venues with
	// different settlement intervals are comparable. FundingIntervalH keeps the
	// original interval, so a 4h/8h assumption stays visible instead of being
	// silently baked into the number.
	FundingRate1hPct float64 `gorm:"column:funding_rate_1h_pct;default:0" json:"funding_rate_1h_pct"`
	FundingIntervalH float64 `gorm:"column:funding_interval_h;default:0" json:"funding_interval_h"`
	PremiumPct       float64 `gorm:"column:premium_pct;default:0" json:"premium_pct"`
	// SourceOK is false for a degraded read (timeout, region block, partial
	// payload). Degraded rows are written so gaps are visible, but readers drop
	// them rather than treating a zero as a real reading.
	SourceOK  bool      `gorm:"column:source_ok;default:false" json:"source_ok"`
	CreatedAt time.Time `json:"created_at"`
}

func (VenueMetricsRow) TableName() string { return "market_venue_metrics" }

// LiquidationRow is one liquidation print from a venue. Side is normalized to
// "long"/"short" (the liquidated side), because the two venues describe the
// same event with opposite aggressor semantics.
type LiquidationRow struct {
	ID          int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	Venue       string    `gorm:"column:venue;not null;uniqueIndex:idx_liquidations_unique,priority:1;index:idx_liquidations_venue_symbol_time,priority:1" json:"venue"`
	Symbol      string    `gorm:"column:symbol;not null;uniqueIndex:idx_liquidations_unique,priority:2;index:idx_liquidations_venue_symbol_time,priority:2" json:"symbol"`
	VenueSymbol string    `gorm:"column:venue_symbol;not null;uniqueIndex:idx_liquidations_unique,priority:3;index:idx_liquidations_venue_symbol_time,priority:3" json:"venue_symbol"`
	Timestamp   time.Time `gorm:"not null;uniqueIndex:idx_liquidations_unique,priority:4;index:idx_liquidations_venue_symbol_time,priority:4,sort:desc;index:idx_liquidations_time,sort:desc" json:"timestamp"`
	Side        string    `gorm:"column:side;not null;uniqueIndex:idx_liquidations_unique,priority:5" json:"side"`
	Price       float64   `gorm:"column:price;default:0;uniqueIndex:idx_liquidations_unique,priority:6" json:"price"`
	Qty         float64   `gorm:"column:qty;default:0;uniqueIndex:idx_liquidations_unique,priority:7" json:"qty"`
	NotionalUSD float64   `gorm:"column:notional_usd;default:0" json:"notional_usd"`
	Seq         string    `gorm:"column:seq;default:''" json:"seq"`
	CreatedAt   time.Time `json:"created_at"`
}

func (LiquidationRow) TableName() string { return "market_liquidations" }

// MarketMetricsStore stores per-venue positioning snapshots and liquidation
// prints. Both are append-only with a retention window; consumers read either
// the newest rows or the newest row at or before a decision timestamp.
type MarketMetricsStore struct {
	db *gorm.DB
}

func NewMarketMetricsStore(db *gorm.DB) *MarketMetricsStore {
	return &MarketMetricsStore{db: db}
}

func (s *MarketMetricsStore) initTables() error {
	targets := []struct {
		table string
		model any
	}{
		{"market_venue_metrics", &VenueMetricsRow{}},
		{"market_liquidations", &LiquidationRow{}},
	}
	for _, target := range targets {
		if s.db.Dialector.Name() == "postgres" {
			var exists int64
			s.db.Raw(`SELECT COUNT(*) FROM information_schema.tables WHERE table_name = ?`, target.table).Scan(&exists)
			if exists > 0 {
				continue
			}
		}
		if err := s.db.AutoMigrate(target.model); err != nil {
			return fmt.Errorf("failed to migrate %s: %w", target.table, err)
		}
	}
	return nil
}

// SaveVenueMetrics writes a batch of readings. Duplicate (venue, symbol,
// timestamp) rows are ignored, so a retry after a partial failure is safe.
func (s *MarketMetricsStore) SaveVenueMetrics(rows []*VenueMetricsRow) error {
	if len(rows) == 0 {
		return nil
	}
	for _, row := range rows {
		if row.Timestamp.IsZero() {
			row.Timestamp = time.Now().UTC()
		} else {
			row.Timestamp = row.Timestamp.UTC()
		}
	}
	if err := s.db.Clauses(clause.OnConflict{DoNothing: true}).
		CreateInBatches(rows, 100).Error; err != nil {
		return fmt.Errorf("failed to save venue metrics: %w", err)
	}
	return nil
}

// SaveLiquidations writes a batch of prints. The unique index makes a websocket
// replay after a reconnect idempotent, which keeps the summed windows honest.
func (s *MarketMetricsStore) SaveLiquidations(rows []*LiquidationRow) error {
	if len(rows) == 0 {
		return nil
	}
	for _, row := range rows {
		if row.Timestamp.IsZero() {
			row.Timestamp = time.Now().UTC()
		} else {
			row.Timestamp = row.Timestamp.UTC()
		}
	}
	if err := s.db.Clauses(clause.OnConflict{DoNothing: true}).
		CreateInBatches(rows, 200).Error; err != nil {
		return fmt.Errorf("failed to save liquidations: %w", err)
	}
	return nil
}

// LatestAt returns the newest fresh row for one venue/symbol at or before the
// given moment. This is the read the offline replay needs: the reading that was
// known when the decision was taken, not the newest one.
func (s *MarketMetricsStore) LatestAt(venue, symbol string, at time.Time) (*VenueMetricsRow, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	var row VenueMetricsRow
	err := s.db.Where("venue = ? AND symbol = ? AND timestamp <= ? AND source_ok = ?",
		venue, symbol, at.UTC(), true).
		Order("timestamp DESC").
		First(&row).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query venue metrics at %s: %w", at.UTC().Format(time.RFC3339), err)
	}
	return &row, nil
}

// LiquidationSum aggregates the liquidated notional per side over a window.
func (s *MarketMetricsStore) LiquidationSum(symbol string, from, to time.Time) (longUSD, shortUSD float64, count int64, err error) {
	var rows []struct {
		Side  string
		Total float64
		Count int64
	}
	err = s.db.Model(&LiquidationRow{}).
		Select("side, COALESCE(SUM(notional_usd), 0) AS total, COUNT(*) AS count").
		Where("symbol = ? AND timestamp >= ? AND timestamp <= ?", symbol, from.UTC(), to.UTC()).
		Group("side").
		Scan(&rows).Error
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to aggregate liquidations: %w", err)
	}
	for _, row := range rows {
		switch row.Side {
		case "long":
			longUSD = row.Total
		case "short":
			shortUSD = row.Total
		}
		count += row.Count
	}
	return longUSD, shortUSD, count, nil
}

// CleanOldRecords deletes snapshots and prints older than the given number of
// days. A non-positive window is a no-op rather than "delete everything".
func (s *MarketMetricsStore) CleanOldRecords(days int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	cutoff := time.Now().AddDate(0, 0, -days).UTC()

	metrics := s.db.Where("timestamp < ?", cutoff).Delete(&VenueMetricsRow{})
	if metrics.Error != nil {
		return 0, fmt.Errorf("failed to clean venue metrics: %w", metrics.Error)
	}
	prints := s.db.Where("timestamp < ?", cutoff).Delete(&LiquidationRow{})
	if prints.Error != nil {
		return metrics.RowsAffected, fmt.Errorf("failed to clean liquidations: %w", prints.Error)
	}
	return metrics.RowsAffected + prints.RowsAffected, nil
}
