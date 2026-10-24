package trader

import (
	"path/filepath"
	"testing"

	"nofx/kernel"
	"nofx/store"
)

func TestCheckOpenProtectionsFailsClosedAfterRepeatedErrors(t *testing.T) {
	st, err := store.NewWithConfig(store.DBConfig{
		Type: store.DBTypeSQLite,
		Path: filepath.Join(t.TempDir(), "prot.db"),
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	cfg := store.GetDefaultStrategyConfig("en")
	engine := kernel.NewStrategyEngine(&cfg)
	at := &AutoTrader{id: "t1", name: "prot-test", store: st, strategyEngine: engine}

	// Healthy store, no positions: nothing to block.
	if r := at.checkOpenProtections("BTCUSDT"); r != "" {
		t.Fatalf("unexpected block on healthy store: %q", r)
	}

	// Break the backing store so every protection query errors.
	st.Close()

	last := ""
	for i := 0; i < protectionFailureLimit; i++ {
		last = at.checkOpenProtections("BTCUSDT")
	}
	if last == "" {
		t.Fatalf("expected fail-closed block after %d failing passes", protectionFailureLimit)
	}
}
