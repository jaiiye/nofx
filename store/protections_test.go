package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestProtectionConfigNormalizedDrawdownBasis(t *testing.T) {
	// An unset basis migrates to NAV: the raw curves cannot distinguish a
	// deposit from a loss.
	if got := (ProtectionConfig{}).Normalized().DrawdownBasis; got != DrawdownBasisNAV {
		t.Fatalf("empty basis = %q, want %q", got, DrawdownBasisNAV)
	}
	// Explicit legacy choices are preserved as an escape hatch.
	if got := (ProtectionConfig{DrawdownBasis: DrawdownBasisBalance}).Normalized().DrawdownBasis; got != DrawdownBasisBalance {
		t.Fatalf("balance basis = %q, want %q", got, DrawdownBasisBalance)
	}
	if got := (ProtectionConfig{DrawdownBasis: DrawdownBasisEquity}).Normalized().DrawdownBasis; got != DrawdownBasisEquity {
		t.Fatalf("equity basis = %q, want %q", got, DrawdownBasisEquity)
	}
	if got := (ProtectionConfig{DrawdownBasis: "nonsense"}).Normalized().DrawdownBasis; got != DrawdownBasisNAV {
		t.Fatalf("invalid basis = %q, want %q", got, DrawdownBasisNAV)
	}
	if got := DefaultProtectionConfig().DrawdownBasis; got != DrawdownBasisNAV {
		t.Fatalf("default basis = %q, want %q", got, DrawdownBasisNAV)
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

// Replays the incident that motivated NAV: a 26.76 USDT transfer in and out.
// Under the raw balance curve that pair reads as a 15.4% drawdown; the NAV index
// divides both points by the baseline in effect, so it reads as flat.
func TestPeakAndCurrentNAVIsCashFlowNeutral(t *testing.T) {
	st, err := NewWithConfig(DBConfig{Type: DBTypeSQLite, Path: filepath.Join(t.TempDir(), "nav.db")})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	base := time.Now().UTC().Add(-4 * time.Hour)
	snaps := []struct {
		at       time.Time
		balance  float64
		baseline float64
	}{
		{base, 146.845953, 160.860362},                       // before the transfer
		{base.Add(30 * time.Minute), 173.607715, 187.622124}, // deposit detected
		{base.Add(60 * time.Minute), 174.094941, 187.622124}, // "peak" of the raw curve
		{base.Add(90 * time.Minute), 147.331233, 160.457954}, // withdrawal detected
	}
	for _, s := range snaps {
		if err := st.Equity().Save(&EquitySnapshot{
			TraderID:       "t1",
			Timestamp:      s.at,
			TotalEquity:    s.balance,
			Balance:        s.balance,
			InitialBalance: s.baseline,
		}); err != nil {
			t.Fatalf("save snapshot: %v", err)
		}
	}

	since := base.Add(-time.Minute)

	// Raw balance: the transfer pair dominates and looks like a big drawdown.
	rawPeak, rawCur, ok, err := st.Equity().PeakAndCurrentBalance("t1", since)
	if err != nil || !ok {
		t.Fatalf("balance query ok=%v err=%v", ok, err)
	}
	rawDD := (rawPeak - rawCur) / rawPeak * 100
	if rawDD < 15 {
		t.Fatalf("raw drawdown = %.2f%%, expected the phantom ~15%%", rawDD)
	}

	// NAV: the transfer pair cancels out, leaving only the real PnL move (~1%:
	// the same absolute loss against a smaller capital base).
	navPeak, navCur, ok, err := st.Equity().PeakAndCurrentNAV("t1", since)
	if err != nil || !ok {
		t.Fatalf("nav query ok=%v err=%v", ok, err)
	}
	navDD := (navPeak - navCur) / navPeak * 100
	if navDD > 2 {
		t.Fatalf("nav drawdown = %.2f%%, want under 2%%", navDD)
	}

	maxDD, err := st.Equity().MaxDrawdownNAVPct("t1", time.Time{})
	if err != nil {
		t.Fatalf("MaxDrawdownNAVPct: %v", err)
	}
	if maxDD > 2 {
		t.Fatalf("max nav drawdown = %.2f%%, want under 2%%", maxDD)
	}
}
