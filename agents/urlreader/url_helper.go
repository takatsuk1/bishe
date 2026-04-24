package urlreader

import (
	"encoding/json"
	"fmt"
	"strings"
)

func firstURL(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if m := urlRegex.FindString(text); m != "" {
		m = strings.TrimSpace(m)
		m = strings.TrimRight(m, "])}>,.;:!?。；：！？")
		return m
	}
	return ""
}

func buildExtractURLPrompt(userQuery string) string {
	prompt := strings.Builder{}
	prompt.WriteString("你是 URL 提取助手。\n")
	prompt.WriteString("任务: 从用户问题中提取一个最相关的 URL 链接。\n")
	prompt.WriteString("输出要求: 仅输出一个完整 URL（以 http:// 或 https:// 开头），不要输出任何其他内容。\n")
	prompt.WriteString("如果有多个链接，输出最适合回答用户问题的一个。\n")
	prompt.WriteString("用户问题:\n")
	prompt.WriteString(userQuery)
	return prompt.String()
}

func buildSummaryPrompt(toolOutput string) string {
	prompt := strings.Builder{}
	prompt.WriteString("你是网页内容整理助手。\n")
	prompt.WriteString("请基于以下 fetch 返回内容进行结构化整理，给出关键结论、要点摘要与可执行建议。\n")
	prompt.WriteString("如果内容不足或异常，请明确指出。\n\n")
	prompt.WriteString("fetch 返回内容:\n")
	prompt.WriteString(toolOutput)
	return prompt.String()
}

func extractOriginalQuestion(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	for _, key := range []string{"input", "text", "query"} {
		raw := strings.TrimSpace(fmt.Sprint(payload[key]))
		if raw == "" || raw == "<nil>" {
			continue
		}
		if q := extractCurrentQuestionSection(raw); q != "" {
			return q
		}
		return raw
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
			if q := extractCurrentQuestionSection(raw); q != "" {
				return q
			}
			return raw
		}
	}
	return ""
}

func extractCurrentQuestionSection(in string) string {
	s := strings.TrimSpace(urlreaderStepRe.ReplaceAllString(in, " "))
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
	return s
}

func extractURLCandidateFromPayload(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	candidates := []string{
		strings.TrimSpace(fmt.Sprint(payload["input"])),
		strings.TrimSpace(fmt.Sprint(payload["text"])),
		strings.TrimSpace(fmt.Sprint(payload["query"])),
	}
	for _, c := range candidates {
		if u := firstURL(extractCurrentQuestionSection(c)); u != "" {
			return u
		}
	}
	if history, ok := payload["history_outputs"].([]any); ok {
		for _, item := range history {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			out, ok := m["output"].(map[string]any)
			if !ok {
				continue
			}
			if strings.TrimSpace(fmt.Sprint(m["node_id"])) != "__input__" {
				continue
			}
			if u := firstURL(extractCurrentQuestionSection(strings.TrimSpace(fmt.Sprint(out["query"])))); u != "" {
				return u
			}
		}
	}
	return ""
}

func buildSummaryInput(payload map[string]any, fallback string) string {
	if payload != nil {
		if fetchOut, ok := payload["N_fetch"].(map[string]any); ok {
			if s := strings.TrimSpace(fmt.Sprint(fetchOut["response"])); s != "" && s != "<nil>" {
				return s
			}
			if b, err := json.Marshal(fetchOut["result"]); err == nil {
				if s := strings.TrimSpace(string(b)); s != "" {
					return s
				}
			}
		}
	}
	return strings.TrimSpace(fallback)
}
