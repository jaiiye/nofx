package trader

import (
	"path/filepath"
	"testing"
	"time"

	"nofx/store"
)

func newRecoveryTestTrader(t *testing.T) (*AutoTrader, *store.Store) {
	t.Helper()
	st, err := store.NewWithConfig(store.DBConfig{
		Type: store.DBTypeSQLite,
		Path: filepath.Join(t.TempDir(), "recovery.db"),
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	at := &AutoTrader{
		id:           "t1",
		name:         "recovery-test",
		store:        st,
		peakPnLCache: make(map[string]float64),
	}
	return at, st
}

func logDecision(t *testing.T, st *store.Store, at time.Time, success bool, errMsg string) {
	t.Helper()
	err := st.Decision().LogDecision(&store.DecisionRecord{
		TraderID:     "t1",
		Timestamp:    at.UTC(),
		Success:      success,
		ErrorMessage: errMsg,
	})
	if err != nil {
		t.Fatalf("log decision: %v", err)
	}
}

const aiFailureMsg = "Failed to get AI decision: boom"

func TestRecoverSafeModeRestoresAfterFailures(t *testing.T) {
	at, st := newRecoveryTestTrader(t)
	defer st.Close()

	base := time.Now().UTC().Add(-time.Hour)
	logDecision(t, st, base, false, aiFailureMsg)
	logDecision(t, st, base.Add(time.Minute), false, aiFailureMsg)
	logDecision(t, st, base.Add(2*time.Minute), false, aiFailureMsg)

	at.recoverSafeMode()

	if !at.safeMode {
		t.Fatalf("expected safe mode after 3 consecutive AI failures")
	}
	if at.consecutiveAIFailures != 3 {
		t.Fatalf("consecutiveAIFailures = %d, want 3", at.consecutiveAIFailures)
	}
}

func TestRecoverSafeModeIgnoresNonAIFailures(t *testing.T) {
	at, st := newRecoveryTestTrader(t)
	defer st.Close()

	base := time.Now().UTC().Add(-time.Hour)
	// Three failures, but none of them from the model call (e.g. context
	// build errors) — these must not restore safe mode.
	logDecision(t, st, base, false, "Failed to build trading context: boom")
	logDecision(t, st, base.Add(time.Minute), false, "Failed to build trading context: boom")
	logDecision(t, st, base.Add(2*time.Minute), false, "Failed to build trading context: boom")

	at.recoverSafeMode()

	if at.safeMode {
		t.Fatalf("non-AI failures must not restore safe mode")
	}
	if at.consecutiveAIFailures != 0 {
		t.Fatalf("consecutiveAIFailures = %d, want 0", at.consecutiveAIFailures)
	}
}

func TestRecoverSafeModeResetsOnSuccess(t *testing.T) {
	at, st := newRecoveryTestTrader(t)
	defer st.Close()

	base := time.Now().UTC().Add(-time.Hour)
	logDecision(t, st, base, false, aiFailureMsg)
	logDecision(t, st, base.Add(time.Minute), false, aiFailureMsg)
	logDecision(t, st, base.Add(2*time.Minute), true, "") // most recent succeeded

	at.recoverSafeMode()

	if at.safeMode {
		t.Fatalf("did not expect safe mode when the latest cycle succeeded")
	}
	if at.consecutiveAIFailures != 0 {
		t.Fatalf("consecutiveAIFailures = %d, want 0", at.consecutiveAIFailures)
	}
}
