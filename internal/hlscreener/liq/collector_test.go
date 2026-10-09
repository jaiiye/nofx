package liq

import (
	"context"
	"math"
	"sync"
	"testing"

	"nofx/internal/hlscreener/hyperliquid"
)

// stubSource implements Source for tests.
type stubSource struct {
	ctxs *hyperliquid.AssetContexts
	err  error
}

func (s *stubSource) AssetContexts(_ context.Context) (*hyperliquid.AssetContexts, error) {
	return s.ctxs, s.err
}

// stubStore implements Store for tests.
type stubStore struct {
	mu    sync.Mutex
	levels []LiqLevel
}

func (s *stubStore) UpsertLiqLevel(_ context.Context, l LiqLevel) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.levels = append(s.levels, l)
	return nil
}

func (s *stubStore) Levels() []LiqLevel {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]LiqLevel, len(s.levels))
	copy(out, s.levels)
	return out
}

func makeCtxs(coin string, mark, oiContracts float64) *hyperliquid.AssetContexts {
	return &hyperliquid.AssetContexts{
		Universe: []hyperliquid.UniverseAsset{{Name: coin}},
		Contexts: []hyperliquid.AssetCtx{
			{
				MarkPx:       ftoa(mark),
				OpenInterest: ftoa(oiContracts),
			},
		},
	}
}

func ftoa(f float64) string {
	return formatFloat(f)
}

func formatFloat(f float64) string {
	// minimal "%g" formatter to avoid importing strconv from this
	// helper file (test-only).
	if f == 0 {
		return "0"
	}
	neg := f < 0
	if neg {
		f = -f
	}
	intPart := int64(f)
	frac := f - float64(intPart)
	out := []byte{}
	if neg {
		out = append(out, '-')
	}
	// integer part
	if intPart == 0 {
		out = append(out, '0')
	} else {
		var digits []byte
		for intPart > 0 {
			digits = append([]byte{byte('0' + intPart%10)}, digits...)
			intPart /= 10
		}
		out = append(out, digits...)
	}
	// fractional (4 digits)
	out = append(out, '.')
	frac *= 10000
	for i := 0; i < 4; i++ {
		out = append(out, byte('0'+byte(int(frac)%10)))
		frac *= 10
	}
	return string(out)
}

func TestSnapshot_BasicShape(t *testing.T) {
	src := &stubSource{
		ctxs: makeCtxs("BTC", 100_000, 1_000), // 1k contracts × 100k = $100M OI
	}
	store := &stubStore{}
	col := New("BTC", src, store, 10) // 0.1% bins, default 100/side

	if err := col.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	levels := store.Levels()
	if len(levels) == 0 {
		t.Fatal("no liq levels written")
	}
	// We should have bins that capture liquidations between 0%
	// and ~50% price move (the inner bins where positions liquidate
	// at high leverage). Past 50% the curve is saturated so
	// marginal = 0 and those bins are skipped.
	if len(levels) < 50 {
		t.Errorf("only %d bins written; expected at least 50", len(levels))
	}
	if len(levels) > 201 {
		t.Errorf("too many bins: %d (max 201)", len(levels))
	}

	// Sanity: first bin below mark, last above
	for _, l := range levels {
		if l.Coin != "BTC" {
			t.Errorf("wrong coin: %s", l.Coin)
		}
		if l.BinStart >= l.BinEnd {
			t.Errorf("bad bin: start %v >= end %v", l.BinStart, l.BinEnd)
		}
		if l.NotionalUSD < 0 {
			t.Errorf("negative notional: %v", l.NotionalUSD)
		}
	}
}

