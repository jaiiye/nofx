package agent

import (
	"log/slog"
	"path/filepath"
	"testing"

	"nofx/store"
)

func TestLoadAIClientFromStoreUserSelectsEnabledModel(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "agent-model-selection.db")
	st, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	if err := st.AIModel().UpdateWithName("default", "default_deepseek", "DeepSeek", true, "sk-test-deepseek", "", "deepseek-chat"); err != nil {
		t.Fatalf("create deepseek model: %v", err)
	}

	a := New(nil, st, DefaultConfig(), slog.Default())
	_, modelName, ok := a.loadAIClientFromStoreUser("default")
	if !ok {
		t.Fatalf("expected model selection to succeed")
	}
	if modelName != "deepseek-chat" {
		t.Fatalf("expected deepseek-chat to be selected, got %q", modelName)
	}
}

func TestLoadAIClientFromStoreUserSkipsDisabledModel(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "agent-model-selection-disabled.db")
	st, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	if err := st.AIModel().UpdateWithName("default", "default_deepseek", "DeepSeek", false, "sk-test-deepseek", "", "deepseek-chat"); err != nil {
		t.Fatalf("create disabled deepseek model: %v", err)
	}

	a := New(nil, st, DefaultConfig(), slog.Default())
	if _, _, ok := a.loadAIClientFromStoreUser("default"); ok {
		t.Fatal("expected disabled model to be skipped")
	}
}
