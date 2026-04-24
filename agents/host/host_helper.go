package host

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

func buildRoutePrompt(userQuery string, agentInfo map[string]any) string {
	var sb strings.Builder
	sb.WriteString("你是 Host 路由助手。\n")
	if names := collectAllowedAgents(agentInfo); len(names) > 0 {
		sb.WriteString("当前可调用 agent 列表: ")
		sb.WriteString(strings.Join(names, ", "))
		sb.WriteString("\n")
	}
	sb.WriteString("任务：判断是否需要调用其他 agent。\n")
	sb.WriteString("输出规则:\n")
	sb.WriteString("1. 不需要调用任何 agent 时，只输出 false。\n")
	sb.WriteString("2. 需要调用时，只输出目标 agent 名称。\n")
	sb.WriteString("3. 不要输出解释和额外文本。\n")
	sb.WriteString("用户问题:\n")
	sb.WriteString(userQuery)
	return sb.String()
}

func buildDirectAnswerPrompt(userQuery string, agentInfo map[string]any) string {
	var sb strings.Builder
	sb.WriteString("你是通用中文助手。\n")
	if names := collectAllowedAgents(agentInfo); len(names) > 0 {
		sb.WriteString("你知道当前可调用 agent 列表: ")
		sb.WriteString(strings.Join(names, ", "))
		sb.WriteString("。\n")
	}
	sb.WriteString("如果问题不需要调用其他 agent，请直接回答用户。\n")
	sb.WriteString("用户问题:\n")
	sb.WriteString(userQuery)
	return sb.String()
}

func normalizeRouteDecision(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "false"
	}
	lower := strings.ToLower(s)
	if lower == "false" || strings.Contains(lower, "不需要") || strings.Contains(lower, "无需") || strings.Contains(lower, "不用") {
		return "false"
	}

	if strings.Contains(s, "\n") {
		s = strings.TrimSpace(strings.Split(s, "\n")[0])
	}
	// Keep the sentence for later dynamic matching against allowed agent names.
	s = strings.Trim(s, "\"'` ")
	if s == "" {
		return "false"
	}
	return s
}

func resolveAllowedAgentName(raw string, allowedAgents []string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	lower := strings.ToLower(raw)

	for _, name := range allowedAgents {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(name)) {
			return name
		}
	}

	token := raw
	if i := strings.IndexAny(token, " \t\n,.;:()[]{}\"'`，。；：！？（）【】"); i >= 0 {
		token = strings.TrimSpace(token[:i])
	}
	for _, name := range allowedAgents {
		if strings.EqualFold(token, strings.TrimSpace(name)) {
			return strings.TrimSpace(name)
		}
	}

	return ""
}

func collectAllowedAgents(agentInfoOut map[string]any) []string {
	result := make([]string, 0)
	seen := map[string]struct{}{}

	appendName := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		result = append(result, name)
	}

	agentsRaw, ok := agentInfoOut["agents"]
	if !ok {
		return result
	}

	switch arr := agentsRaw.(type) {
	case []map[string]any:
		for _, item := range arr {
			appendName(fmt.Sprint(item["name"]))
		}
	case []any:
		for _, v := range arr {
			if m, ok := v.(map[string]any); ok {
				appendName(fmt.Sprint(m["name"]))
			}
		}
	default:
		b, err := json.Marshal(arr)
		if err == nil {
			var generic []map[string]any
			if unmarshalErr := json.Unmarshal(b, &generic); unmarshalErr == nil {
				for _, m := range generic {
					appendName(fmt.Sprint(m["name"]))
				}
			}
		}
	}

	return result
}

