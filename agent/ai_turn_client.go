package agent

import (
	"context"

	"nofx/mcp"
)

// Per-request AI client injection.
//
// Agent used to keep ONE shared aiClient field that was reassigned on every
// incoming message based on that message's store user. With multiple store
// users talking concurrently, user A's in-flight turn could suddenly run on
// user B's freshly-loaded model (and the field write itself was a data race).
//
// Now the request path resolves the client once per request, injects it into
// the turn context (withTurnAIClient), and every downstream LLM call reads it
// via turnAIClient(ctx). The shared field remains only as the default-store
// user's client for background jobs (briefs, maintenance) and is guarded by a
// RWMutex.

type turnAIClientKey struct{}

// withTurnAIClient resolves the best usable AI client for this store user once
// per request and injects it into ctx. Downstream code must read it back with
// turnAIClient(ctx) instead of touching the shared field.
//
// Loading a client is cheap (config + http.Client; the wallet-balance probe
// inside model ranking is cached), so resolving per request guarantees model
// config edits take effect on the next message.
func (a *Agent) withTurnAIClient(ctx context.Context, storeUserID string) context.Context {
	var client mcp.AIClient
	if a != nil {
		if loaded, _, ok := a.loadAIClientFromStoreUser(storeUserID); ok {
			client = loaded
		}
	}
	return context.WithValue(ctx, turnAIClientKey{}, client)
}

// turnAIClient returns the per-request injected client, falling back to the
// default (shared, mutex-guarded) client for callers without an injected turn
// context — background jobs and tests.
func (a *Agent) turnAIClient(ctx context.Context) mcp.AIClient {
	if a == nil {
		return nil
	}
	if ctx != nil {
		if client, ok := ctx.Value(turnAIClientKey{}).(mcp.AIClient); ok && client != nil {
			return client
		}
	}
	return a.defaultAIClient()
}

// turnClientContext detaches the injected client onto a fresh context for
// async continuation work (post-response maintenance) that outlives the
// request but must keep using the requesting store user's model.
func turnClientContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	if client, ok := ctx.Value(turnAIClientKey{}).(mcp.AIClient); ok && client != nil {
		return context.WithValue(context.Background(), turnAIClientKey{}, client)
	}
	return context.Background()
}
