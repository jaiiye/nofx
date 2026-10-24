// Package binance provides the public USD-M futures market-data surface that
// nofx needs for positioning context: funding/mark prices, open interest, and
// the liquidation stream. It deliberately does not touch account or order
// endpoints — trading lives in trader/binance.
package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"nofx/hook"
	"nofx/logger"
)

const (
	DefaultBaseURL = "https://fapi.binance.com"
	DefaultWSURL   = "wss://fstream.binance.com/ws"

	maxAttempts = 3
)

// Client is a public futures market-data client. All endpoints it uses are
// unauthenticated.
type Client struct {
	httpClient *http.Client
	baseURL    string
	wsURL      string
}

func NewClient() *Client {
	return NewClientWithURLs(DefaultBaseURL, DefaultWSURL)
}

func NewClientWithURLs(baseURL, wsURL string) *Client {
	httpClient := &http.Client{Timeout: 30 * time.Second}

	// Same seam the existing market client uses: an embedded hook can swap the
	// transport, which is how deployments behind a region block route requests.
	if result := hook.HookExec[hook.SetHttpClientResult](hook.SET_HTTP_CLIENT, httpClient); result != nil && result.Error() == nil {
		httpClient = result.GetResult()
	}

	return &Client{
		httpClient: httpClient,
		baseURL:    strings.TrimRight(baseURL, "/"),
		wsURL:      strings.TrimRight(wsURL, "/"),
	}
}

// getJSON performs a GET with bounded retries. Binance answers a burst with 429
// (and a repeated offender with 418), so Retry-After is honored instead of
// hammering the endpoint.
func (c *Client) getJSON(ctx context.Context, path string, params url.Values, out any) error {
	endpoint := c.baseURL + path
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}

	var lastErr error
	backoff := time.Second
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("request failed: %w", err)
		} else {
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr != nil {
				return fmt.Errorf("failed to read response: %w", readErr)
			}

			switch {
			case resp.StatusCode == http.StatusOK:
				if err := json.Unmarshal(body, out); err != nil {
					return fmt.Errorf("failed to parse %s: %w", path, err)
				}
				return nil
			case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusTeapot:
				lastErr = fmt.Errorf("%s rate limited (status %d)", path, resp.StatusCode)
				if wait := retryAfter(resp.Header.Get("Retry-After")); wait > 0 {
					if err := sleepCtx(ctx, wait); err != nil {
						return err
					}
					continue
				}
			case resp.StatusCode >= 500:
				lastErr = fmt.Errorf("%s unavailable (status %d)", path, resp.StatusCode)
			default:
				return fmt.Errorf("%s failed (status %d): %s", path, resp.StatusCode, truncate(string(body), 200))
			}
		}

		if attempt == maxAttempts-1 {
			break
		}
		logger.Warnf("⚠️ Binance market request retry in %s: %v", backoff, lastErr)
		if err := sleepCtx(ctx, backoff); err != nil {
			return err
		}
		backoff *= 2
	}
	return lastErr
}

func retryAfter(header string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(header))
	if err != nil || seconds <= 0 {
		return 0
	}
	if seconds > 60 {
		seconds = 60
	}
	return time.Duration(seconds) * time.Second
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}
