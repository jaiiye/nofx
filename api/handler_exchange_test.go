package api

import (
	"testing"

	"nofx/crypto"
	"nofx/store"
)

func TestSafeExchangeConfigFromStoreIncludesCredentialPresenceFlags(t *testing.T) {
	cfg := &store.Exchange{
		ID:                    "ex-1",
		ExchangeType:          "hyperliquid",
		AccountName:           "HL Main",
		Name:                  "HL Main",
		Type:                  "dex",
		Enabled:               true,
		APIKey:                crypto.EncryptedString("api-test-123"),
		SecretKey:             crypto.EncryptedString("secret-test-123"),
		Passphrase:            crypto.EncryptedString("passphrase-test-123"),
		HyperliquidWalletAddr: "0xabcdef",
	}

	safe := safeExchangeConfigFromStore(cfg)
	if !safe.HasAPIKey {
		t.Fatalf("expected has_api_key to be true")
	}
	if !safe.HasSecretKey {
		t.Fatalf("expected has_secret_key to be true")
	}
	if !safe.HasPassphrase {
		t.Fatalf("expected has_passphrase to be true")
	}
	if safe.HyperliquidWalletAddr != "0xabcdef" {
		t.Fatalf("expected hyperliquid wallet addr to be preserved, got %q", safe.HyperliquidWalletAddr)
	}
}
