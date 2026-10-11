package connector

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

// RFC 4231 test vectors for HMAC-SHA-256.
func TestHMACSHA256HexRFC4231(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		data    string
		wantHex string
	}{
		{
			name:    "case 1",
			key:     strings.Repeat("\x0b", 20),
			data:    "Hi There",
			wantHex: "b0344c61d8db38535ca8afceaf0bf12b881dc200c9833da726e9376c2e32cff7",
		},
		{
			name:    "case 2",
			key:     "Jefe",
			data:    "what do ya want for nothing?",
			wantHex: "5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HMACSHA256Hex(tc.key, tc.data); got != tc.wantHex {
				t.Fatalf("HMACSHA256Hex = %q, want %q", got, tc.wantHex)
			}
		})
	}
}

func TestHMACSHA256Base64MatchesDigest(t *testing.T) {
	// base64 is just the digest encoded; validate against the RFC vector.
	digest, err := hex.DecodeString("b0344c61d8db38535ca8afceaf0bf12b881dc200c9833da726e9376c2e32cff7")
	if err != nil {
		t.Fatal(err)
	}
	want := base64.StdEncoding.EncodeToString(digest)
	got := HMACSHA256Base64(strings.Repeat("\x0b", 20), "Hi There")
	if got != want {
		t.Fatalf("HMACSHA256Base64 = %q, want %q", got, want)
	}
}

func TestHMACSHA512HexKnownVector(t *testing.T) {
	// RFC 4231 test case 1 for HMAC-SHA-512.
	want := "87aa7cdea5ef619d4ff0b4241a1d6cb02379f4e2ce4ec2787ad0b30545e17cdedaa833b7d6b8a702038b274eaea3f4e4be9d914eeb61f1702e696c203a126854"
	if got := HMACSHA512Hex(strings.Repeat("\x0b", 20), "Hi There"); got != want {
		t.Fatalf("HMACSHA512Hex = %q, want %q", got, want)
	}
}
