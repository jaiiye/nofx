package agent

import (
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"nofx/mcp"
	"nofx/store"
)

// --- Toolset convergence ---------------------------------------------------

func TestAgentToolsConvergeManagementIntoSkillEntries(t *testing.T) {
	names := toolNamesForTest(agentTools())

	for _, removed := range []string{
		"get_exchange_configs", "manage_exchange_config",
		"get_model_configs", "manage_model_config",
		"get_strategies", "manage_strategy",
		"manage_trader",
	} {
		if containsString(names, removed) {
			t.Fatalf("flat management tool %q must be replaced by skill domain entries in the agentic toolset", removed)
		}
	}

	for _, expected := range []string{
		"skill_exchange_management", "skill_model_management",
		"skill_strategy_management", "skill_trader_management",
		"execute_trade", "get_ai500_list", "get_watchlist", "manage_watchlist",
	} {
		if !containsString(names, expected) {
			t.Fatalf("expected tool %q in agentic toolset, got %v", expected, names)
		}
	}
}

// The legacy routing stack must keep the flat tools.
func TestLegacyCachedToolsKeepFlatManagementTools(t *testing.T) {
	names := toolNamesForTest(cachedTools)
	for _, expected := range []string{"manage_trader", "manage_strategy", "manage_model_config", "manage_exchange_config"} {
		if !containsString(names, expected) {
			t.Fatalf("legacy toolset must keep %q", expected)
		}
	}
	if containsString(names, "skill_model_management") {
		t.Fatal("legacy toolset must not contain skill domain entries")
	}
}

func TestSkillDomainToolsGeneratedFromRegistry(t *testing.T) {
	tools := skillDomainTools()
	if len(tools) != len(managementSkillDefinitions()) {
		t.Fatalf("expected one tool per management skill, got %d tools for %d skills", len(tools), len(managementSkillDefinitions()))
	}
	var modelTool *mcp.Tool
	for i := range tools {
		if tools[i].Function.Name == "skill_model_management" {
			modelTool = &tools[i]
		}
	}
	if modelTool == nil {
		t.Fatal("skill_model_management tool missing")
	}

	raw, err := json.Marshal(modelTool.Function.Parameters)
	if err != nil || !json.Valid(raw) {
		t.Fatalf("invalid parameters schema: %v", err)
	}
	params := modelTool.Function.Parameters
	props, _ := params["properties"].(map[string]any)
	if props == nil {
		t.Fatal("parameters must have properties")
	}
	action, ok := props["action"].(map[string]any)
	if !ok {
		t.Fatal("action property missing")
	}
	enum, _ := action["enum"].([]string)
	if !containsString(enum, "create") || !containsString(enum, "query_list") || !containsString(enum, "delete") {
		t.Fatalf("action enum must come from the skill JSON, got %v", enum)
	}
	// provider is an enum constraint in model_management.json → typed property.
	provider, ok := props["provider"].(map[string]any)
	if !ok {
		t.Fatal("provider property must be typed from field_constraints")
	}
	providerEnum, _ := provider["enum"].([]string)
	if !containsString(providerEnum, "deepseek") {
		t.Fatalf("provider enum values must come from the skill JSON, got %v", providerEnum)
	}
	if !strings.Contains(modelTool.Function.Description, "provider") {
		t.Fatal("tool description must carry the skill JSON description")
	}
}

// strategy_management.json has ~70 field constraints; they must collapse into
// a compact surface with a free-form fields object instead of exploding the schema.
func TestSkillDomainToolCompactsLargeFieldSets(t *testing.T) {
	var strategyTool *mcp.Tool
	for _, tool := range skillDomainTools() {
		if tool.Function.Name == "skill_strategy_management" {
			strategyTool = &tool
		}
	}
	if strategyTool == nil {
		t.Fatal("skill_strategy_management tool missing")
	}
	props := strategyTool.Function.Parameters["properties"].(map[string]any)
	if _, ok := props["fields"]; !ok {
		t.Fatal("large field sets must expose a free-form fields object")
	}
	// Inline typed properties are capped; the rest collapse into `fields`.
	const maxTyped = skillToolMaxTypedFields + 4 // action + target_ref + confirmed + fields
	if len(props) > maxTyped {
		t.Fatalf("too many typed properties (%d); large field sets must collapse", len(props))
	}
	raw, _ := json.Marshal(strategyTool.Function.Parameters)
	if len(raw) > 8000 {
		t.Fatalf("strategy tool schema too large: %d bytes", len(raw))
	}
}

func TestSkillDomainToolListForPrompt(t *testing.T) {
	list := skillDomainToolList("zh")
	for _, expected := range []string{"skill_exchange_management", "skill_model_management", "skill_strategy_management", "skill_trader_management"} {
		if !strings.Contains(list, expected) {
			t.Fatalf("prompt tool list must mention %q, got %q", expected, list)
		}
	}
}

// --- Prompt consumes all 9 skill JSONs --------------------------------------