func TestSnapshot_TotalNotionalBounded(t *testing.T) {
	// The cumulative notional across all bins should not exceed
	// total OI (one-side) — by construction, since cumAtMove
	// asymptotes at 1.0.
	src := &stubSource{
		ctxs: makeCtxs("ETH", 3_000, 100_000), // 100k contracts × 3k = $300M OI
	}
	store := &stubStore{}
	col := New("ETH", src, store, 10)

	if err := col.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	var total float64
	for _, l := range store.Levels() {
		total += l.NotionalUSD
	}
	oneSide := 300_000_000.0 / 2 // 50/50 split
	// Sum should approach but not exceed oneSide
	if total > oneSide*1.01 {
		t.Errorf("total notional %v exceeds one-side OI %v", total, oneSide)
	}
	// Sanity: should be at least 30% of oneSide (we model a
	// substantial fraction of positions as liquidatable within ±10%)
	if total < oneSide*0.3 {
		t.Errorf("total notional %v suspiciously low (one-side %v)", total, oneSide)
	}
}

func TestSnapshot_MonotonicFromMark(t *testing.T) {
	// Bins far from the mark should have more notional than bins
	// close to the mark (cumulative curve is monotonic).
	src := &stubSource{
		ctxs: makeCtxs("SOL", 150, 1_000_000), // big OI
	}
	store := &stubStore{}
	col := New("SOL", src, store, 10)

	if err := col.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	levels := store.Levels()
	if len(levels) < 50 {
		t.Skip("not enough levels")
	}

	// Walk outward from index ~100 (the mark) in both directions;
	// cumulative notional should not decrease.
	mid := len(levels) / 2
	var cumBelow, cumAbove float64
	prevCumBelow := 0.0
	prevCumAbove := 0.0
	for offset := 1; offset < 50; offset++ {
		if mid-offset >= 0 {
			cumBelow += levels[mid-offset].NotionalUSD
			if cumBelow < prevCumBelow-1 {
				t.Errorf("below: cum not monotonic at offset %d (%v < %v)",
					offset, cumBelow, prevCumBelow)
			}
			prevCumBelow = cumBelow
		}
		if mid+offset < len(levels) {
			cumAbove += levels[mid+offset].NotionalUSD
			if cumAbove < prevCumAbove-1 {
				t.Errorf("above: cum not monotonic at offset %d (%v < %v)",
					offset, cumAbove, prevCumAbove)
			}
			prevCumAbove = cumAbove
		}
	}
}

func TestSnapshot_CoinNotFound(t *testing.T) {
	src := &stubSource{
		ctxs: &hyperliquid.AssetContexts{
			Universe: []hyperliquid.UniverseAsset{{Name: "BTC"}},
			Contexts: []hyperliquid.AssetCtx{{MarkPx: "100", OpenInterest: "1000"}},
		},
	}
	store := &stubStore{}
	col := New("ETH", src, store, 10) // looking for ETH
	if err := col.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(store.Levels()); got != 0 {
		t.Errorf("expected 0 levels for missing coin, got %d", got)
	}
}

func TestSnapshot_ZeroMark(t *testing.T) {
	src := &stubSource{
		ctxs: &hyperliquid.AssetContexts{
			Universe: []hyperliquid.UniverseAsset{{Name: "X"}},
			Contexts: []hyperliquid.AssetCtx{{MarkPx: "0", OpenInterest: "1000"}},
		},
	}
	store := &stubStore{}
	col := New("X", src, store, 10)
	if err := col.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(store.Levels()); got != 0 {
		t.Errorf("expected 0 levels for zero mark, got %d", got)
	}
}

func TestStoreFunc(t *testing.T) {
	var captured LiqLevel
	f := StoreFunc(func(_ context.Context, l LiqLevel) error {
		captured = l
		return nil
	})
	if err := f.UpsertLiqLevel(context.Background(), LiqLevel{
		Coin: "X", BinStart: 1, BinEnd: 2, NotionalUSD: 100,
	}); err != nil {
		t.Fatal(err)
	}
	if captured.Coin != "X" || captured.BinStart != 1 {
		t.Errorf("captured = %+v", captured)
	}
}

func TestNew_Defaults(t *testing.T) {
	src := &stubSource{}
	store := &stubStore{}
	col := New("BTC", src, store, 0) // 0 → default
	if col.binBps != 10 {
		t.Errorf("default binBps = %v, want 10", col.binBps)
	}
	if col.binsPerSide != 100 {
		t.Errorf("default binsPerSide = %v, want 100", col.binsPerSide)
	}
	_ = math.Pi // keep math import
}
