// EIP-712 signing for Hyperliquid L1 actions (order / cancel).
//
// Algorithm (mirrors hyperliquid-python-sdk `signing.py`):
//
//	action_hash   = keccak256(msgpack.pack((action, nonce, vault_address)))
//	digest        = keccak256(0x1901 ‖ domainSeparator ‖ structHash)
//	domainSep     = keccak256(abi.encode(
//	                  keccak256("EIP712Domain(string name,string version,uint256 chainId)"),
//	                  keccak256("Exchange"), keccak256("1"), 1337))
//	structHash    = keccak256(abi.encode(
//	                  keccak256("Agent(string source,bytes32 connectionId)"),
//	                  keccak256(source), action_hash))
//	source        = "a" (API/agent wallet)
//	                | "x" (mainnet user wallet)
//	                | "b" (testnet user wallet)
//
// Dependencies are deliberately light: vmihailenco/msgpack for the
// exact msgpack byte layout python's SDK produces, decred secp256k1
// for signing/recovery. No go-ethereum.
//
// CAVEAT: the msgpack encoding uses Go structs with fixed field order
// (Go maps are unordered and would produce unstable hashes). Field
// order matches the python SDK's dict construction order exactly.
// The signing pipeline is unit-tested for self-consistency (recover
// the signer address) and payload determinism; full format validation
// requires a live testnet order (see cmd/testnet-order).
package hyperliquid

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/vmihailenco/msgpack/v5"
	"golang.org/x/crypto/sha3"
)

// HLChainID is the EIP-712 chainId HL signs with (same on testnet).
const HLChainID = 1337

// keccak256 hashes data with Keccak-256 (NOT sha3-256 — different padding).
func keccak256(data ...[]byte) []byte {
	h := sha3.NewLegacyKeccak256()
	for _, d := range data {
		h.Write(d)
	}
	return h.Sum(nil)
}

// DebugPayload exposes the raw msgpack bytes of the action for
// byte-level comparison against the python SDK. Debug-only.
func DebugPayload(action any) ([]byte, error) {
	return l1Payload(action)
}

// DebugActionHash exposes the L1 action hash for debugging. Debug-only.
func DebugActionHash(action any, nonce uint64) ([]byte, error) {
	return actionHash(action, nil, nonce, nil)
}

// l1Payload builds the msgpack bytes of the action alone. The nonce
// and vault marker are appended as RAW bytes in actionHash (not
// msgpack) — verified against hyperliquid-python-sdk v0.24.
func l1Payload(action any) ([]byte, error) {
	return msgpack.Marshal(action)
}

// actionHash computes the HL L1 action hash, byte-exact with the
// python SDK:
//
//	data = msgpack.packb(action)
//	data += nonce.to_bytes(8, "big")
//	data += b"\x00"                      (vault_address is None)
//	  | b"\x01" + address_to_bytes(va)   (vault present)
//	data += b"\x00" + expiry.to_bytes(8) (expires_after present)
//	action_hash = keccak256(data)
func actionHash(action any, vaultAddress []byte, nonce uint64, expiresAfter *uint64) ([]byte, error) {
	packed, err := msgpack.Marshal(action)
	if err != nil {
		return nil, fmt.Errorf("msgpack: %w", err)
	}
	data := make([]byte, 0, len(packed)+24)
	data = append(data, packed...)

	var nonceBytes [8]byte
	binary.BigEndian.PutUint64(nonceBytes[:], nonce)
	data = append(data, nonceBytes[:]...)

	if vaultAddress == nil {
		data = append(data, 0x00)
	} else {
		data = append(data, 0x01)
		data = append(data, vaultAddress...)
	}

	if expiresAfter != nil {
		data = append(data, 0x00)
		var expBytes [8]byte
		binary.BigEndian.PutUint64(expBytes[:], *expiresAfter)
		data = append(data, expBytes[:]...)
	}
	return keccak256(data), nil
}

