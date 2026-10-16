package store

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Positions created from the exchange fill stream start with a placeholder
// leverage; the trading loop reconciles it with the venue figure through this
// update. The stored PnL% is net/margin, so a stale leverage skews it.
func TestUpdateLeverageOnlyTouchesMatchingOpenPosition(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&TraderPosition{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store := &PositionStore{db: db}

	seed := func(symbol, side, status string, leverage int) int64 {
		pos := &TraderPosition{TraderID: "t1", Symbol: symbol, Side: side, Quantity: 1, EntryPrice: 100, Leverage: leverage, Status: status}
		if err := store.Create(pos); err != nil {
			t.Fatalf("create %s/%s: %v", symbol, side, err)
		}
		return pos.ID
	}
	openID := seed("SOLUSDT", "LONG", "OPEN", 1)
	otherSideID := seed("SOLUSDT", "SHORT", "OPEN", 1)
	otherSymbolID := seed("BTCUSDT", "LONG", "OPEN", 1)
	// Close this one through the real path: Create() forces status=OPEN.
	closedID := seed("SOLUSDT", "LONG", "OPEN", 1)
	if err := store.ClosePosition(closedID, 101, "exit-1", 0.5, 0.01, "test"); err != nil {
		t.Fatalf("ClosePosition: %v", err)
	}

	if err := store.UpdateLeverage("t1", "SOLUSDT", "long", 5); err != nil {
		t.Fatalf("UpdateLeverage: %v", err)
	}

	check := func(id int64, want int, label string) {
		var pos TraderPosition
		if err := db.First(&pos, id).Error; err != nil {
			t.Fatalf("load %s: %v", label, err)
		}
		if pos.Leverage != want {
			t.Fatalf("%s leverage = %d, want %d", label, pos.Leverage, want)
		}
	}
	check(openID, 5, "matching open position")
	check(otherSideID, 1, "same symbol other side")
	check(closedID, 1, "closed position")
	check(otherSymbolID, 1, "other symbol")

	// A non-positive leverage is a no-op, never an overwrite with garbage.
	if err := store.UpdateLeverage("t1", "SOLUSDT", "LONG", 0); err != nil {
		t.Fatalf("UpdateLeverage(0): %v", err)
	}
	check(openID, 5, "after no-op update")
}
