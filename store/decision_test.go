package store

import (
	"testing"
	"time"
)

func TestDecisionVenueRoundTrip(t *testing.T) {
	st, err := New(t.TempDir() + "/nofx.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)

	record := &DecisionRecord{
		TraderID:    "trader-1",
		CycleNumber: 7,
		Venue:       "hyperliquid",
		Timestamp:   now,
		Decisions:   []DecisionAction{},
	}
	if err := st.Decision().LogDecision(record); err != nil {
		t.Fatalf("log decision: %v", err)
	}

	records, err := st.Decision().GetRecordsByRange("trader-1", now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("read records: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0].Venue != "hyperliquid" {
		t.Fatalf("venue = %q, want hyperliquid", records[0].Venue)
	}
}
