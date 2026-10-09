package hyperliquid

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

// TestKeccakKnownVector — "abc" is the canonical Keccak-256 test
// vector (verified against reference tooling). Catches wrong-padding
// (sha3-256 vs keccak-256), which would silently invalidate every
// signature.
func TestKeccakKnownVector(t *testing.T) {
	got := hex.EncodeToString(keccak256([]byte("abc")))
	want := "4e03657aea45a94fc7d47ba826c8d667c0d1e6e33a64a036ec44f58fa12d6c45"
	if got != want {
		t.Fatalf("keccak256(abc) = %s, want %s", got, want)
	}
}

// TestGoldenSDKVector — the signature MUST reproduce, byte-for-byte,
// what hyperliquid-python-sdk v0.24 produces for the same
// action/nonce/key. Vectors generated with:
//
//	wallet = eth_account.Account.from_key(key)
//	action = {"type":"order","orders":[{"a":3,"b":True,"p":"10000",
//	          "s":"0.001","r":False,"t":{"limit":{"tif":"Gtc"}}}],
//	          "grouping":"na"}
//	sign_l1_action(wallet, action, None, nonce=1791505567726, None,
//	               is_mainnet=False)
//
// This is the definitive format validation — a mismatch here means a
// signing-layer divergence that self-consistency tests cannot catch.
func TestGoldenSDKVector(t *testing.T) {
	const key = "0x0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const wantActionHash = "2ab49f892e7b58067afba3b00adc330c6fd317fd1097ed7cb4b7830f27cd7ef0"
	want := Signature{
		R: "0xc15c1dde705001afb508273adc90d1dee87c512e8f046e608ad4ff46210575cd",
		S: "0x246fe14fe82cca399a66d38622ad5e1de410cdbd83c30a32aa6608eb0e3cc8eb",
		V: 28,
	}
	action := OrderAction{
		Type: "order",
		Orders: []OrderWire{{
			Asset: 3, IsBuy: true,
			LimitPx: "10000", Sz: "0.001",
			ReduceOnly: false,
			T:          LimitTif{Limit: TifInner{Tif: "Gtc"}},
		}},
		Grouping: "na",
	}
	nonce := uint64(1791505567726)

	ah, err := actionHash(action, nil, nonce, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(ah); got != wantActionHash {
		t.Fatalf("action hash = %s, want %s", got, wantActionHash)
	}

	sig, err := SignL1Action(key, action, nonce, nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if sig.R != want.R || sig.S != want.S || sig.V != want.V {
		t.Fatalf("signature mismatch:\n got  %+v\n want %+v", sig, want)
	}
}

// TestMsgpackActionGolden — msgpack(action) bytes must match the
// SDK's msgpack.packb(action) exactly (field order + wire types).
func TestMsgpackActionGolden(t *testing.T) {
	action := OrderAction{
		Type: "order",
		Orders: []OrderWire{{
			Asset: 3, IsBuy: true,
			LimitPx: "10000", Sz: "0.001",
			ReduceOnly: false,
			T:          LimitTif{Limit: TifInner{Tif: "Gtc"}},
		}},
		Grouping: "na",
	}
	p, err := l1Payload(action)
	if err != nil {
		t.Fatal(err)
	}
	want := "83a474797065a56f72646572a66f72646572739186a16103a162c3a170a53130303030a173a5302e303031a172c2a17481a56c696d697481a3746966a3477463a867726f7570696e67a26e61"
	if got := hex.EncodeToString(p); got != want {
		t.Fatalf("msgpack(action):\n got  %s\n want %s", got, want)
	}
}

// TestSignRecoverSelfConsistency — sign, recover the pubkey from
// (r, s, v), verify it derives the signer's address.
func TestSignRecoverSelfConsistency(t *testing.T) {
	keyHex := "0x0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	addr, err := PrivateKeyAddress(keyHex)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(addr, "0x") || len(addr) != 42 {
		t.Fatalf("address format wrong: %s", addr)
	}

	action := OrderAction{
		Type: "order",
		Orders: []OrderWire{{
			Asset: 3, IsBuy: true,
			LimitPx: "10000", Sz: "0.001",
			T: LimitTif{Limit: TifInner{Tif: "Gtc"}},
		}},
		Grouping: "na",
	}
	nonce := uint64(1700000000000)
	sig, err := SignL1Action(keyHex, action, nonce, nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if sig.V != 27 && sig.V != 28 {
		t.Fatalf("v = %d, want 27 or 28", sig.V)
	}
	rBytes, _ := hex.DecodeString(strings.TrimPrefix(sig.R, "0x"))
	sBytes, _ := hex.DecodeString(strings.TrimPrefix(sig.S, "0x"))

	ah, err := actionHash(action, nil, nonce, nil)
	if err != nil {
		t.Fatal(err)
	}
	digest := eip712Digest(
		eip712DomainSeparator(),
		agentStructHash(SourceFor(true), ah),
	)

	// decred's RecoverCompact expects the Ethereum-style header
	// (27/28, or 31/34 for compressed keys) — pass v as-is.
	compact := append([]byte{byte(sig.V)}, append(rBytes, sBytes...)...)
	pubKey, _, err := ecdsa.RecoverCompact(compact, digest)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	pubBytes := pubKey.SerializeUncompressed()[1:]
	recovered := "0x" + hex.EncodeToString(keccak256(pubBytes)[12:])
	if recovered != addr {
		t.Fatalf("recovered address %s != signer %s", recovered, addr)
	}
}

// TestSourceFor — wire-source selection (SDK v0.24 semantics).
func TestSourceFor(t *testing.T) {
	if got := SourceFor(false); got != "a" {
		t.Errorf("SourceFor(mainnet) = %q, want a", got)
	}
	if got := SourceFor(true); got != "b" {
		t.Errorf("SourceFor(testnet) = %q, want b", got)
	}
}

// TestActionHashChangesWithNonce — hash sensitivity to nonce.
func TestActionHashChangesWithNonce(t *testing.T) {
	action := OrderAction{Type: "order", Grouping: "na"}
	h1, _ := actionHash(action, nil, 1000, nil)
	h2, _ := actionHash(action, nil, 1001, nil)
	if hex.EncodeToString(h1) == hex.EncodeToString(h2) {
		t.Fatal("action hash unchanged across nonces")
	}
}

// TestPrivateKeyParsing — 0x prefix optional, bad inputs rejected,
// generator-point sanity (priv=1 → known G).
func TestPrivateKeyParsing(t *testing.T) {
	if _, err := parsePrivateKey(strings.Repeat("ab", 32)); err != nil {
		t.Errorf("valid 32-byte key rejected: %v", err)
	}
	if _, err := parsePrivateKey("0x" + strings.Repeat("ab", 32)); err != nil {
		t.Errorf("0x-prefixed key rejected: %v", err)
	}
	if _, err := parsePrivateKey("abcd"); err == nil {
		t.Error("short key accepted")
	}
	if _, err := parsePrivateKey("zz" + strings.Repeat("ab", 31)); err == nil {
		t.Error("non-hex key accepted")
	}
	k, err := parsePrivateKey("0000000000000000000000000000000000000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	pub := k.PubKey().SerializeCompressed()
	want := "0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"
	if hex.EncodeToString(pub) != want {
		t.Fatalf("G derivation wrong: %x", pub)
	}
}
