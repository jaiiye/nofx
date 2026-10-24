package market

import (
	"testing"
	"time"
)

func TestNormalizeFunding(t *testing.T) {
	cases := []struct {
		name        string
		rate        float64
		intervalH   float64
		wantPct     float64
		wantAssumed bool
	}{
		{"binance 8h", 0.0008, 8, 0.01, false},
		{"hyperliquid hourly", 0.0001, 1, 0.01, false},
		{"unknown interval defaults to 8h and says so", 0.0008, 0, 0.01, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, assumed := NormalizeFunding(tc.rate, tc.intervalH)
			if got != tc.wantPct || assumed != tc.wantAssumed {
				t.Fatalf("NormalizeFunding(%v, %v) = %v, %v; want %v, %v",
					tc.rate, tc.intervalH, got, assumed, tc.wantPct, tc.wantAssumed)
			}
		})
	}
}

func TestBuildCrossVenueKeepsVenuesSeparate(t *testing.T) {
	now := time.Now().UnixMilli()
	quotes := []VenueQuote{
		{Symbol: "BTCUSDT", Venue: "hyperliquid", VenueSymbol: "BTC", MarkPrice: 63000, OIBase: 1000, OIUSD: 63000000, FundingRate: 0.00012, FundingIntervalH: 1, SourceOK: true, TimestampMs: now},
		{Symbol: "BTCUSDT", Venue: "binance", VenueSymbol: "BTCUSDT", MarkPrice: 63010, OIBase: 5000, OIUSD: 315050000, FundingRate: 0.00008, FundingIntervalH: 8, SourceOK: true, TimestampMs: now},
		{Symbol: "BTCUSDT", Venue: "bybit", VenueSymbol: "BTCUSDT", MarkPrice: 63005, OIBase: 2000, OIUSD: 126010000, FundingRate: 0.00016, FundingIntervalH: 8, SourceOK: true, TimestampMs: now},
	}

	got := BuildCrossVenue("BTCUSDT", quotes, now)
	if got == nil {
		t.Fatal("expected a cross-venue reading")
	}
	if len(got.Venues) != 3 {
		t.Fatalf("venues = %d, want 3", len(got.Venues))
	}
	if got.Venues[0].Venue != "binance" || got.Venues[2].Venue != "hyperliquid" {
		t.Fatalf("venue order = %v, want binance, bybit, hyperliquid", []string{got.Venues[0].Venue, got.Venues[1].Venue, got.Venues[2].Venue})
	}

	total := 63000000.0 + 315050000 + 126010000
	if got.OITotalUSD != total {
		t.Fatalf("total = %v, want %v", got.OITotalUSD, total)
	}
	shareSum := 0.0
	for _, share := range got.OISharePct {
		shareSum += share
	}
	if shareSum < 99.99 || shareSum > 100.01 {
		t.Fatalf("shares sum to %v, want 100", shareSum)
	}

	// HL 0.00012/h = 0.012%/h; Binance 0.00008/8h = 0.001%/h.
	if got.Venues[2].Funding1hPct != 0.012 || got.Venues[0].Funding1hPct != 0.001 {
		t.Fatalf("funding normalization: hl=%v binance=%v", got.Venues[2].Funding1hPct, got.Venues[0].Funding1hPct)
	}
	if !got.FundingSpreadOK || got.FundingSpreadPair != "hyperliquid/binance" {
		t.Fatalf("spread = %v %q ok=%v", got.FundingSpread1hPct, got.FundingSpreadPair, got.FundingSpreadOK)
	}
	if diff := got.FundingSpread1hPct - (0.012 - 0.001); diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("spread = %v, want %v", got.FundingSpread1hPct, 0.011)
	}
}

func TestBuildCrossVenueRequiresTwoVenues(t *testing.T) {
	now := time.Now().UnixMilli()
	got := BuildCrossVenue("xyz:NVDA", []VenueQuote{
		{Symbol: "xyz:NVDA", Venue: "hyperliquid", MarkPrice: 180, OIUSD: 1e7, FundingRate: 0.0001, FundingIntervalH: 1, SourceOK: true, TimestampMs: now},
	}, now)
	if got != nil {
		t.Fatalf("a single venue is not a cross-venue reading: %+v", got)
	}
}

func TestBuildCrossVenueDropsDegradedQuotes(t *testing.T) {
	now := time.Now().UnixMilli()
	got := BuildCrossVenue("BTCUSDT", []VenueQuote{
		{Venue: "hyperliquid", MarkPrice: 63000, OIUSD: 1e8, FundingRate: 0.0001, FundingIntervalH: 1, SourceOK: true, TimestampMs: now},
		{Venue: "binance", MarkPrice: 63000, OIUSD: 1e8, SourceOK: false, TimestampMs: now},
		{Venue: "bybit", MarkPrice: 63000, OIUSD: 1e8, FundingRate: 0.0001, FundingIntervalH: 8, SourceOK: true, TimestampMs: now},
	}, now)
	if got == nil {
		t.Fatal("expected the two healthy venues")
	}
	if len(got.Venues) != 2 {
		t.Fatalf("venues = %d, want 2 (degraded dropped)", len(got.Venues))
	}
}

func TestBuildCrossVenueCrowding(t *testing.T) {
	now := time.Now().UnixMilli()
	// Both venues at 0.01%/h, comfortably above the 0.005 threshold.
	quotes := []VenueQuote{
		{Venue: "hyperliquid", MarkPrice: 100, OIUSD: 1e8, FundingRate: 0.0001, FundingIntervalH: 1, SourceOK: true, TimestampMs: now},
		{Venue: "binance", MarkPrice: 100, OIUSD: 1e8, FundingRate: 0.0008, FundingIntervalH: 8, SourceOK: true, TimestampMs: now},
	}
	got := BuildCrossVenue("BTCUSDT", quotes, now)
	if got == nil || got.CrowdedSide != "long" {
		t.Fatalf("crowded = %+v, want long", got)
	}

	for i := range quotes {
		quotes[i].FundingRate = -quotes[i].FundingRate
	}
	got = BuildCrossVenue("BTCUSDT", quotes, now)
	if got == nil || got.CrowdedSide != "short" {
		t.Fatalf("crowded = %+v, want short", got)
	}
}
