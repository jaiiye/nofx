package store

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// PositionBinsSnapshot is one market's holder cost/liquidation distribution at
// one refresh. The buckets travel as JSON: the metrics derived from them are
// versioned in code, so refining the maths does not require a schema migration.
type PositionBinsSnapshot struct {
	ID               int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	MarketType       string    `gorm:"column:market_type;default:''" json:"market_type"`
	Symbol           string    `gorm:"column:symbol;not null;index:idx_position_bins_symbol_time" json:"symbol"`
	Timestamp        time.Time `gorm:"not null;index:idx_position_bins_symbol_time,sort:desc;index:idx_position_bins_time,sort:desc" json:"timestamp"`
	MarkPrice        float64   `gorm:"column:mark_price;default:0" json:"mark_price"`
	BandPct          float64   `gorm:"column:band_pct;default:0" json:"band_pct"`
	BinCount         int       `gorm:"column:bin_count;default:0" json:"bin_count"`
	AddressesSeen    int       `gorm:"column:addresses_seen;default:0" json:"addresses_seen"`
	PositionsSeen    int       `gorm:"column:positions_seen;default:0" json:"positions_seen"`
	PositionsPlaced  int       `gorm:"column:positions_placed;default:0" json:"positions_placed"`
	PositionsOutside int       `gorm:"column:positions_outside;default:0" json:"positions_outside"`
	WeightSource     string    `gorm:"column:weight_source;default:''" json:"weight_source"`
	BinsJSON         string    `gorm:"column:bins_json;default:''" json:"bins_json"`
	CreatedAt        time.Time `json:"created_at"`
}

func (PositionBinsSnapshot) TableName() string { return "market_position_bins" }

// MarketStructureStore stores per-market holder-structure snapshots. Snapshots
// are what every consumer reads: the decision prompt, the structure gate and the
// offline replay all use a stored row rather than querying the venue again, so a
// page view or a replayed decision never triggers new upstream requests.
type MarketStructureStore struct {
	db *gorm.DB
}

// NewMarketStructureStore creates a MarketStructureStore.
func NewMarketStructureStore(db *gorm.DB) *MarketStructureStore {
	return &MarketStructureStore{db: db}
}

// initTables initializes the structure snapshot table.
func (s *MarketStructureStore) initTables() error {
	// For PostgreSQL with an existing table, skip AutoMigrate (same rule as the
	// other stores, which never run AutoMigrate against a pre-existing PG table).
	if s.db.Dialector.Name() == "postgres" {
		var tableExists int64
		s.db.Raw(`SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'market_position_bins'`).Scan(&tableExists)
		if tableExists > 0 {
			return nil
		}
	}
	if err := s.db.AutoMigrate(&PositionBinsSnapshot{}); err != nil {
		return fmt.Errorf("failed to migrate market_position_bins: %w", err)
	}
	return nil
}

// Save stores one snapshot.
func (s *MarketStructureStore) Save(snapshot *PositionBinsSnapshot) error {
	if snapshot.Timestamp.IsZero() {
		snapshot.Timestamp = time.Now().UTC()
	} else {
		snapshot.Timestamp = snapshot.Timestamp.UTC()
	}
	if err := s.db.Omit("ID").Create(snapshot).Error; err != nil {
		return fmt.Errorf("failed to save position bins snapshot: %w", err)
	}
	return nil
}

// GetLatest returns the newest snapshots for a symbol, oldest first so the result
// reads as a time series.
func (s *MarketStructureStore) GetLatest(symbol string, limit int) ([]*PositionBinsSnapshot, error) {
	var snapshots []*PositionBinsSnapshot
	err := s.db.Where("symbol = ?", symbol).
		Order("timestamp DESC").
		Limit(limit).
		Find(&snapshots).Error
	if err != nil {
		return nil, fmt.Errorf("failed to query position bins: %w", err)
	}
	for i, j := 0, len(snapshots)-1; i < j; i, j = i+1, j-1 {
		snapshots[i], snapshots[j] = snapshots[j], snapshots[i]
	}
	return snapshots, nil
}

// LatestAt returns the newest snapshot at or before the given moment, which is
// the read the offline replay needs: a decision is judged against the structure
// that was known when it was taken. Returns (nil, nil) when nothing is stored.
func (s *MarketStructureStore) LatestAt(symbol string, at time.Time) (*PositionBinsSnapshot, error) {
	var snapshot PositionBinsSnapshot
	err := s.db.Where("symbol = ? AND timestamp <= ?", symbol, at.UTC()).
		Order("timestamp DESC").
		First(&snapshot).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query position bins at %s: %w", at.UTC().Format(time.RFC3339), err)
	}
	return &snapshot, nil
}

// CleanOldRecords deletes snapshots older than the given number of days.
func (s *MarketStructureStore) CleanOldRecords(days int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	result := s.db.Where("timestamp < ?", cutoff).Delete(&PositionBinsSnapshot{})
	if result.Error != nil {
		return 0, fmt.Errorf("failed to clean position bins: %w", result.Error)
	}
	return result.RowsAffected, nil
}
