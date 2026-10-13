package crypto

import (
	"database/sql/driver"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// newTestService builds a CryptoService from generated keys via the same env
// path production uses.
func newTestService(t *testing.T) *CryptoService {
	t.Helper()
	privPEM, _, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	dataKey, err := GenerateDataKey()
	if err != nil {
		t.Fatalf("GenerateDataKey: %v", err)
	}
	t.Setenv(EnvRSAPrivateKey, privPEM)
	t.Setenv(EnvDataEncryptionKey, dataKey)

	cs, err := NewCryptoService()
	if err != nil {
		t.Fatalf("NewCryptoService: %v", err)
	}
	return cs
}

func TestStorageRoundTrip(t *testing.T) {
	cs := newTestService(t)

	const secret = "sk-test-abc123SECRET"
	enc, err := cs.EncryptForStorage(secret)
	if err != nil {
		t.Fatalf("EncryptForStorage: %v", err)
	}
	if !cs.IsEncryptedStorageValue(enc) {
		t.Fatalf("encrypted value lacks storage prefix: %q", enc)
	}
	if strings.Contains(enc, secret) {
		t.Fatalf("ciphertext leaks plaintext: %q", enc)
	}

	got, err := cs.DecryptFromStorage(enc)
	if err != nil {
		t.Fatalf("DecryptFromStorage: %v", err)
	}
	if got != secret {
		t.Fatalf("round trip mismatch: got %q", got)
	}

	// Encrypting an already-encrypted value is a no-op (idempotent save path).
	enc2, err := cs.EncryptForStorage(enc)
	if err != nil {
		t.Fatalf("EncryptForStorage(idempotent): %v", err)
	}
	if enc2 != enc {
		t.Fatalf("double encrypt changed the value")
	}

	// Empty values pass through without encryption.
	if enc, err := cs.EncryptForStorage(""); err != nil || enc != "" {
		t.Fatalf("empty plaintext: enc=%q err=%v", enc, err)
	}
}

func TestStorageAADBinding(t *testing.T) {
	cs := newTestService(t)

	enc, err := cs.EncryptForStorage("secret", "user1", "exchange1")
	if err != nil {
		t.Fatalf("EncryptForStorage: %v", err)
	}

	if got, err := cs.DecryptFromStorage(enc, "user1", "exchange1"); err != nil || got != "secret" {
		t.Fatalf("decrypt with matching aad: got=%q err=%v", got, err)
	}

	// A different AAD (different user) must not decrypt the value.
	if _, err := cs.DecryptFromStorage(enc, "user2", "exchange1"); err == nil {
		t.Fatalf("decrypt with wrong aad succeeded")
	}
}

func TestStorageWrongKeyFails(t *testing.T) {
	cs := newTestService(t)
	enc, err := cs.EncryptForStorage("secret")
	if err != nil {
		t.Fatalf("EncryptForStorage: %v", err)
	}

	// A service with a different data key must fail loudly, not return data.
	t.Setenv(EnvDataEncryptionKey, mustGenerateDataKey(t))
	other, err := NewCryptoService()
	if err != nil {
		t.Fatalf("NewCryptoService(other): %v", err)
	}
	if _, err := other.DecryptFromStorage(enc); err == nil {
		t.Fatalf("decrypt with wrong key succeeded")
	}
}

func mustGenerateDataKey(t *testing.T) string {
	t.Helper()
	k, err := GenerateDataKey()
	if err != nil {
		t.Fatalf("GenerateDataKey: %v", err)
	}
	return k
}

func TestEncryptedStringRoundTrip(t *testing.T) {
	cs := newTestService(t)
	SetGlobalCryptoService(cs)
	t.Cleanup(func() { SetGlobalCryptoService(nil) })

	es := EncryptedString("my-api-key")
	v, err := es.Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	stored, ok := v.(string)
	if !ok {
		t.Fatalf("Value returned %T, want string", v)
	}
	if !cs.IsEncryptedStorageValue(stored) {
		t.Fatalf("Value must encrypt before storage, got %q", stored)
	}

	var back EncryptedString
	if err := back.Scan(stored); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if back != es {
		t.Fatalf("round trip mismatch: got %q", back)
	}

	// NULL columns scan as empty.
	var nullVal EncryptedString
	if err := nullVal.Scan(nil); err != nil || nullVal != "" {
		t.Fatalf("Scan(nil): %q err=%v", nullVal, err)
	}
	// Empty string stays empty (Value short-circuits).
	emptyVal, err := EncryptedString("").Value()
	if err != nil || emptyVal != "" {
		t.Fatalf("empty Value: %v err=%v", emptyVal, err)
	}
}

func TestEncryptedStringLegacyPlaintextPassthrough(t *testing.T) {
	cs := newTestService(t)
	SetGlobalCryptoService(cs)
	t.Cleanup(func() { SetGlobalCryptoService(nil) })

	// Values stored before the column was encrypted read back as-is and are
	// re-encrypted on the next Value() call.
	var legacy EncryptedString
	if err := legacy.Scan("plaintext-legacy-token"); err != nil {
		t.Fatalf("Scan(plaintext): %v", err)
	}
	if legacy != "plaintext-legacy-token" {
		t.Fatalf("legacy plaintext must pass through, got %q", legacy)
	}
	v, err := legacy.Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	if !cs.IsEncryptedStorageValue(v.(string)) {
		t.Fatalf("re-save must encrypt legacy plaintext")
	}

	// Corrupted ciphertext falls back to the raw value rather than erroring
	// (documented fail-open on read; writes fail closed).
	var corrupt EncryptedString
	garbled := storagePrefix + base64.StdEncoding.EncodeToString([]byte("junk")) + storageDelimiter + base64.StdEncoding.EncodeToString([]byte("junk"))
	if err := corrupt.Scan(garbled); err != nil {
		t.Fatalf("Scan(corrupt): %v", err)
	}
	if string(corrupt) != garbled {
		t.Fatalf("corrupt value must fall back to raw, got %q", corrupt)
	}
}

func TestEncryptedStringWithoutServiceStoresPlaintext(t *testing.T) {
	// No global service configured: writes degrade to plaintext rather than
	// erroring (unmanaged deployments).
	if globalCryptoService != nil {
		t.Fatalf("global service must be unset for this test")
	}
	v, err := EncryptedString("secret").Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	if v.(string) != "secret" {
		t.Fatalf("without service the value passes through, got %v", v)
	}
	_ = driver.Value(nil) // keep the driver import honest if assertions change
}

func TestParseRSAPrivateKeyFromPEMRoundTrip(t *testing.T) {
	privPEM, pubPEM, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	if !strings.Contains(pubPEM, "PUBLIC KEY") {
		t.Fatalf("public PEM malformed: %q", pubPEM[:32])
	}
	key, err := ParseRSAPrivateKeyFromPEM([]byte(privPEM))
	if err != nil {
		t.Fatalf("ParseRSAPrivateKeyFromPEM: %v", err)
	}
	if key.N.BitLen() != 2048 {
		t.Fatalf("key size = %d, want 2048", key.N.BitLen())
	}
	if _, err := ParseRSAPrivateKeyFromPEM([]byte("not a pem")); err == nil {
		t.Fatalf("garbage PEM must error")
	}
}

func TestNormalizeAESKey(t *testing.T) {
	for _, size := range []int{16, 24, 32} {
		raw := make([]byte, size)
		key, ok := normalizeAESKey(raw)
		if !ok || len(key) != size {
			t.Fatalf("size %d: ok=%v len=%d", size, ok, len(key))
		}
	}
	if _, ok := normalizeAESKey(nil); ok {
		t.Fatalf("empty key must be rejected")
	}
	key, ok := normalizeAESKey([]byte("arbitrary length passphrase"))
	if !ok || len(key) != 32 {
		t.Fatalf("non-standard size must normalize to 32 bytes, got %d ok=%v", len(key), ok)
	}
}

func TestDecryptPayloadRejectsStaleTimestamp(t *testing.T) {
	cs := newTestService(t)

	stale := &EncryptedPayload{TS: time.Now().Add(-10 * time.Minute).Unix()}
	if _, err := cs.DecryptPayload(stale); err == nil {
		t.Fatalf("stale timestamp must be rejected")
	}
	missing := &EncryptedPayload{}
	if _, err := cs.DecryptPayload(missing); err == nil {
		t.Fatalf("missing timestamp must be rejected")
	}
	future := &EncryptedPayload{TS: time.Now().Add(-2 * time.Minute).Unix()}
	// Within the 5-minute window the RSA unwrap fails on garbage, but the
	// timestamp check itself must not reject it.
	if _, err := cs.DecryptPayload(future); err != nil && strings.Contains(err.Error(), "timestamp") {
		t.Fatalf("fresh timestamp should pass the replay check, got: %v", err)
	}
}
