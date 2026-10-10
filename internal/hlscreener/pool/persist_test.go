package pool

import (
	"context"
	"testing"

	"nofx/internal/hlscreener/store"
)

// TestPersistResults_PromotesNearMisses — when no coin passes all 5
// layers, the top-scoring coins are still written to the active pool
// (marked selection="top_score") so downstream consumers that gate on
// the pool never see an empty list.
func TestPersistResults_PromotesNearMisses(t *testing.T) {
	st := &stubStore{}
	s := New(&stubSource{}, st, DefaultThresholds()).WithMinPool(3)

	results := []Result{
		{Coin: "AAA", Score: 90, InPool: false, Layers: map[string]LayerResult{}},
		{Coin: "BBB", Score: 80, InPool: false, Layers: map[string]LayerResult{}},
		{Coin: "CCC", Score: 70, InPool: false, Layers: map[string]LayerResult{}},
		{Coin: "DDD", Score: 60, InPool: false, Layers: map[string]LayerResult{}},
	}
	if err := s.PersistResults(context.Background(), results); err != nil {
		t.Fatal(err)
	}
	if len(st.activePool) != 3 {
		t.Fatalf("pool size = %d, want 3 (minPool)", len(st.activePool))
	}
	// Highest scores win, all flagged as promoted.
	if st.activePool[0].Coin != "AAA" || st.activePool[1].Coin != "BBB" || st.activePool[2].Coin != "CCC" {
		t.Errorf("unexpected selection order: %+v", st.activePool)
	}
	for _, c := range st.activePool {
		if c.Reasons["selection"] != "top_score" {
			t.Errorf("%s: selection = %v, want top_score", c.Coin, c.Reasons["selection"])
		}
		if !c.InPool {
			t.Errorf("%s: written candidate must have InPool=true", c.Coin)
		}
	}
}

// TestPersistResults_StrictPassesTakePriority — strict passes are
// written first and only the remainder of minPool is topped up.
func TestPersistResults_StrictPassesTakePriority(t *testing.T) {
	st := &stubStore{}
	s := New(&stubSource{}, st, DefaultThresholds()).WithMinPool(3)

	results := []Result{
		{Coin: "GOOD", Score: 50, InPool: true, Layers: map[string]LayerResult{}},
		{Coin: "NEAR1", Score: 95, InPool: false, Layers: map[string]LayerResult{}},
		{Coin: "NEAR2", Score: 94, InPool: false, Layers: map[string]LayerResult{}},
	}
	if err := s.PersistResults(context.Background(), results); err != nil {
		t.Fatal(err)
	}
	if len(st.activePool) != 3 {
		t.Fatalf("pool size = %d, want 3", len(st.activePool))
	}
	// Strict pass is first despite lower score (ordering: strict then promoted).
	if st.activePool[0].Coin != "GOOD" || st.activePool[0].Reasons["selection"] != "strict" {
		t.Errorf("strict pass should lead the pool: %+v", st.activePool[0])
	}
	if st.activePool[1].Coin != "NEAR1" || st.activePool[2].Coin != "NEAR2" {
		t.Errorf("promoted coins should follow by score: %+v", st.activePool)
	}
}

// TestPersistResults_MoreStrictThanMinPool — no promotion when enough
// coins already pass.
func TestPersistResults_MoreStrictThanMinPool(t *testing.T) {
	st := &stubStore{}
	s := New(&stubSource{}, st, DefaultThresholds()).WithMinPool(2)

	results := []Result{
		{Coin: "A", Score: 10, InPool: true, Layers: map[string]LayerResult{}},
		{Coin: "B", Score: 9, InPool: true, Layers: map[string]LayerResult{}},
		{Coin: "C", Score: 100, InPool: false, Layers: map[string]LayerResult{}},
	}
	if err := s.PersistResults(context.Background(), results); err != nil {
		t.Fatal(err)
	}
	if len(st.activePool) != 2 {
		t.Fatalf("pool size = %d, want 2 (no near-miss needed)", len(st.activePool))
	}
	for _, c := range st.activePool {
		if c.Reasons["selection"] != "strict" {
			t.Errorf("%s: selection = %v, want strict", c.Coin, c.Reasons["selection"])
		}
	}
}

// TestPersistResults_EmptyInput — nothing in, nothing out (and no panic).
func TestPersistResults_EmptyInput(t *testing.T) {
	st := &stubStore{}
	s := New(&stubSource{}, st, DefaultThresholds())
	if err := s.PersistResults(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(st.activePool) != 0 {
		t.Errorf("expected empty pool, got %d", len(st.activePool))
	}
}

var _ = store.Candidate{} // keep store import for the fixture type above