func TestSkillCatalogConsumesAllSkillJSONs(t *testing.T) {
	prompt := skillCatalogPrompt("zh")
	toolNames := toolNamesForTest(skillDomainTools())
	for _, name := range listSkillNames() {
		if strings.HasSuffix(name, "_management") {
			// Management skills are consumed through their converged tool
			// (schema + description come from the JSON), not repeated in the
			// catalog.
			if !containsString(toolNames, skillDomainToolName(name)) {
				t.Fatalf("management skill %q must be consumed as a converged tool", name)
			}
			if strings.Contains(prompt, skillDomainToolName(name)) {
				t.Fatalf("management skill %q must not be repeated in the skill catalog", name)
			}
			continue
		}
		if !strings.Contains(prompt, name) {
			t.Fatalf("skill catalog must consume skill %q", name)
		}
	}
}

// The JSON validation rules must reach the tool description so hard
// constraints are known before the call.
func TestSkillDomainToolCarriesValidationRules(t *testing.T) {
	for _, tool := range skillDomainTools() {
		if tool.Function.Name != "skill_model_management" {
			continue
		}
		desc := tool.Function.Description
		if !strings.Contains(desc, "HTTPS") {
			t.Fatalf("model skill description must carry the HTTPS validation rule, got %q", desc)
		}
		if !strings.Contains(desc, "provider 必须是支持列表之一") {
			t.Fatalf("model skill description must carry the provider validation rule, got %q", desc)
		}
		return
	}
	t.Fatal("skill_model_management tool missing")
}

// --- Dispatch ---------------------------------------------------------------

func TestHandleSkillDomainToolCallInvalidArguments(t *testing.T) {
	a := New(nil, nil, DefaultConfig(), slog.Default())

	if got := a.handleSkillDomainToolCall(t.Context(), "default", 1, "zh", toolCall("c1", "skill_model_management", "{not json")); !strings.Contains(got, "error") {
		t.Fatalf("invalid JSON args must error, got %q", got)
	}
	if got := a.handleSkillDomainToolCall(t.Context(), "default", 1, "zh", toolCall("c1", "skill_model_management", "{}")); !strings.Contains(got, "action is required") {
		t.Fatalf("missing action must error, got %q", got)
	}
	if got := a.handleSkillDomainToolCall(t.Context(), "default", 1, "zh", toolCall("c1", "skill_not_a_skill", `{"action":"create"}`)); !strings.Contains(got, "unknown skill tool") {
		t.Fatalf("unknown skill must error, got %q", got)
	}
	// Actions not defined in the skill JSON must be rejected explicitly
	// instead of silently falling through to a legacy handler.
	got := a.handleSkillDomainToolCall(t.Context(), "default", 1, "zh", toolCall("c1", "skill_model_management", `{"action":"fly_to_moon"}`))
	if !strings.Contains(got, "unsupported action") {
		t.Fatalf("undefined action must be rejected, got %q", got)
	}
	if !strings.Contains(got, "query_list") {
		t.Fatalf("rejection must list allowed actions, got %q", got)
	}
}

func TestHandleSkillDomainToolCallQueryList(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "skill-domain-tools.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := st.AIModel().UpdateWithName("default", "default_openai", "OpenAI", true, "sk-test", "", "gpt-5.2"); err != nil {
		t.Fatalf("create model: %v", err)
	}
	a := New(nil, st, DefaultConfig(), slog.Default())

	got := a.handleSkillDomainToolCall(t.Context(), "default", 1, "zh", toolCall("c1", "skill_model_management", `{"action":"query_list"}`))
	if !strings.Contains(got, "OpenAI") || !strings.Contains(got, "gpt-5.2") {
		t.Fatalf("query_list should return model configs, got %q", got)
	}
}

// Business fields must be accepted both flat and nested under `fields`.
func TestHandleSkillDomainToolCallAcceptsFlatAndNestedFields(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "skill-fields.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := st.AIModel().UpdateWithName("default", "default_openai", "OpenAI", true, "sk-test", "", "gpt-5.2"); err != nil {
		t.Fatalf("create model: %v", err)
	}
	a := New(nil, st, DefaultConfig(), slog.Default())

	enabledOf := func() bool {
		models, listErr := st.AIModel().List("default")
		if listErr != nil || len(models) == 0 {
			t.Fatalf("list models: %v", listErr)
		}
		return models[0].Enabled
	}

	// Nested: target_ref + fields.enabled
	a.handleSkillDomainToolCall(t.Context(), "default", 1, "zh", toolCall("c1", "skill_model_management",
		`{"action":"update_status","target_ref":"default_openai","fields":{"enabled":false}}`))
	if enabledOf() {
		t.Fatal("nested fields.enabled must reach the skill handler")
	}

	// Flat: model_id + enabled
	a.handleSkillDomainToolCall(t.Context(), "default", 1, "zh", toolCall("c2", "skill_model_management",
		`{"action":"update_status","model_id":"default_openai","enabled":true}`))
	if !enabledOf() {
		t.Fatal("flat fields must reach the skill handler")
	}
}
