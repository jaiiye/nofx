package hyperliquid

import (
	"math"
	"regexp"
	"testing"
	"time"

	"github.com/sonirico/go-hyperliquid"
)

// A post-only entry must join the passive side of the book: a buy joins the best
// bid, a sell the best ask, so the order cannot cross and be rejected as ALO.
func TestPassiveEntryPrice(t *testing.T) {
	bids := [][]float64{{100.5, 2}, {100.0, 5}}
	asks := [][]float64{{100.7, 1}, {101.0, 4}}

	if price, ok := passiveEntryPrice(bids, asks, true); !ok || price != 100.5 {
		t.Fatalf("long passive price = %v ok=%v, want the best bid 100.5", price, ok)
	}
	if price, ok := passiveEntryPrice(bids, asks, false); !ok || price != 100.7 {
		t.Fatalf("short passive price = %v ok=%v, want the best ask 100.7", price, ok)
	}

	// An empty or zero-priced side means there is nothing to join.
	if _, ok := passiveEntryPrice(nil, nil, true); ok {
		t.Fatal("empty book must not produce a passive price")
	}
	if _, ok := passiveEntryPrice([][]float64{{0, 1}}, asks, true); ok {
		t.Fatal("zero-priced levels must be skipped")
	}
	if _, ok := passiveEntryPrice(bids, [][]float64{}, false); ok {
		t.Fatal("missing ask side must not produce a passive price")
	}
	if price, ok := passiveEntryPrice([][]float64{{}, {99.5, 1}}, asks, true); !ok || price != 99.5 {
		t.Fatalf("malformed level must be skipped, got %v ok=%v", price, ok)
	}
}

func TestMakerEntryDecision(t *testing.T) {
	cases := []struct {
		filled, requested float64
		timedOut          bool
		want              makerEntryAction
	}{
		{0, 10, false, makerEntryWait},
		{5, 10, false, makerEntryWait},
		{10, 10, false, makerEntryFilled},
		{9.995, 10, false, makerEntryFilled}, // venue size rounding
		{10, 10, true, makerEntryFilled},     // a full fill wins over the deadline
		{4, 10, true, makerEntryAcceptPartial},
		{0, 10, true, makerEntryFallback},
		{0, 0, true, makerEntryFallback},
	}
	for _, c := range cases {
		if got := makerEntryDecision(c.filled, c.requested, c.timedOut); got != c.want {
			t.Errorf("makerEntryDecision(%v, %v, %v) = %v, want %v", c.filled, c.requested, c.timedOut, got, c.want)
		}
	}
}

func TestALOOrderRequestIsPostOnly(t *testing.T) {
	order := aloOrderRequest("BTC", true, 0.5, 60000, "0xabc")
	if order.OrderType.Limit == nil {
		t.Fatal("post-only entry must be a limit order")
	}
	if order.OrderType.Limit.Tif != hyperliquid.TifAlo {
		t.Fatalf("tif = %v, want %v", order.OrderType.Limit.Tif, hyperliquid.TifAlo)
	}
	if order.ReduceOnly {
		t.Error("an entry must never be reduce-only")
	}
	if !order.IsBuy || order.Size != 0.5 || order.Price != 60000 || order.Coin != "BTC" {
		t.Fatalf("unexpected order fields: %+v", order)
	}
	if order.ClientOrderID == nil || *order.ClientOrderID != "0xabc" {
		t.Fatalf("client order id not carried: %+v", order.ClientOrderID)
	}

	// Without a cloid the order is still valid (no empty client id is sent).
	order = aloOrderRequest("BTC", false, 0.5, 60000, "")
	if order.ClientOrderID != nil {
		t.Error("empty cloid must not be set")
	}
	if order.IsBuy {
		t.Error("side must be carried through")
	}
}

func TestNewMakerCloidFormat(t *testing.T) {
	pattern := regexp.MustCompile(`^0x[0-9a-f]{32}$`)
	first := newMakerCloid()
	if !pattern.MatchString(first) {
		t.Fatalf("cloid %q does not match the venue format", first)
	}
	if second := newMakerCloid(); second == first {
		t.Fatal("cloids must be unique per entry")
	}
}

func TestParseVenueFloat(t *testing.T) {
	cases := []struct {
		raw  string
		def  float64
		want float64
	}{
		{"12.5", 0, 12.5},
		{" 3 ", 0, 3},
		{"", 7, 7},
		{"abc", 7, 7},
		{"Inf", 7, 7},
		{"NaN", 7, 7},
	}
	for _, c := range cases {
		if got := parseVenueFloat(c.raw, c.def); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("parseVenueFloat(%q, %v) = %v, want %v", c.raw, c.def, got, c.want)
		}
	}
}

func TestSideLabel(t *testing.T) {
	if sideLabel(true) != "LONG" || sideLabel(false) != "SHORT" {
		t.Fatalf("side labels = %q/%q", sideLabel(true), sideLabel(false))
	}
}

// The wait loop must terminate even when the venue never reports the order.
func TestWaitForMakerFillHonoursTimeoutWithoutIdentifiers(t *testing.T) {
	trader := &HyperliquidTrader{}
	// No oid and no cloid: every poll errors, and the deadline is what ends it.
	start := time.Now()
	filled, timedOut, err := trader.waitForMakerFill("BTC", 0, "", 1, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filled != 0 || !timedOut {
		t.Fatalf("filled=%v timedOut=%v, want 0/true", filled, timedOut)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("wait took %v, want it bounded by the timeout", elapsed)
	}
}

// A maker fill saves less than the fee difference alone suggests: the passive fill
// itself carries adverse selection. Reporting only the gross number over-credits
// the execution mode, so both are computed and reported.
func TestMakerSavingBps(t *testing.T) {
	// The account's measured rates: 4.32 bp taker, 1.44 bp maker.
	gross, netLow, netHigh := makerSavingBps(0.000432, 0.000144)
	if math.Abs(gross-2.88) > 1e-9 {
		t.Fatalf("gross saving = %v bp, want 2.88", gross)
	}
	if math.Abs(netLow-2.05) > 1e-9 || math.Abs(netHigh-2.50) > 1e-9 {
		t.Fatalf("net saving = %v-%v bp, want 2.05-2.50 (gross minus 0.38-0.83)", netLow, netHigh)
	}
	if netLow > netHigh {
		t.Fatalf("net range inverted: %v > %v", netLow, netHigh)
	}

	// When the maker rate is worse than taker, the "saving" is a cost and must
	// stay negative — the adverse-selection term must not turn it positive.
	gross, netLow, _ = makerSavingBps(0.0004, 0.0005)
	if gross > 0 || netLow > 0 {
		t.Fatalf("maker worse than taker: gross %v netLow %v, want negative", gross, netLow)
	}

	// Unknown rates contribute nothing.
	if gross, _, _ := makerSavingBps(0, 0); gross != 0 {
		t.Fatalf("zero rates: gross = %v, want 0", gross)
	}
}
