package agent

import (
	"fmt"
	"sort"
	"strings"
)

// skillCatalogPrompt renders the prompt-level skill knowledge. It is generated
// from the embedded skill JSON definitions so every skill file is consumed by
// the agent:
//   - management skills surface as converged domain-entry tools
//     (skill_*_management, see skill_domain_tools.go) and are pointed at from
//     the prompt's tool section, so they are not repeated here;
//   - diagnosis/execution skills keep their prompt-level knowledge
//     (capabilities, dynamic rules) rendered from the JSON.
func skillCatalogPrompt(lang string) string {
	var sb strings.Builder
	if lang == "zh" {
		sb.WriteString(`## 多轮与 Skill-First 工作模式
- 对于高频已知任务，优先按 skill 执行，不要每次从零规划
- 如果用户仍在同一任务里，继续当前 flow，不要重新路由
- 只追问继续执行所需的最少必要字段，不要让用户重复已确认信息
- 高风险动作（删除、启动实盘、停止运行中 trader、覆盖关键配置）必须单独确认
- 对诊断类问题，优先做“问题归类 -> 可能原因 -> 核查项 -> 下一步建议”

`)
	} else {
		sb.WriteString(`## Multi-turn and Skill-First Operating Mode
- For high-frequency known tasks, prefer stable skills instead of replanning from scratch
- If the user is still in the same task, continue the active flow
- Ask only for the minimum missing fields required to proceed
- Require explicit confirmation for destructive or financially sensitive actions
- For diagnostic requests, use: issue class -> likely causes -> checks -> next steps

`)
	}

	sb.WriteString(catalogLinesForKnowledgeSkills(lang))

	if lang == "zh" {
		sb.WriteString(`

## 回答格式要求
- 诊断类问题尽量按“现象 / 原因 / 先检查什么 / 怎么修复”回答
- 配置指导类问题尽量按步骤回答
- 如果已有工具能验证当前状态，先查再下结论
- 如果结论是推测，必须明确说是“更可能”或“优先怀疑”`)
	} else {
		sb.WriteString(`

Response style:
- Diagnostics: symptom -> cause -> checks -> fix
- Setup guidance: step-by-step
- Verify with tools when possible before concluding`)
	}
	return sb.String()
}

func catalogLinesForKnowledgeSkills(lang string) string {
	names := make([]string, 0, len(skillRegistry))
	for name, def := range skillRegistry {
		if strings.EqualFold(def.Kind, "management") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	var header string
	if lang == "zh" {
		header = "## 诊断与执行技能（按这些知识回答与执行）"
	} else {
		header = "## Diagnosis & Execution Skills (answer and act using this knowledge)"
	}
	lines := make([]string, 0, len(names)*3+1)
	lines = append(lines, header)
	for _, name := range names {
		def := skillRegistry[name]
		var parts []string
		if desc := strings.TrimSpace(def.Description); desc != "" {
			parts = append(parts, desc)
		}
		if len(def.Capabilities) > 0 {
			if lang == "zh" {
				parts = append(parts, "能力: "+strings.Join(def.Capabilities, "；"))
			} else {
				parts = append(parts, "capabilities: "+strings.Join(def.Capabilities, "; "))
			}
		}
		if len(def.DynamicRules) > 0 {
			if lang == "zh" {
				parts = append(parts, "关键规则: "+strings.Join(def.DynamicRules, " "))
			} else {
				parts = append(parts, "key rules: "+strings.Join(def.DynamicRules, " "))
			}
		}
		if len(parts) == 0 {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s: %s", name, strings.Join(parts, " ")))
	}
	return strings.Join(lines, "\n")
}
