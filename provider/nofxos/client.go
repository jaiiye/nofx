// Package nofxos provides market-wide quantitative data (open-interest
// rankings, fund-flow rankings, price gainers/losers, and the AI-scored
// "AI500" board) derived from free public sources.
//
// Historically this package proxied https://nofxos.ai through the paid
// claw402 gateway. That gateway has been removed from this build, so the
// data is now computed locally from:
//
//   - Hyperliquid native info API (metaAndAssetCtxs, candles) — the primary
//     source, since Hyperliquid is the only supported exchange.
//   - Binance public futures endpoints (ticker/24hr) as a free breadth source
//     so rankings are not limited to the Hyperliquid universe.
//
// Both are keyless and rate-limit friendly; callers should still prefer the
// cached accessors in ai500_cache.go for UI-facing polling.
package nofxos

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"nofx/logger"
	"nofx/security"
)

// Default configuration
const (
	// DefaultBaseURL is retained so callers that still reference it keep
	// compiling; no requests are routed to the legacy paid gateway anymore.
	DefaultBaseURL = "https://api.hyperliquid.xyz"
	// DefaultTimeout bounds every public data request.
	DefaultTimeout = 20 * time.Second
	// DefaultAuthKey is unused by the free sources; kept for API stability.
	DefaultAuthKey = ""
)

// Public, keyless data endpoints used by this package.
const (
	hyperliquidInfoURL   = "https://api.hyperliquid.xyz/info"
	binanceFuturesTicker = "https://fapi.binance.com/fapi/v1/ticker/24hr"
)

// Client is the market-data client.
type Client struct {
	BaseURL string
	AuthKey string
	Timeout time.Duration
	mu      sync.RWMutex
	http    *http.Client
}

var (
	defaultClient *Client
	clientOnce    sync.Once
)

// DefaultClient returns the singleton default client.
func DefaultClient() *Client {
	clientOnce.Do(func() {
		defaultClient = NewClient(DefaultBaseURL, DefaultAuthKey)
	})
	return defaultClient
}

// NewClient creates a new market-data client.
func NewClient(baseURL, authKey string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		BaseURL: baseURL,
		AuthKey: authKey,
		Timeout: DefaultTimeout,
		http:    &http.Client{Timeout: DefaultTimeout},
	}
}

// SetConfig updates client configuration.
func (c *Client) SetConfig(baseURL, authKey string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if baseURL != "" {
		c.BaseURL = baseURL
	}
	if authKey != "" {
		c.AuthKey = authKey
	}
}

// GetBaseURL returns the current base URL.
func (c *Client) GetBaseURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.BaseURL
}

// GetAuthKey returns the current auth key.
func (c *Client) GetAuthKey() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.AuthKey
}

// postJSON performs an HTTPS POST with a JSON body and returns the raw
// response payload. Used for the Hyperliquid info API, which only accepts POST.
func (c *Client) postJSON(url string, payload any) ([]byte, error) {
	if err := security.ValidateURL(url); err != nil {
		return nil, err
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to encode request: %w", err)
	}

	c.mu.RLock()
	timeout := c.Timeout
	c.mu.RUnlock()

	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := security.SafeHTTPClient(timeout)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return raw, &APIError{StatusCode: resp.StatusCode, Message: string(raw)}
	}
	return raw, nil
}

// getJSON performs a plain HTTPS GET and returns the raw response payload.
func (c *Client) getJSON(url string) ([]byte, error) {
	c.mu.RLock()
	timeout := c.Timeout
	c.mu.RUnlock()

	resp, err := security.SafeGet(url, timeout)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return raw, &APIError{StatusCode: resp.StatusCode, Message: string(raw)}
	}
	return raw, nil
}

// APIError represents an upstream API error response.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("upstream returned status %d", e.StatusCode)
	}
	return e.Message
}

// ExtractAuthKey is retained for backwards compatibility; the free data
// sources do not use an auth key.
func ExtractAuthKey(url string) string {
	if idx := strings.Index(url, "auth="); idx != -1 {
		authKey := url[idx+5:]
		if ampIdx := strings.Index(authKey, "&"); ampIdx != -1 {
			authKey = authKey[:ampIdx]
		}
		return authKey
	}
	return ""
}

// warnFetchFailure logs a non-fatal upstream failure.
func warnFetchFailure(what string, err error) {
	logger.Warnf("⚠️  Failed to fetch %s from public source: %v", what, err)
}
