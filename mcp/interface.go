package mcp

import (
	"context"
	"time"
)

// ClientEmbedder is implemented by provider types that embed *Client,
// allowing generic extraction of the underlying base client (e.g. for cloning).
type ClientEmbedder interface {
	BaseClient() *Client
}

// AIClient public AI client interface (for external use)
type AIClient interface {
	SetAPIKey(apiKey string, customURL string, customModel string)
	SetTimeout(timeout time.Duration)
	// CallWithMessages is the legacy system+user-prompt entry point.
	// ctx, when non-nil, cancels the call (including retry backoff waits);
	// nil is treated as context.Background().
	CallWithMessages(ctx context.Context, systemPrompt, userPrompt string) (string, error)
	CallWithRequest(req *Request) (string, error)
	// CallWithRequestStream streams the LLM response via SSE.
	// onChunk is called with the full accumulated text so far (not raw deltas).
	// Returns the complete final text when done.
	CallWithRequestStream(req *Request, onChunk func(string)) (string, error)
	// CallWithRequestFull returns both text content and tool calls.
	// Use this when the request includes Tools — the LLM may respond with
	// either a plain text reply (LLMResponse.Content) or tool invocations
	// (LLMResponse.ToolCalls), but not both.
	CallWithRequestFull(req *Request) (*LLMResponse, error)
}
