package hyperliquid

import (
	"testing"
)

// The venue returns numbers as strings, sometimes with thousands separators;
// a mis-parse would silently zero the AI's OI/funding context.
func TestParseVenueNumber(t *testing.T) {
	cases := map[string]float64{
		"94098.08":    94098.08,
		"1,234,567.5": 1234567.5,
		"-0.00001234": -0.00001234,
		"0":           0,
		"":            0,
		" 12.5 ":      12.5,
		"garbage":     0,
	}
	for in, want := range cases {
		if got := ParseVenueNumber(in); got != want {
			t.Errorf("ParseVenueNumber(%q) = %v, want %v", in, got, want)
		}
	}
}
