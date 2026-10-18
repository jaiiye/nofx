package mcp

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nofx/logger"
)

// newTestClient serves one canned response per request and records the request
// bodies it received, so a test can assert on what actually went on the wire.
func newTestClient(t *testing.T, responseBody string) (*Client, *[]map[string]any) {
	t.Helper()

	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("failed to decode request body: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		bodies = append(bodies, body)
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
	return client, &bodies
}

// A reasoning model can spend its whole completion budget on thinking and return
// no answer. That used to come back as "" with no error, so the decision cycle
// silently stored a "wait" with no content. It must fail the cycle — and NOT be
// retried: the same request with the same budget reproduces the same empty answer.
func TestCallRejectsEmptyCompletion(t *testing.T) {
	// Reasoning-only completion.
	client, _ := newTestClient(t, `{"choices":[{"message":{"content":"","reasoning_content":"thinking hard"}}]}`)
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
	client, _ = newTestClient(t, `{"choices":[{"message":{"content":"   "}}]}`)
	if _, err := client.Call("system", "user"); !errors.Is(err, ErrEmptyCompletion) {
		t.Fatalf("blank content: err = %v, want ErrEmptyCompletion", err)
	}

	// A real answer passes through untouched.
	client, _ = newTestClient(t, `{"choices":[{"message":{"content":"  {\"action\":\"wait\"}  "}}]}`)
	got, err := client.Call("system", "user")
	if err != nil {
		t.Fatalf("content response: %v", err)
	}
	if strings.TrimSpace(got) != `{"action":"wait"}` {
		t.Fatalf("content = %q", got)
	}
}

// The reasoning/cached split only exists in usage, and it is the only place the
// cause of an empty completion (and the prompt-cache win) is visible.
func TestUsageDetailsAreReported(t *testing.T) {
	var reported TokenUsage
	previous := TokenUsageCallback
	TokenUsageCallback = func(usage TokenUsage) { reported = usage }
	t.Cleanup(func() { TokenUsageCallback = previous })

	client, _ := newTestClient(t, `{"choices":[{"message":{"content":"ok"}}],"usage":{`+
		`"prompt_tokens":1000,"completion_tokens":500,"total_tokens":1500,`+
		`"prompt_tokens_details":{"cached_tokens":768},`+
		`"completion_tokens_details":{"reasoning_tokens":480}}}`)

	if _, err := client.Call("system", "user"); err != nil {
		t.Fatalf("call: %v", err)
	}
	if reported.ReasoningTokens != 480 {
		t.Errorf("ReasoningTokens = %d, want 480", reported.ReasoningTokens)
	}
	if reported.CachedTokens != 768 {
		t.Errorf("CachedTokens = %d, want 768", reported.CachedTokens)
	}
	if reported.PromptTokens != 1000 || reported.CompletionTokens != 500 {
		t.Errorf("usage = %+v", reported)
	}
}

// Extra body overrides are the escape hatch for provider knobs nofx has no field
// for — a thinking model's reasoning switch above all. Both request paths must
// put them on the wire, and an explicit value must beat the built-in default.
func TestExtraBodyIsMergedIntoRequests(t *testing.T) {
	client, bodies := newTestClient(t, `{"choices":[{"message":{"content":"ok"}}]}`)
	client.ExtraBody = map[string]any{"thinking": false, "max_tokens": 1234}

	if _, err := client.Call("system", "user"); err != nil {
		t.Fatalf("Call: %v", err)
	}
	body := (*bodies)[0]
	if thinking, ok := body["thinking"].(bool); !ok || thinking {
		t.Errorf("thinking = %v, want false", body["thinking"])
	}
	if got, ok := body["max_tokens"].(float64); !ok || int(got) != 1234 {
		t.Errorf("max_tokens = %v, want 1234 (the override must win)", body["max_tokens"])
	}
	if body["model"] != "test-model" {
		t.Errorf("model = %v, want test-model", body["model"])
	}

	// Same for the builder path.
	if _, err := client.CallWithRequest(&Request{
		Messages: []Message{NewSystemMessage("system"), NewUserMessage("user")},
	}); err != nil {
		t.Fatalf("CallWithRequest: %v", err)
	}
	if thinking, ok := (*bodies)[1]["thinking"].(bool); !ok || thinking {
		t.Errorf("builder path thinking = %v, want false", (*bodies)[1]["thinking"])
	}
}

// AI_EXTRA_BODY_JSON is an opt-in provider override: a malformed value is dropped
// rather than failing startup.
func TestExtraBodyFromEnv(t *testing.T) {
	t.Setenv("AI_EXTRA_BODY_JSON", `{"thinking": false}`)
	if got := DefaultConfig().ExtraBody["thinking"]; got != false {
		t.Errorf("ExtraBody[thinking] = %v, want false", got)
	}

	t.Setenv("AI_EXTRA_BODY_JSON", `{not json`)
	if got := DefaultConfig().ExtraBody; got != nil {
		t.Errorf("malformed AI_EXTRA_BODY_JSON = %v, want nil", got)
	}
}

// A reasoning model needs room for both the thinking and the answer; the old
// 2000-token default was routinely exhausted before any content was produced.
func TestDefaultMaxTokensLeavesRoomForReasoning(t *testing.T) {
	if got := DefaultConfig().MaxTokens; got < 4096 {
		t.Fatalf("default MaxTokens = %d, want at least 4096 for reasoning models", got)
	}
}
