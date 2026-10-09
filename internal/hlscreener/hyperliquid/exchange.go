// Exchange endpoint client — ORDER PLACEMENT (testnet-ready).
//
// Signing is implemented in signing.go (EIP-712 Agent envelope, light
// deps: msgpack + decred secp256k1). The action structs below carry
// msgpack field tags because the L1 action hash is computed over the
// msgpack bytes — field ORDER matters, and Go maps are unordered.
// Field order mirrors the python SDK's dict construction exactly.
package hyperliquid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ExchangeURL returns the /exchange endpoint for the configured
// network. Mainnet default; testnet via HYPERLIQUID_API_URL.
func (c *Client) exchangeURL() string {
	return c.apiURL + "/exchange"
}

// OrderAction is the HL "order" action (field order = python SDK).
type OrderAction struct {
	Type     string      `json:"type"     msgpack:"type"`
	Orders   []OrderWire `json:"orders"   msgpack:"orders"`
	Grouping string      `json:"grouping" msgpack:"grouping"`
}

// LimitTif is the "t" variant for limit orders: {"limit": {"tif": ...}}.
type LimitTif struct {
	Limit TifInner `json:"limit" msgpack:"limit"`
}

type TifInner struct {
	Tif string `json:"tif" msgpack:"tif"` // "Gtc" | "Ioc" | "Alo"
}

// OrderWire is one order in the exchange request.
// Field names are HL's single-letter wire keys. NOTE: `a` is the
// asset INDEX in the meta universe (a number), not the coin name —
// use AssetIndex() to resolve it.
type OrderWire struct {
	Asset      int      `json:"a" msgpack:"a"` // universe index (BTC=0 on mainnet)
	IsBuy      bool     `json:"b" msgpack:"b"` // true=buy, false=sell
	LimitPx    string   `json:"p" msgpack:"p"` // price (tick-aligned string)
	Sz         string   `json:"s" msgpack:"s"` // size (szDecimals-aligned string)
	ReduceOnly bool     `json:"r" msgpack:"r"`
	T          LimitTif `json:"t" msgpack:"t"`
}

// CancelAction is the HL "cancel" action (field order = python SDK).
type CancelAction struct {
	Type    string       `json:"type"    msgpack:"type"`
	Cancels []CancelWire `json:"cancels" msgpack:"cancels"`
}

type CancelWire struct {
	Asset int   `json:"a" msgpack:"a"` // universe index
	Oid   int64 `json:"o" msgpack:"o"`
}

// exchangeRequest is the signed envelope posted to /exchange.
type exchangeRequest struct {
	Action    any        `json:"action"`
	Nonce     uint64     `json:"nonce"`
	Signature *Signature `json:"signature"`
}

// PlaceOrder signs and posts an order action.
//
//	isTestnet:   selects the EIP-712 source ("b" vs "a") — the URL
//	             itself is set via HYPERLIQUID_API_URL at client build.
//	vaultAddress: nil for direct trading, or a 20-byte address.
//	expiresAfter: nil (absent) or an epoch-ms deadline.
func (c *Client) PlaceOrder(ctx context.Context, privateKeyHex string, action OrderAction, isTestnet bool, vaultAddress []byte, expiresAfter *uint64) (map[string]any, error) {
	nonce := uint64(time.Now().UnixMilli())
	sig, err := SignL1Action(privateKeyHex, action, nonce, vaultAddress, expiresAfter, isTestnet)
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	return c.postExchange(ctx, exchangeRequest{Action: action, Nonce: nonce, Signature: sig})
}

// CancelOrder signs and posts a cancel action.
func (c *Client) CancelOrder(ctx context.Context, privateKeyHex string, action CancelAction, isTestnet bool, vaultAddress []byte, expiresAfter *uint64) (map[string]any, error) {
	nonce := uint64(time.Now().UnixMilli())
	sig, err := SignL1Action(privateKeyHex, action, nonce, vaultAddress, expiresAfter, isTestnet)
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	return c.postExchange(ctx, exchangeRequest{Action: action, Nonce: nonce, Signature: sig})
}

// AssetIndex resolves a coin name ("BTC") to its universe index for
// exchange actions. Errors if the coin is not in the perps universe.
func (c *Client) AssetIndex(ctx context.Context, coin string) (int, error) {
	meta, err := c.Meta(ctx)
	if err != nil {
		return 0, err
	}
	for i, a := range meta.Universe {
		if a.Name == coin {
			return i, nil
		}
	}
	return 0, fmt.Errorf("coin %s not in universe", coin)
}

func (c *Client) postExchange(ctx context.Context, req exchangeRequest) (map[string]any, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.exchangeURL(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("exchange %d: %s", resp.StatusCode, string(buf))
	}
	var out map[string]any
	if err := json.Unmarshal(buf, &out); err != nil {
		return nil, err
	}
	return out, nil
}
