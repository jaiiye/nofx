package kernel

import (
	"testing"

	"nofx/store"
)

func TestClampHyperRankLimit(t *testing.T) {
	// An unset count resolves to the product default (the volume board), not to
	// a hardcoded smaller number: the two must not drift apart.
	if got := clampHyperRankLimit(0); got != store.DefaultHyperRankLimit {
		t.Fatalf("clamp 0 = %d, want %d", got, store.DefaultHyperRankLimit)
	}
	if got := clampHyperRankLimit(99); got != 10 {
		t.Fatalf("clamp 99 = %d, want 10", got)
	}
	if got := clampHyperRankLimit(3); got != 3 {
		t.Fatalf("clamp 3 = %d, want 3", got)
	}
}
