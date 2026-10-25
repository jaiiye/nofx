package main

import (
	"testing"

	"nofx/market"
	"nofx/store"
)

func TestResolveReplayVenueUsesRecordedVenue(t *testing.T) {
	records := []*store.DecisionRecord{
		{Venue: "hyperliquid"},
		{Venue: "hyperliquid"},
	}
	venue, err := resolveReplayVenue(records, "auto")
	if err != nil {
		t.Fatalf("auto venue: %v", err)
	}
	if venue != market.VenueHyperliquid {
		t.Fatalf("venue = %s, want hyperliquid", venue)
	}
}

func TestResolveReplayVenueExplicitOverride(t *testing.T) {
	records := []*store.DecisionRecord{{Venue: "hyperliquid"}}
	venue, err := resolveReplayVenue(records, "binance")
	if err != nil {
		t.Fatalf("explicit venue: %v", err)
	}
	if venue != market.VenueBinance {
		t.Fatalf("venue = %s, want binance", venue)
	}
}

func TestResolveReplayVenueRejectsMixedLog(t *testing.T) {
	records := []*store.DecisionRecord{{Venue: "hyperliquid"}, {Venue: "binance"}}
	if _, err := resolveReplayVenue(records, "auto"); err == nil {
		t.Fatal("a log mixing venues must require an explicit --venue")
	}
}

func TestResolveReplayVenueFallsBackForOldRecords(t *testing.T) {
	venue, err := resolveReplayVenue([]*store.DecisionRecord{{Venue: ""}}, "auto")
	if err != nil {
		t.Fatalf("fallback: %v", err)
	}
	if venue != market.VenueBinance {
		t.Fatalf("venue = %s, want binance fallback", venue)
	}
}
