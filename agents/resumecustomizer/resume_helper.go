package resumecustomizer

import (
	"encoding/json"
	"fmt"
	"strings"
)

func extractResumeNodeQuery(payload map[string]any) string {
	// In workflow nodes, `query` may be overwritten by node PreInput.
	// Prefer `text`/`input` to preserve original user message with [upload]/[warning]/[content].
	for _, key := range []string{"text", "input", "query"} {
		if v := strings.TrimSpace(fmt.Sprint(payload[key])); v != "" && v != "<nil>" {
			return extractCurrentQuestionSection(v)
		}
	}
	return ""
}

func extractCurrentQuestionSection(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	const marker = "=== 当前问题 ==="
	if idx := strings.LastIndex(s, marker); idx >= 0 {
		part := strings.TrimSpace(s[idx+len(marker):])
		if part != "" {
			return part
		}
	}
	return s
}

func buildResumeDirectOutput(query string) string {
	contents, warnings := extractUploadContentsAndWarnings(query)
	contents = uniqueStringsKeepOrder(contents)
	if len(warnings) > 0 {
		return "Detected upload/extraction issues:\n- " + strings.Join(uniqueStringsKeepOrder(warnings), "\n- ")
	}
	if len(contents) == 0 {
		return ""
	}
	return "Extracted file content (LLM skipped):\n\n" + strings.Join(contents, "\n\n---\n\n")
}

func extractUploadContentsAndWarnings(query string) ([]string, []string) {
	lines := strings.Split(strings.ReplaceAll(query, "\r\n", "\n"), "\n")
	contents := make([]string, 0, 2)
	warnings := make([]string, 0, 2)
	var contentBuf []string
	inContent := false
	flushContent := func() {
		if len(contentBuf) == 0 {
			return
		}
		block := strings.TrimSpace(strings.Join(contentBuf, "\n"))
		if block != "" {
			contents = append(contents, block)
		}
		contentBuf = contentBuf[:0]
	}

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "[content]"):
			flushContent()
			inContent = true
			rest := strings.TrimSpace(strings.TrimPrefix(line, "[content]"))
			if rest != "" {
				contentBuf = append(contentBuf, rest)
			}
			continue
		case strings.HasPrefix(line, "[warning]"):
			flushContent()
			inContent = false
			w := strings.TrimSpace(strings.TrimPrefix(line, "[warning]"))
			if w != "" {
				warnings = append(warnings, w)
			}
			continue
		case strings.HasPrefix(line, "["):
			flushContent()
			inContent = false
			continue
		}

		if inContent {
			if looksLikeUploadFileHeader(line) {
				flushContent()
				inContent = false
				continue
			}
			contentBuf = append(contentBuf, raw)
		}
	}
	flushContent()
	return contents, uniqueStringsKeepOrder(warnings)
}

func uniqueStringsKeepOrder(items []string) []string {
	if len(items) == 0 {
		return items
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		v := strings.TrimSpace(item)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func looksLikeUploadFileHeader(line string) bool {
	if line == "" {
		return false
	}
	if !strings.Contains(line, "(") || !strings.Contains(line, "bytes)") {
		return false
	}
	if strings.HasPrefix(line, "[") {
		return false
	}
	return true
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

func buildAnalyzePrompt(query string) string {
	return "你是简历诊断助手。请基于用户提供的简历和目标岗位信息，输出结构化诊断：\n1) 目标岗位关键词\n2) 现有经历匹配点\n3) 缺失项\n4) 可改写建议\n\n用户输入：\n" + query
}

func buildTailorPrompt(query string) string {
	return "你是简历定制助手。请基于输入内容产出一版可直接使用的中文简历草稿，必须包含：个人摘要、核心技能、项目经历（STAR）、工作经历（量化成果）、教育背景、可选自荐语。要求真实、具体、避免夸大。\n\n输入内容：\n" + query
}

func fallbackResumeOutput(query string) string {
	return "已接收你的简历定制请求。当前模型调用失败，请稍后重试。你也可以补充目标岗位JD、工作年限和希望突出项目，以便生成更精准版本。\n\n输入摘要：\n" + strings.TrimSpace(query)
}
