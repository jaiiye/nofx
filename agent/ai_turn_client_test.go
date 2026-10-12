package agent

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"nofx/mcp"
	"nofx/store"
)

// Per-request AI client injection: every request resolves its own client so
// concurrent store users cannot clobber each other's model selection (the old
// shared-field behaviour).
func TestTurnAIClientInjectionIsPerRequest(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "turn-client.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	// Two store users with different providers → different resolved clients.
	if err := st.AIModel().UpdateWithName("default", "default_openai", "OpenAI", true, "sk-default", "", "gpt-5.2"); err != nil {
		t.Fatalf("create default model: %v", err)
	}
	if err := st.AIModel().UpdateWithName("alice", "alice_deepseek", "DeepSeek", true, "sk-alice", "", "deepseek-chat"); err != nil {
		t.Fatalf("create alice model: %v", err)
	}

	a := New(nil, st, DefaultConfig(), slog.Default())
	stale := &scriptedAIClient{}
	a.SetAIClient(stale)

	// Without injection: falls back to the shared default.
	if got := a.turnAIClient(context.Background()); got != mcp.AIClient(stale) {
		t.Fatal("uninjected context must fall back to the shared default client")
	}

	ctxDefault := a.withTurnAIClient(context.Background(), "default")
	ctxAlice := a.withTurnAIClient(context.Background(), "alice")

	clientDefault := a.turnAIClient(ctxDefault)
	clientAlice := a.turnAIClient(ctxAlice)
	if clientDefault == nil || clientAlice == nil {
		t.Fatal("injected contexts must carry a resolved client")
	}
	if clientDefault == mcp.AIClient(stale) || clientAlice == mcp.AIClient(stale) {
		t.Fatal("injected clients must not be the stale shared field value")
	}
	// Different store users resolve independent clients, so concurrent turns
	// cannot clobber each other's model selection.
	if clientDefault == clientAlice {
		t.Fatal("different store users must resolve independent clients")
	}

	// No models configured → injected value is nil, falls back to default
	// (background-job compatibility) rather than panicking.
	a2 := New(nil, nil, DefaultConfig(), slog.Default())
	if got := a.turnAIClient(a2.withTurnAIClient(context.Background(), "default")); got != mcp.AIClient(stale) {
		t.Fatal("nil injection must fall back to the shared default client")
	}
}
