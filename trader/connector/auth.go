// Package connector holds shared plumbing for exchange adapters: request
// signing, an HTTP client with rate limiting, and (later) unified order/fee
// types. The goal is to stop each adapter from re-implementing the same
// transport concerns.
package connector

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
)

// Credentials is the common API-key triple used by most exchanges.
type Credentials struct {
	APIKey     string
	SecretKey  string
	Passphrase string
}

// HMACSHA256Base64 signs preHash with HMAC-SHA256 and returns the base64
// digest. OKX and Bitget share this exact scheme:
//
//	base64(HMAC_SHA256(timestamp + method + requestPath + body, secret))
func HMACSHA256Base64(secret, preHash string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(preHash))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// HMACSHA256Hex is the hex-encoded HMAC-SHA256 digest.
func HMACSHA256Hex(secret, preHash string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(preHash))
	return hex.EncodeToString(mac.Sum(nil))
}

// HMACSHA512Hex is the hex-encoded HMAC-SHA512 digest (e.g. Indodax).
func HMACSHA512Hex(secret, preHash string) string {
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write([]byte(preHash))
	return hex.EncodeToString(mac.Sum(nil))
}
