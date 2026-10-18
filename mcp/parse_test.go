package mcp

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nofx/logger"
)

// A reasoning model can spend its whole completion budget on thinking and return
// no answer. That used to come back as "" with no error, so the decision cycle
// silently stored a "wait" with no content. It must fail the cycle — and NOT be
// retried: the same request with the same budget reproduces the same empty answer.
func TestCallRejectsEmptyCompletion(t *testing.T) {
	newClient := func(responseBody string) *Client {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, responseBody)
		}))
		t.Cleanup(server.Close)

		client := &Client{
			APIKey:     "test-key",
			BaseURL:    server.URL,
			Model:      "test-model",
			HTTPClient: server.Client(),
			Log:        logger.NewMCPLogger(),
			Cfg:        DefaultConfig(),
		}
		client.Hooks = client
		return client
	}

	// Reasoning-only completion.
	client := newClient(`{"choices":[{"message":{"content":"","reasoning_content":"thinking hard"}}]}`)
	_, err := client.Call("system", "user")
	if err == nil {
		t.Fatal("an empty completion must be an error")
	}
	if !errors.Is(err, ErrEmptyCompletion) {
		t.Fatalf("error = %v, want ErrEmptyCompletion", err)
	}
	if client.IsRetryableError(err) {
		t.Fatalf("an empty completion must NOT be retried, got %v", err)
	}

	// Whitespace-only content is the same failure.
	client = newClient(`{"choices":[{"message":{"content":"   "}}]}`)
	if _, err := client.Call("system", "user"); !errors.Is(err, ErrEmptyCompletion) {
		t.Fatalf("blank content: err = %v, want ErrEmptyCompletion", err)
	}

	// A real answer passes through untouched.
	client = newClient(`{"choices":[{"message":{"content":"  {\"action\":\"wait\"}  "}}]}`)
	got, err := client.Call("system", "user")
	if err != nil {
		t.Fatalf("content response: %v", err)
	}
	if strings.TrimSpace(got) != `{"action":"wait"}` {
		t.Fatalf("content = %q", got)
	}
}

// A reasoning model needs room for both the thinking and the answer; the old
// 2000-token default was routinely exhausted before any content was produced.
func TestDefaultMaxTokensLeavesRoomForReasoning(t *testing.T) {
	if got := DefaultConfig().MaxTokens; got < 4096 {
		t.Fatalf("default MaxTokens = %d, want at least 4096 for reasoning models", got)
	}
}
