package agent

import (
	"strings"
	"testing"
)

func TestModelProviderChoicePromptListsOnlyDeepSeek(t *testing.T) {
	for _, lang := range []string{"zh", "en"} {
		msg := modelProviderChoicePrompt(lang)
		if !strings.Contains(msg, "DeepSeek") {
			t.Fatalf("[%s] expected prompt to advertise DeepSeek, got: %s", lang, msg)
		}
		for _, unsupported := range []string{"claw402", "blockrun-base", "blockrun-sol", "OpenAI", "Claude", "Gemini", "Qwen", "Kimi", "Grok", "MiniMax"} {
			if strings.Contains(msg, unsupported) {
				t.Fatalf("[%s] prompt should not advertise removed provider %q, got: %s", lang, unsupported, msg)
			}
		}
	}
}

func TestSupportedModelProvidersIsDeepSeekOnly(t *testing.T) {
	ids := supportedModelProviderIDs()
	if len(ids) != 1 || ids[0] != "deepseek" {
		t.Fatalf("expected only deepseek to be supported, got %+v", ids)
	}
	if _, ok := modelProviderSpecByID("claw402"); ok {
		t.Fatal("claw402 should no longer be a supported model provider")
	}
}

func TestModelProviderCredentialGuidanceForDeepSeek(t *testing.T) {
	msg := modelProviderCredentialGuidance("zh", "deepseek")
	if !strings.Contains(msg, "DeepSeek") || !strings.Contains(msg, "API Key") {
		t.Fatalf("expected DeepSeek API key guidance, got: %s", msg)
	}
}

func TestModelProviderDetailedGuidanceForDeepSeek(t *testing.T) {
	msg := modelProviderDetailedGuidance("zh", "deepseek")
	for _, want := range []string{"DeepSeek", "deepseek-chat", "custom_api_url"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("expected detailed guidance to contain %q, got: %s", want, msg)
		}
	}
}

func TestModelProviderGuidanceForUnsupportedProviderIsEmpty(t *testing.T) {
	if msg := modelProviderCredentialGuidance("zh", "claw402"); msg != "" {
		t.Fatalf("expected empty guidance for removed provider, got: %s", msg)
	}
	if msg := modelProviderDetailedGuidance("zh", "claw402"); msg != "" {
		t.Fatalf("expected empty detailed guidance for removed provider, got: %s", msg)
	}
}
