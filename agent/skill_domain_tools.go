package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"nofx/mcp"
)

// Skill-domain entry tools.
//
// The native function-calling loop (agentic V2) no longer exposes the flat
// management pairs (get_/manage_exchange_config, get_/manage_model_config,
// get_/manage_strategy, manage_trader). Instead it exposes ONE converged tool
// per management skill, generated from the embedded skill JSON definitions
// (agent/skills/*_management.json): the action enum, slot guidance, and
// description all come straight from the JSON, and execution routes through
// the existing legacy skill machinery (executeAtomicSkillTaskWithSession),
// which already consumes those same JSONs for slot validation, DAG steps, and
// confirmation handling.

const (
	skillDomainToolPrefix = "skill_"

	// skillToolMaxTypedFields caps how many field constraints get inlined as
	// typed JSON-Schema properties. Larger skills (strategy_management has
	// ~70) collapse their remaining fields into a free-form `fields` object
	// to keep the tool surface small.
	skillToolMaxTypedFields = 14
)

var cachedSkillDomainTools = buildSkillDomainTools()

func skillDomainTools() []mcp.Tool { return cachedSkillDomainTools }

func skillDomainToolName(skillName string) string {
	return skillDomainToolPrefix + strings.TrimSpace(skillName)
}

// managementSkillDefinitions returns the embedded skill definitions that
// surface as converged domain-entry tools, sorted by name. The result is
// cached and must be treated as read-only.
func managementSkillDefinitions() []SkillDefinition {
	return cachedManagementSkillDefinitions
}

var cachedManagementSkillDefinitions = loadManagementSkillDefinitions()

