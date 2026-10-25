package store

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A stored config may still carry the retired "drawdown_basis" key; it must keep
// loading, and the value is simply ignored because NAV is the only curve now.
func TestProtectionConfigIgnoresLegacyDrawdownBasis(t *testing.T) {
	var cfg ProtectionConfig
	if err := json.Unmarshal([]byte(`{"max_drawdown_pct":10,"drawdown_basis":"balance"}`), &cfg); err != nil {
		t.Fatalf("unmarshal legacy config: %v", err)
	}
	if cfg.MaxDrawdownPct != 10 {
		t.Fatalf("max drawdown = %v, want 10", cfg.MaxDrawdownPct)
	}

	// Round-tripping drops the key: there is nothing left to configure.
	out, err := json.Marshal(cfg.Normalized())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), "drawdown_basis") {
		t.Fatalf("retired key is still written: %s", out)
	}
}

// NAV is mark-to-market: a floating loss has to count, which a wallet-only curve
// would miss until the position closed.
func TestPeakAndCurrentNAVIncludesUnrealized(t *testing.T) {
	st, err := NewWithConfig(DBConfig{Type: DBTypeSQLite, Path: filepath.Join(t.TempDir(), "nav-equity.db")})
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
		{base, 1000, 1000},                       // flat
		{base.Add(30 * time.Minute), 1000, 1100}, // wallet up but 100 of floating loss
	}
	for _, s := range snaps {
		if err := st.Equity().Save(&EquitySnapshot{
			TraderID:       "t1",
			Timestamp:      s.at,
			TotalEquity:    s.equity,
			Balance:        s.balance,
			InitialBalance: 1000,
		}); err != nil {
			t.Fatalf("save snapshot: %v", err)
		}
	}

	peak, cur, ok, err := st.Equity().PeakAndCurrentNAV("t1", base.Add(-time.Minute))
	if err != nil || !ok {
		t.Fatalf("nav query ok=%v err=%v", ok, err)
	}
	if peak != 1 || cur != 1 {
		t.Fatalf("nav = %v/%v, want 1/1 (equity based, not the 1.1 wallet)", peak, cur)
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

	// The raw curve the breaker used to watch: the transfer pair dominates and
	// looks like a big drawdown.
	rawPeak, rawCur := 0.0, 0.0
	for _, s := range snaps {
		if s.balance > rawPeak {
			rawPeak = s.balance
		}
		rawCur = s.balance
	}
	if rawDD := (rawPeak - rawCur) / rawPeak * 100; rawDD < 15 {
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