// eip712DomainSeparator for HL's Agent envelope. NOTE: HL's domain
// includes verifyingContract (the zero address) — omitting it
// produces a different separator and the API recovers a different
// signer.
func eip712DomainSeparator() []byte {
	typeHash := keccak256([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"))
	nameHash := keccak256([]byte("Exchange"))
	versionHash := keccak256([]byte("1"))
	chain := make([]byte, 32)
	chain[30] = byte(HLChainID >> 8)   // 1337 = 0x0539 → [30]=0x05
	chain[31] = byte(HLChainID & 0xff) //              [31]=0x39
	contract := make([]byte, 32)       // zero address, left-padded
	var buf []byte
	buf = append(buf, typeHash...)
	buf = append(buf, nameHash...)
	buf = append(buf, versionHash...)
	buf = append(buf, chain...)
	buf = append(buf, contract...)
	return keccak256(buf)
}

// agentStructHash for the Agent{source, connectionId} primary type.
func agentStructHash(source string, connectionID []byte) []byte {
	typeHash := keccak256([]byte("Agent(string source,bytes32 connectionId)"))
	sourceHash := keccak256([]byte(source))
	if len(connectionID) != 32 {
		panic("connectionId must be 32 bytes")
	}
	var buf []byte
	buf = append(buf, typeHash...)
	buf = append(buf, sourceHash...)
	buf = append(buf, connectionID...)
	return keccak256(buf)
}

// eip712Digest = keccak256(0x1901 ‖ domainSeparator ‖ structHash).
func eip712Digest(domainSep, structHash []byte) []byte {
	prefix := []byte{0x19, 0x01}
	return keccak256(prefix, domainSep, structHash)
}

// SourceFor returns the EIP-712 "source" value. Verified against
// python SDK v0.24 construct_phantom_agent: mainnet → "a",
// testnet → "b".
func SourceFor(isTestnet bool) string {
	if isTestnet {
		return "b"
	}
	return "a"
}

// Signature is the HL wire signature ({r,s,v} hex form).
type Signature struct {
	R string `json:"r"`
	S string `json:"s"`
	V int    `json:"v"`
}

// SignL1Action signs an HL L1 action and returns the wire signature.
//
//	privateKeyHex: secp256k1 private key, with or without 0x prefix.
//	vaultAddress:  nil (no vault) or a 20-byte address.
//	expiresAfter:  nil (absent) or an epoch-ms deadline.
//
// Source rules (verified against python SDK v0.24
// construct_phantom_agent): mainnet → "a", testnet → "b".
func SignL1Action(privateKeyHex string, action any, nonce uint64, vaultAddress []byte, expiresAfter *uint64, isTestnet bool) (*Signature, error) {
	key, err := parsePrivateKey(privateKeyHex)
	if err != nil {
		return nil, err
	}
	ah, err := actionHash(action, vaultAddress, nonce, expiresAfter)
	if err != nil {
		return nil, err
	}
	digest := eip712Digest(
		eip712DomainSeparator(),
		agentStructHash(SourceFor(isTestnet), ah),
	)
	return signDigest(key, digest)
}

func parsePrivateKey(hexKey string) (*secp256k1.PrivateKey, error) {
	hexKey = strings.TrimPrefix(strings.TrimSpace(hexKey), "0x")
	b, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("private key hex: %w", err)
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("private key must be 32 bytes, got %d", len(b))
	}
	return secp256k1.PrivKeyFromBytes(b), nil
}

// signDigest signs a 32-byte digest, producing Ethereum-style (r, s, v)
// with v = 27 + recoveryID.
func signDigest(key *secp256k1.PrivateKey, digest []byte) (*Signature, error) {
	// SignCompact returns [recoveryCode, R(32), S(32)] where
	// recoveryCode = 27 + recoveryID (+4 when compressed key).
	compact := ecdsa.SignCompact(key, digest, false)
	if len(compact) != 65 {
		return nil, fmt.Errorf("unexpected compact sig length %d", len(compact))
	}
	v := int(compact[0])
	if v >= 31 {
		v -= 4 // strip compressed-key flag
	}
	if v < 27 || v > 28 {
		return nil, fmt.Errorf("unexpected recovery code %d", compact[0])
	}
	r := compact[1:33]
	s := compact[33:65]
	return &Signature{
		R: "0x" + hex.EncodeToString(r),
		S: "0x" + hex.EncodeToString(s),
		V: v,
	}, nil
}

// PrivateKeyAddress derives the 0x-prefixed Ethereum address of a
// private key (keccak256(uncompressed pubkey[1:])[12:]) — used for
// dry-run display and self-consistency checks.
func PrivateKeyAddress(privateKeyHex string) (string, error) {
	key, err := parsePrivateKey(privateKeyHex)
	if err != nil {
		return "", err
	}
	pub := key.PubKey().SerializeUncompressed()[1:] // drop 0x04 prefix
	h := keccak256(pub)
	return "0x" + hex.EncodeToString(h[12:]), nil
}

// keccakSelfTest guards against a subtle dependency trap: Go's
// x/crypto has both sha3.Sum256 (SHA3 padding) and NewLegacyKeccak256
// (Ethereum padding). If we accidentally used the wrong one, every
// signature would be wrong. Verified against the canonical reference:
// keccak256("abc") = 4e03657aea45a94fc7d47ba826c8d667c0d1e6e33a64a036ec44f58fa12d6c45
// (keccak_256 test vector, emn178 reference tool + Keccak team KATs).
func keccakSelfTest() error {
	got := hex.EncodeToString(keccak256([]byte("abc")))
	want := "4e03657aea45a94fc7d47ba826c8d667c0d1e6e33a64a036ec44f58fa12d6c45"
	if got != want {
		return fmt.Errorf("keccak256 self-test failed: got %s", got)
	}
	return nil
}

func init() {
	if err := keccakSelfTest(); err != nil {
		panic(err)
	}
}