func loadManagementSkillDefinitions() []SkillDefinition {
	names := make([]string, 0, len(skillRegistry))
	for name, def := range skillRegistry {
		if strings.EqualFold(def.Kind, "management") && len(def.Actions) > 0 {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	defs := make([]SkillDefinition, 0, len(names))
	for _, name := range names {
		defs = append(defs, skillRegistry[name])
	}
	return defs
}

func buildSkillDomainTools() []mcp.Tool {
	defs := managementSkillDefinitions()
	tools := make([]mcp.Tool, 0, len(defs))
	for _, def := range defs {
		tools = append(tools, skillDomainToolForSkill(def))
	}
	return tools
}

func skillDomainActionNames(def SkillDefinition) []string {
	names := make([]string, 0, len(def.Actions))
	for name := range def.Actions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func skillDomainActionLabel(def SkillDefinition, action string) string {
	actionDef, ok := def.Actions[action]
	if !ok {
		return action
	}
	goal := strings.TrimSpace(actionDef.Goal)
	if goal == "" {
		goal = strings.TrimSpace(actionDef.Description)
	}
	label := action
	if goal != "" {
		label += " — " + goal
	}
	if len(actionDef.RequiredSlots) > 0 {
		label += fmt.Sprintf("（必填: %s）", strings.Join(actionDef.RequiredSlots, ", "))
	}
	if actionDef.NeedsConfirmation {
		label += "（需用户确认）"
	}
	return label
}

func skillDomainActionLines(def SkillDefinition) string {
	lines := make([]string, 0, len(def.Actions)+2)
	lines = append(lines, "可用动作（action 必须取下列枚举之一）：")
	for _, action := range skillDomainActionNames(def) {
		lines = append(lines, "- "+skillDomainActionLabel(def, action))
	}
	return strings.Join(lines, "\n")
}

// skillValidationRuleLines renders the skill's JSON validation rules (capped)
// so the model knows the hard constraints before calling — e.g. HTTPS-only
// endpoints, required credentials, irreversible deletes.
func skillValidationRuleLines(def SkillDefinition) string {
	rules := def.ValidationRules
	if len(rules) == 0 {
		return ""
	}
	const maxRules = 8
	if len(rules) > maxRules {
		rules = rules[:maxRules]
	}
	lines := make([]string, 0, len(rules))
	for _, rule := range rules {
		lines = append(lines, "- "+rule)
	}
	return strings.Join(lines, "\n")
}

func skillDomainFieldProperties(def SkillDefinition) (map[string]any, bool) {
	ordered := make([]string, 0, len(def.FieldConstraints))
	seen := make(map[string]bool, len(def.FieldConstraints))
	appendFields := func(fields []string) {
		for _, field := range fields {
			if seen[field] {
				continue
			}
			seen[field] = true
			ordered = append(ordered, field)
		}
	}
	// The create action's slots are the most commonly used → type them first.
	if create, ok := def.Actions["create"]; ok {
		appendFields(orderedSkillFieldKeys(def, create, true))
	}
	appendFields(orderedSkillFieldKeys(def, SkillActionDefinition{}, false))

	compact := len(ordered) > skillToolMaxTypedFields
	if compact {
		ordered = ordered[:skillToolMaxTypedFields]
	}
	props := make(map[string]any, len(ordered))
	for _, field := range ordered {
		constraint, ok := def.FieldConstraints[field]
		if !ok {
			continue
		}
		props[field] = skillFieldSchema(constraint)
	}
	return props, compact
}

func skillFieldSchema(constraint SkillFieldConstraint) map[string]any {
	schema := map[string]any{}
	switch constraint.Type {
	case "bool":
		schema["type"] = "boolean"
	case "number", "int", "float":
		schema["type"] = "number"
		if constraint.Min != nil {
			schema["minimum"] = *constraint.Min
		}
		if constraint.Max != nil {
			schema["maximum"] = *constraint.Max
		}
	default:
		schema["type"] = "string"
		if len(constraint.Values) > 0 {
			schema["enum"] = constraint.Values
		}
	}
	description := strings.TrimSpace(constraint.Description)
	if constraint.Type == "credential" {
		description = strings.TrimSpace(description + " 敏感凭证，只在用户提供时传入。")
	}
	if description != "" {
		schema["description"] = description
	}
	return schema
}

func skillDomainToolForSkill(def SkillDefinition) mcp.Tool {
	description := strings.TrimSpace(def.Description)
	if lines := skillDomainActionLines(def); lines != "" {
		description += "\n\n" + lines
	}
	if rules := skillValidationRuleLines(def); rules != "" {
		description += "\n\n关键校验规则：\n" + rules
	}

	properties := map[string]any{
		"action": map[string]any{
			"type":        "string",
			"enum":        skillDomainActionNames(def),
			"description": "要执行的动作。",
		},
		"target_ref": map[string]any{
			"type":        "string",
			"description": "要操作的对象 ID 或名称（来自 query_list 结果）；create/query_list 等不需要目标对象的动作可省略。",
		},
		"confirmed": map[string]any{
			"type":        "boolean",
			"description": "高危动作（如 delete）必须先向用户确认后传 true。",
		},
	}
	fields, compact := skillDomainFieldProperties(def)
	for name, schema := range fields {
		properties[name] = schema
	}
	if compact {
		properties["fields"] = map[string]any{
			"type":        "object",
			"description": "其他补充字段的键值对（字段名以技能定义的 field_constraints 为准）。",
		}
	}

	return mcp.Tool{
		Type: "function",
		Function: mcp.FunctionDef{
			Name:        skillDomainToolName(def.Name),
			Description: description,
			Parameters: map[string]any{
				"type":       "object",
				"properties": properties,
				"required":   []string{"action"},
			},
		},
	}
}

// skillDomainToolList renders one line per management skill for the system
// prompt's tool section. The action list lives in each tool's own schema, so
// this stays at one summary line to avoid repeating it across the prompt.
func skillDomainToolList(lang string) string {
	lines := make([]string, 0, len(cachedManagementSkillDefinitions))
	for _, def := range cachedManagementSkillDefinitions {
		summary := firstSentence(def.Description)
		if lang == "zh" {
			lines = append(lines, fmt.Sprintf("- **%s** — %s域管理入口：%s", skillDomainToolName(def.Name), def.Domain, summary))
		} else {
			lines = append(lines, fmt.Sprintf("- **%s** — %s domain management entry: %s", skillDomainToolName(def.Name), def.Domain, summary))
		}
	}
	return strings.Join(lines, "\n")
}

// firstSentence returns the leading sentence of a skill description, so the
// prompt keeps a short pointer instead of the full text already present in the
// tool schema.
func firstSentence(text string) string {
	text = strings.TrimSpace(text)
	for _, sep := range []string{"。", ". "} {
		if idx := strings.Index(text, sep); idx > 0 {
			return strings.TrimSpace(text[:idx+len(sep)])
		}
	}
	return text
}

// skillTargetRefAlias maps a skill to the domain-specific id field callers may
// use instead of the canonical target_ref.
func skillTargetRefAlias(skillName string) string {
	switch strings.TrimSpace(skillName) {
	case "exchange_management":
		return "exchange_id"
	case "model_management":
		return "model_id"
	case "strategy_management":
		return "strategy_id"
	case "trader_management":
		return "trader_id"
	default:
		return ""
	}
}

// handleSkillDomainToolCall routes a converged skill-domain tool call into the
// legacy skill machinery, which owns slot validation, defaults, DAG steps, and
// confirmation flows driven by the same skill JSON definitions.
func (a *Agent) handleSkillDomainToolCall(ctx context.Context, storeUserID string, userID int64, lang string, tc mcp.ToolCall) string {
	skillName := strings.TrimPrefix(tc.Function.Name, skillDomainToolPrefix)
	def, ok := getSkillDefinition(skillName)
	if !ok || !strings.EqualFold(def.Kind, "management") {
		return fmt.Sprintf(`{"error":"unknown skill tool: %s"}`, tc.Function.Name)
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &raw); err != nil {
		return fmt.Sprintf(`{"error":"invalid arguments for %s: %v"}`, tc.Function.Name, err)
	}
	action, _ := raw["action"].(string)
	action = strings.TrimSpace(action)
	if action == "" {
		return fmt.Sprintf(`{"error":"action is required for %s","allowed_actions":%s}`, tc.Function.Name, mustMarshalJSON(skillDomainActionNames(def)))
	}
	// Reject actions the skill JSON does not define: the legacy handlers below
	// would otherwise execute them as if they were valid.
	if _, ok := def.Actions[action]; !ok {
		return fmt.Sprintf(`{"error":"unsupported action %q for %s","allowed_actions":%s}`, action, tc.Function.Name, mustMarshalJSON(skillDomainActionNames(def)))
	}

	session := skillSession{
		Name:   skillName,
		Action: action,
		Phase:  "collecting",
		Fields: map[string]string{},
	}
	ref, _ := raw["target_ref"].(string)
	ref = strings.TrimSpace(ref)
	if ref == "" {
		// Tolerate the domain-specific id field (model_id, trader_id, ...):
		// models often pass it instead of target_ref.
		if alias := skillTargetRefAlias(skillName); alias != "" {
			if v, _ := raw[alias].(string); strings.TrimSpace(v) != "" {
				ref = strings.TrimSpace(v)
			}
		}
	}
	if ref != "" {
		session.Fields["target_ref"] = ref
		session.TargetRef = &EntityReference{ID: ref, Name: ref, Source: "tool_call"}
	}
	if confirmed, _ := raw["confirmed"].(bool); confirmed {
		session.Fields["confirmed"] = "true"
	}
	addField := func(key string, value any) {
		switch key = strings.TrimSpace(key); key {
		case "", "action", "target_ref", "confirmed", "fields":
			return
		}
		if v := skillArgToString(value); v != "" {
			session.Fields[normalizeFieldKey(&session, key)] = v
		}
	}
	// The LLM may nest business fields under `fields` or pass them flat.
	for key, value := range raw {
		addField(key, value)
	}
	if nested, _ := raw["fields"].(map[string]any); nested != nil {
		for key, value := range nested {
			addField(key, value)
		}
	}

	answer, handled := a.executeAtomicSkillTaskWithSession(storeUserID, userID, lang, "", session, nil)
	if !handled {
		return fmt.Sprintf(`{"error":"unsupported skill action","skill":%q,"action":%q}`, skillName, action)
	}
	return answer
}

func skillArgToString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case bool:
		if v {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case json.Number:
		return strings.TrimSpace(v.String())
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(raw)
	}
}
