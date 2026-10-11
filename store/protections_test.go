package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestProtectionConfigNormalizedDrawdownBasis(t *testing.T) {
	if got := (ProtectionConfig{}).Normalized().DrawdownBasis; got != DrawdownBasisEquity {
		t.Fatalf("empty basis = %q, want %q", got, DrawdownBasisEquity)
	}
	if got := (ProtectionConfig{DrawdownBasis: DrawdownBasisBalance}).Normalized().DrawdownBasis; got != DrawdownBasisBalance {
		t.Fatalf("balance basis = %q, want %q", got, DrawdownBasisBalance)
	}
	if got := (ProtectionConfig{DrawdownBasis: "nonsense"}).Normalized().DrawdownBasis; got != DrawdownBasisEquity {
		t.Fatalf("invalid basis = %q, want %q", got, DrawdownBasisEquity)
	}
}

func TestPeakAndCurrentEquityAndBalance(t *testing.T) {
	st, err := NewWithConfig(DBConfig{Type: DBTypeSQLite, Path: filepath.Join(t.TempDir(), "protections.db")})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	base := time.Now().UTC().Add(-2 * time.Hour)
	snaps := []struct {
		at      time.Time
		equity  float64
		balance float64
	}{
		{base, 1000, 1000},
		{base.Add(30 * time.Minute), 1200, 1000}, // equity peaks, balance flat
		{base.Add(60 * time.Minute), 900, 950},
	}
	for _, s := range snaps {
		if err := st.Equity().Save(&EquitySnapshot{
			TraderID:    "t1",
			Timestamp:   s.at,
			TotalEquity: s.equity,
			Balance:     s.balance,
		}); err != nil {
			t.Fatalf("save snapshot: %v", err)
		}
	}

	since := base.Add(-time.Minute)

	peak, cur, ok, err := st.Equity().PeakAndCurrentEquity("t1", since)
	if err != nil || !ok {
		t.Fatalf("equity query ok=%v err=%v", ok, err)
	}
	if peak != 1200 || cur != 900 {
		t.Fatalf("equity peak/current = %v/%v, want 1200/900", peak, cur)
	}

	bPeak, bCur, ok, err := st.Equity().PeakAndCurrentBalance("t1", since)
	if err != nil || !ok {
		t.Fatalf("balance query ok=%v err=%v", ok, err)
	}
	if bPeak != 1000 || bCur != 950 {
		t.Fatalf("balance peak/current = %v/%v, want 1000/950", bPeak, bCur)
	}

	// Window with no snapshots must report ok=false.
	if _, _, ok, err := st.Equity().PeakAndCurrentEquity("t1", time.Now().UTC().Add(time.Hour)); err != nil || ok {
		t.Fatalf("empty window ok=%v err=%v, want ok=false", ok, err)
	}
}
