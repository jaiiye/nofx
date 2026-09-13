package api

import (
	"fmt"
	"strings"
	"testing"
)

func TestMaskSensitiveString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "Short string (8 characters or less)",
			input:    "short",
			expected: "****",
		},
		{
			name:     "Normal API key",
			input:    "sk-1234567890abcdefghijklmnopqrstuvwxyz",
			expected: "sk-1****wxyz",
		},
		{
			name:     "Normal private key",
			input:    "0x1234567890abcdef1234567890abcdef12345678",
			expected: "0x12****5678",
		},
		{
			name:     "Exactly 9 characters",
			input:    "123456789",
			expected: "1234****6789",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MaskSensitiveString(tt.input)
			if result != tt.expected {
				t.Errorf("MaskSensitiveString(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSanitizeModelConfigForLog(t *testing.T) {
	models := map[string]ModelConfigUpdate{
		"deepseek": {
			Enabled:         true,
			APIKey:          "sk-1234567890abcdefghijklmnopqrstuvwxyz",
			CustomAPIURL:    "https://api.deepseek.com",
			CustomModelName: "deepseek-chat",
		},
	}

	result := SanitizeModelConfigForLog(models)

	deepseekConfig, ok := result["deepseek"].(map[string]interface{})
	if !ok {
		t.Fatal("deepseek config not found or wrong type")
	}

	if deepseekConfig["enabled"] != true {
		t.Errorf("expected enabled=true, got %v", deepseekConfig["enabled"])
	}

	maskedKey, ok := deepseekConfig["api_key"].(string)
	if !ok {
		t.Fatal("api_key not found or wrong type")
	}

	if maskedKey != "sk-1****wxyz" {
		t.Errorf("expected masked api_key='sk-1****wxyz', got %q", maskedKey)
	}

	if deepseekConfig["custom_api_url"] != "https://api.deepseek.com" {
		t.Errorf("custom_api_url should not be masked")
	}
}

func TestSanitizeExchangeConfigForLog(t *testing.T) {
	exchanges := map[string]ExchangeConfigUpdate{
		"hyperliquid": {
			Enabled:               true,
			APIKey:                "hyper_api_key_1234567890abcdef",
			SecretKey:             "hyper_secret_key_1234567890abcdef",
			Passphrase:            "hyper_passphrase_supersecret_value",
			HyperliquidWalletAddr: "0x1234567890abcdef1234567890abcdef12345678",
			Testnet:               false,
		},
	}

	result := SanitizeExchangeConfigForLog(exchanges)

	hlConfig, ok := result["hyperliquid"].(map[string]interface{})
	if !ok {
		t.Fatal("hyperliquid config not found or wrong type")
	}

	maskedAPIKey, ok := hlConfig["api_key"].(string)
	if !ok {
		t.Fatal("hyperliquid api_key not found or wrong type")
	}

	if maskedAPIKey != "hype****cdef" {
		t.Errorf("expected masked api_key='hype****cdef', got %q", maskedAPIKey)
	}

	maskedSecretKey, ok := hlConfig["secret_key"].(string)
	if !ok {
		t.Fatal("hyperliquid secret_key not found or wrong type")
	}

	if maskedSecretKey != "hype****cdef" {
		t.Errorf("expected masked secret_key='hype****cdef', got %q", maskedSecretKey)
	}

	// Check passphrase is masked (regression: previously not covered)
	maskedPassphrase, ok := hlConfig["passphrase"].(string)
	if !ok {
		t.Fatal("hyperliquid passphrase not found or wrong type")
	}
	if maskedPassphrase != "hype****alue" {
		t.Errorf("expected masked passphrase='hype****alue', got %q", maskedPassphrase)
	}

	walletAddr, ok := hlConfig["hyperliquid_wallet_addr"].(string)
	if !ok {
		t.Fatal("hyperliquid_wallet_addr not found or wrong type")
	}

	// Wallet address should not be masked
	if walletAddr != "0x1234567890abcdef1234567890abcdef12345678" {
		t.Errorf("wallet address should not be masked, got %q", walletAddr)
	}
}

// TestSanitizeExchangeConfigForLog_NoPlaintextSecrets renders the sanitized log
// output exactly as the handler does (`%+v`) and asserts that no plaintext
// secret — including the passphrase that was historically not redacted —
// survives into the log line.
func TestSanitizeExchangeConfigForLog_NoPlaintextSecrets(t *testing.T) {
	secrets := map[string]string{
		"api_key":    "hyper_api_key_1234567890abcdef",
		"secret_key": "hyper_secret_key_1234567890abcdef",
		"passphrase": "hyper_passphrase_supersecret_value",
	}

	exchanges := map[string]ExchangeConfigUpdate{
		"hyperliquid": {
			Enabled:    true,
			APIKey:     secrets["api_key"],
			SecretKey:  secrets["secret_key"],
			Passphrase: secrets["passphrase"],
		},
	}

	rendered := fmt.Sprintf("%+v", SanitizeExchangeConfigForLog(exchanges))

	for field, secret := range secrets {
		if strings.Contains(rendered, secret) {
			t.Errorf("sanitized log leaked plaintext %s: %q present in %q", field, secret, rendered)
		}
	}
}

func TestMaskEmail(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Empty email",
			input:    "",
			expected: "",
		},
		{
			name:     "Invalid format",
			input:    "notanemail",
			expected: "****",
		},
		{
			name:     "Normal email",
			input:    "user@example.com",
			expected: "us****@example.com",
		},
		{
			name:     "Short username",
			input:    "a@example.com",
			expected: "**@example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MaskEmail(tt.input)
			if result != tt.expected {
				t.Errorf("MaskEmail(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