func stringSliceToAnySlice(in []string) []any {
	out := make([]any, 0, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}

func buildRoutePromptV2(userQuery string, agentInfo map[string]any) string {
	var sb strings.Builder
	sb.WriteString("你是 Host 路由助手。任务：判断是否需要调用其他 agent。\n")
	if names := collectAllowedAgents(agentInfo); len(names) > 0 {
		sb.WriteString("当前可调用 agent 列表: ")
		sb.WriteString(strings.Join(names, ", "))
		sb.WriteString("\n")
	}
	sb.WriteString("输出规则:\n")
	sb.WriteString("1. 如果不需要调用任何 agent，只输出 false。\n")
	sb.WriteString("2. 如果需要调用，只输出目标 agent 名称。\n")
	sb.WriteString("3. 禁止输出解释或额外文本。\n")
	sb.WriteString("用户问题:\n")
	sb.WriteString(userQuery)
	return sb.String()
}

func buildDirectAnswerPromptV2(userQuery string, agentInfo map[string]any) string {
	var sb strings.Builder
	sb.WriteString("你是通用中文助手。\n")
	if names := collectAllowedAgents(agentInfo); len(names) > 0 {
		sb.WriteString("你掌握当前可调用 agent 列表: ")
		sb.WriteString(strings.Join(names, ", "))
		sb.WriteString("。当用户询问可调用哪些 agent 时，请准确回答。\n")
	}
	sb.WriteString("如果问题无需调用其他 agent，请直接给出清晰、简洁、有帮助的回答。\n")
	sb.WriteString("用户问题:\n")
	sb.WriteString(userQuery)
	return sb.String()
}

func extractAgentInfoPayload(payload map[string]any) map[string]any {
	if len(payload) == 0 {
		return map[string]any{}
	}
	node, _ := payload["N_agent_info"].(map[string]any)
	if len(node) == 0 {
		return map[string]any{}
	}
	if result, ok := node["result"].(map[string]any); ok && len(result) > 0 {
		return result
	}
	if agents, ok := node["agents"]; ok {
		return map[string]any{"agents": agents}
	}
	return map[string]any{}
}

var hostStepTokenRe = regexp.MustCompile(`\[\]\(step://[^\)]*\)`)

func extractHostUserQuery(payload map[string]any, fallback string) string {
	if payload != nil {
		for _, key := range []string{"input", "text", "query"} {
			raw := strings.TrimSpace(fmt.Sprint(payload[key]))
			if raw == "" || raw == "<nil>" {
				continue
			}
			if q := extractHostCurrentQuestion(raw); q != "" {
				return q
			}
		}
		if history, ok := payload["history_outputs"].([]any); ok {
			for _, item := range history {
				m, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if strings.TrimSpace(fmt.Sprint(m["node_id"])) != "__input__" {
					continue
				}
				out, ok := m["output"].(map[string]any)
				if !ok {
					continue
				}
				raw := strings.TrimSpace(fmt.Sprint(out["query"]))
				if raw == "" || raw == "<nil>" {
					continue
				}
				if q := extractHostCurrentQuestion(raw); q != "" {
					return q
				}
			}
		}
	}
	return extractHostCurrentQuestion(strings.TrimSpace(fallback))
}

func extractHostCurrentQuestion(in string) string {
	s := strings.TrimSpace(hostStepTokenRe.ReplaceAllString(in, " "))
	if s == "" {
		return ""
	}
	if i := strings.LastIndex(s, "=== 当前问题 ==="); i >= 0 {
		q := strings.TrimSpace(s[i+len("=== 当前问题 ==="):])
		if q != "" {
			return q
		}
	}
	if i := strings.LastIndex(s, "用户:"); i >= 0 {
		q := strings.TrimSpace(s[i+len("用户:"):])
		if q != "" {
			return q
		}
	}
	if strings.Contains(s, "map[") {
		return ""
	}
	return strings.TrimSpace(s)
}

func snapshotAnyForLog(v any, maxLen int) string {
	b, err := json.Marshal(v)
	if err != nil {
		s := strings.TrimSpace(fmt.Sprint(v))
		if maxLen > 0 && len(s) > maxLen {
			return s[:maxLen] + "...(truncated)"
		}
		if s == "" {
			return "<empty>"
		}
		return s
	}
	s := strings.TrimSpace(string(b))
	if maxLen > 0 && len(s) > maxLen {
		return s[:maxLen] + "...(truncated)"
	}
	if s == "" {
		return "<empty>"
	}
	return s
}

func truncateText(input string, max int) string {
	input = strings.TrimSpace(input)
	if max <= 0 || len(input) <= max {
		return input
	}
	return strings.TrimSpace(input[:max]) + "..."
}
