// Package bybit provides the public linear market-data surface nofx needs for
// positioning context: tickers (OI, funding, mark) and the all-liquidation
// stream. It deliberately does not touch account or order endpoints — trading
// lives in trader/bybit.
package bybit

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
	DefaultBaseURL = "https://api.bybit.com"
	DefaultWSURL   = "wss://stream.bybit.com/v5/public/linear"

	maxAttempts = 3
)

// Client is a public v5 market-data client. All endpoints it uses are
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
	if result := hook.HookExec[hook.SetHttpClientResult](hook.SET_HTTP_CLIENT, httpClient); result != nil && result.Error() == nil {
		httpClient = result.GetResult()
	}
	return &Client{
		httpClient: httpClient,
		baseURL:    strings.TrimRight(baseURL, "/"),
		wsURL:      strings.TrimRight(wsURL, "/"),
	}
}

// response is the v5 envelope. retCode 0 is success; anything else carries a
// message and must not be parsed as an empty result.
type response struct {
	RetCode int             `json:"retCode"`
	RetMsg  string          `json:"retMsg"`
	Result  json.RawMessage `json:"result"`
}

func (c *Client) get(ctx context.Context, path string, params url.Values, out any) error {
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

			if resp.StatusCode == http.StatusTooManyRequests {
				lastErr = fmt.Errorf("%s rate limited", path)
				if wait := retryAfter(resp.Header.Get("Retry-After")); wait > 0 {
					if err := sleepCtx(ctx, wait); err != nil {
						return err
					}
					continue
				}
			} else if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("%s failed (status %d): %s", path, resp.StatusCode, truncate(string(body), 200))
			} else {
				var envelope response
				if err := json.Unmarshal(body, &envelope); err != nil {
					return fmt.Errorf("failed to parse %s: %w", path, err)
				}
				if envelope.RetCode != 0 {
					return fmt.Errorf("%s rejected (retCode %d): %s", path, envelope.RetCode, envelope.RetMsg)
				}
				if len(envelope.Result) == 0 {
					return fmt.Errorf("%s returned no result", path)
				}
				if err := json.Unmarshal(envelope.Result, out); err != nil {
					return fmt.Errorf("failed to parse %s result: %w", path, err)
				}
				return nil
			}
		}

		if attempt == maxAttempts-1 {
			break
		}
		logger.Warnf("⚠️ Bybit market request retry in %s: %v", backoff, lastErr)
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
