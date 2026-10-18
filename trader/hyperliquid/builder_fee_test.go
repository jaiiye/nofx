package hyperliquid

import (
	"sync"
	"testing"
)

// resetBuilderForTest clears the once-cached resolution so one test can vary the
// environment without leaking into the next.
func resetBuilderForTest() {
	builderOnce = sync.Once{}
	builderVal = builderSettings{}
}

// A builder fee is charged per fill on top of the venue fee. On this deployment it
// measured larger than the venue's own taker fee, so the default must be "no
// builder" — an operator opts in explicitly.
func TestBuilderRoutingDefaultsToDisabled(t *testing.T) {
	t.Setenv(EnvBuilderAddress, "")
	t.Setenv(EnvBuilderFeeBps, "")
	resetBuilderForTest()

	if BuilderFeeEnabled() {
		t.Fatal("builder routing must be disabled by default")
	}
	if BuilderFeeBps() != 0 {
		t.Fatalf("default fee = %d bp, want 0", BuilderFeeBps())
	}
	if info := builderInfo(); info != nil {
		t.Fatalf("default builderInfo = %+v, want nil", info)
	}
}

func TestBuilderRoutingEnabledFromEnv(t *testing.T) {
	t.Setenv(EnvBuilderAddress, "0xabc")
	t.Setenv(EnvBuilderFeeBps, "5")
	resetBuilderForTest()

	if !BuilderFeeEnabled() || BuilderFeeBps() != 5 {
		t.Fatalf("enabled = %v fee = %d bp, want true/5", BuilderFeeEnabled(), BuilderFeeBps())
	}
	info := builderInfo()
	if info == nil || info.Builder != "0xabc" {
		t.Fatalf("builderInfo = %+v, want address 0xabc", info)
	}
	// BuilderInfo.Fee is in tenths of a basis point: 5 bp = 50.
	if info.Fee != 50 {
		t.Fatalf("BuilderInfo.Fee = %d, want 50 for 5 bp", info.Fee)
	}
}

func TestBuilderRoutingRequiresAddressAndFee(t *testing.T) {
	// A fee without an address charges nothing.
	t.Setenv(EnvBuilderAddress, "")
	t.Setenv(EnvBuilderFeeBps, "5")
	resetBuilderForTest()
	if BuilderFeeEnabled() || builderInfo() != nil {
		t.Fatal("a fee without a builder address must not attach a builder")
	}

	// An address with a zero (or unparsable) fee is also a no-op.
	t.Setenv(EnvBuilderAddress, "0xabc")
	t.Setenv(EnvBuilderFeeBps, "0")
	resetBuilderForTest()
	if BuilderFeeEnabled() {
		t.Fatal("a zero fee must not attach a builder")
	}
	t.Setenv(EnvBuilderFeeBps, "not-a-number")
	resetBuilderForTest()
	if BuilderFeeEnabled() {
		t.Fatal("an unparsable fee must fall back to disabled")
	}
}

func TestBuilderRoutingClampsToVenueCap(t *testing.T) {
	t.Setenv(EnvBuilderAddress, "0xabc")
	t.Setenv(EnvBuilderFeeBps, "250") // 2.5%, above the venue's 0.1% cap
	resetBuilderForTest()

	if got := BuilderFeeBps(); got != maxBuilderFeeBps {
		t.Fatalf("fee = %d bp, want clamped to %d", got, maxBuilderFeeBps)
	}
	if info := builderInfo(); info == nil || info.Fee != maxBuilderFeeBps*10 {
		t.Fatalf("BuilderInfo = %+v, want Fee %d (tenths of bp)", info, maxBuilderFeeBps*10)
	}
}
