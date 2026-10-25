package types

import (
	"math"
	"testing"
)

func TestBalanceFromEquityKeepsIdentity(t *testing.T) {
	b := BalanceFromEquity("okx", 1100, 100, 900)

	if b.Wallet != 1000 {
		t.Fatalf("wallet = %v, want 1000", b.Wallet)
	}
	if err := b.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestBalanceFromWalletKeepsIdentity(t *testing.T) {
	b := BalanceFromWallet("binance", 1000, -25.5, 900)

	if b.Equity != 974.5 {
		t.Fatalf("equity = %v, want 974.5", b.Equity)
	}
	if err := b.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateRejectsBrokenIdentity(t *testing.T) {
	b := AccountBalance{Venue: "manual", Equity: 100, Wallet: 50, UnrealizedPnL: 10}
	if err := b.Validate(); err == nil {
		t.Fatal("expected a broken-identity error")
	}
}

func TestValidateToleratesRounding(t *testing.T) {
	b := AccountBalance{Venue: "manual", Equity: 146.934675, Wallet: 146.841315, UnrealizedPnL: 0.09336}
	if err := b.Validate(); err != nil {
		t.Fatalf("rounding must be tolerated: %v", err)
	}
}

func TestToMapEmitsEveryAlias(t *testing.T) {
	b := BalanceFromEquity("kucoin", 200, 20, 150)
	b.MarginUsed = 30

	m := b.ToMap()
	for _, key := range []string{
		"totalEquity", "total_equity",
		"totalWalletBalance", "wallet_balance",
		"totalUnrealizedProfit", "unrealized_profit",
		"availableBalance", "balance",
	} {
		if _, ok := m[key]; !ok {
			t.Fatalf("ToMap is missing %q", key)
		}
	}
	if m["totalEquity"].(float64) != m["total_equity"].(float64) {
		t.Fatal("equity aliases disagree")
	}
	if m["totalWalletBalance"].(float64) != 180 {
		t.Fatalf("wallet = %v, want 180", m["totalWalletBalance"])
	}
}

func TestToMapXyzPoolOnlyWhenSeparate(t *testing.T) {
	b := BalanceFromEquity("hyperliquid", 150, 0, 150)

	if _, ok := b.ToMap()["xyzDexBalance"]; ok {
		t.Fatal("xyzDexBalance must be absent when the dex is not a separate pool")
	}

	b.XyzSeparate = true
	b.XyzEquity = 26.7664
	b.XyzAvailable = 0
	if _, ok := b.ToMap()["xyzDexBalance"]; !ok {
		t.Fatal("xyzDexBalance must be present for a separate pool")
	}
}

func TestIncompleteFlagsMap(t *testing.T) {
	b := BalanceFromWallet("aster", 500, 0, 100).Incomplete()
	if b.ToMap()["balance_incomplete"] != true {
		t.Fatal("incomplete balance must be flagged in the map")
	}
	if math.Abs(b.Wallet-500) > 1e-9 {
		t.Fatal("Incomplete must not alter the figures")
	}
}
