package hyperliquid

import (
	"errors"
	"strings"
	"testing"
)

// The raw venue rejection ("Insufficient margin to place order") is the exact
// message from the reported production failure on xyz:SILVER. On the xyz dex
// it means the dex's own USDC pool needs a deposit, which the bare error does
// not convey — so it must carry that guidance through.
func TestWrapXyzInsufficientMargin(t *testing.T) {
	raw := errors.New("xyz dex order error (coin=xyz:SILVER, assetIndex=110026, size=1.0500, price=57.4500): Insufficient margin to place order. asset=110026")
	got := wrapXyzInsufficientMargin(raw, "xyz:SILVER")
	if got == nil {
		t.Fatal("expected a wrapped error")
	}
	msg := got.Error()
	for _, want := range []string{
		"Insufficient margin",      // original venue detail preserved
		"single USDC balance",      // unified/portfolio accounts
		"separate xyz dex balance", // manual/standard accounts
		"xyz:SILVER",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("wrapped message missing %q:\n%s", want, msg)
		}
	}

	// Unrelated failures pass through untouched.
	other := errors.New("some other venue failure")
	if wrapXyzInsufficientMargin(other, "xyz:SILVER").Error() != other.Error() {
		t.Fatal("non-margin errors must pass through unchanged")
	}
	if wrapXyzInsufficientMargin(nil, "xyz:SILVER") != nil {
		t.Fatal("nil error must stay nil")
	}

	// Case-insensitive match: the venue has varied the capitalisation before.
	lower := errors.New("order rejected: insufficient margin")
	if !strings.Contains(wrapXyzInsufficientMargin(lower, "xyz:GOLD").Error(), "single USDC balance") {
		t.Fatal("lowercase 'insufficient margin' must also be wrapped")
	}
}
