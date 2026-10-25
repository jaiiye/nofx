package bybit

import "testing"

// Bybit's totalEquity is authoritative; the wallet is derived so that
// equity == wallet + unrealizedPnL cannot drift. A venue wallet of zero must not
// be substituted with the equity — that is what used to make the PnL baseline
// reconciler book the whole unrealized PnL as an external deposit.
func TestBalanceFromWalletFieldsKeepsIdentity(t *testing.T) {
	got := balanceFromWalletFields(150, 0, 120, 30)

	if got.Equity != 150 {
		t.Fatalf("equity = %v, want 150", got.Equity)
	}
	if got.Wallet != 120 {
		t.Fatalf("wallet = %v, want 150-30", got.Wallet)
	}
	if got.UnrealizedPnL != 30 {
		t.Fatalf("unrealized = %v, want 30", got.UnrealizedPnL)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestBalanceFromWalletFieldsToleratesVenueWalletMismatch(t *testing.T) {
	// 1080 equity, 80 perp UPL -> derived wallet 1000. The venue says 1000 too.
	got := balanceFromWalletFields(1080, 1000, 900, 80)
	if got.Wallet != 1000 {
		t.Fatalf("wallet = %v, want 1000", got.Wallet)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}
