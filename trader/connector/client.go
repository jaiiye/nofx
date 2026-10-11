package connector

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// Client wraps an *http.Client with an optional rate limiter. Adapters call
// Do instead of touching net/http directly so transport concerns live in one
// place.
type Client struct {
	http    *http.Client
	limiter *Limiter
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient injects a custom HTTP client (e.g. a mock transport in tests).
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) {
		if h != nil {
			c.http = h
		}
	}
}

// WithLimiter attaches a rate limiter.
func WithLimiter(l *Limiter) Option {
	return func(c *Client) { c.limiter = l }
}

// WithTimeout sets the request timeout on the underlying client.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.http.Timeout = d
		}
	}
}

// NewClient builds a Client with a 30s default timeout.
func NewClient(opts ...Option) *Client {
	c := &Client{
		http: &http.Client{
			Timeout:   30 * time.Second,
			Transport: http.DefaultTransport,
		},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// maxQueueWait bounds how long a call may block waiting for a rate-limit
// token. http.Client.Timeout does not cover this queueing time, and adapter
// requests carry a Background context, so without a budget a burst could
// block the trading loop indefinitely.
const maxQueueWait = 10 * time.Second

// Do applies the rate limiter (if any) and performs the request. The caller
// owns the response body.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if c.limiter != nil {
		ctx, cancel := context.WithTimeout(req.Context(), maxQueueWait)
		defer cancel()
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("rate limit wait exceeded: %w", err)
		}
	}
	return c.http.Do(req)
}

// HTTPClient exposes the underlying client for callers that need it directly.
func (c *Client) HTTPClient() *http.Client { return c.http }
