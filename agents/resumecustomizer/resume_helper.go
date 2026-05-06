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
	return "你是资深简历诊断顾问。请基于用户提供的原始简历与目标岗位信息，输出一份可直接支撑后续改写的结构化诊断。要求严格基于已提供信息，不要虚构经历。\n\n请按以下格式输出：\n## 岗位目标与判断\n- 目标岗位：\n- 你判断该岗位最看重的3-5项能力：\n\n## 当前简历亮点\n- 已有优势1：价值说明\n- 已有优势2：价值说明\n- 已有优势3：价值说明\n\n## 主要问题\n- 问题1：原句/原内容概括 -> 为什么不利于求职\n- 问题2：原句/原内容概括 -> 为什么不利于求职\n- 问题3：原句/原内容概括 -> 为什么不利于求职\n\n## 优化策略\n- 策略1：应该补强什么，怎么改写\n- 策略2：应该补强什么，怎么改写\n- 策略3：应该补强什么，怎么改写\n\n## 关键词覆盖\n- 已覆盖关键词：\n- 缺失关键词：\n\n用户输入：\n" + query
}

func buildTailorPrompt(query string, analysis string) string {
	analysis = strings.TrimSpace(analysis)
	if analysis == "" {
		analysis = "暂无上一步诊断结果，请你先自行归纳简历问题后再输出。"
	}
	return "你是资深简历优化顾问。请基于原始简历/JD和简历诊断结果，输出一份既能体现优化价值、又能直接交付用户使用的结果。\n\n重要要求：\n1. 必须同时展示为什么这样改和改完长什么样。\n2. 不能虚构未提供的公司、项目、数据、头衔、奖项；如果原简历没有量化数据，只能写成可补充项或保守表达。\n3. 优化说明必须尽量引用原简历中的弱表达，给出对应的优化后表达。\n4. 如果岗位JD不明确，也要先说明你默认按通用求职优化处理。\n\n请严格按以下结构输出：\n# 一、优化价值总览\n- 用3-5条总结这次优化具体提升了什么，例如：从职责罗列改成成果表达、补齐岗位关键词、突出项目价值、压缩无效信息。\n\n# 二、关键优化对照\n请给出至少4条修改前 -> 修改后 -> 价值说明的对照，格式如下：\n1. 修改前：\n   修改后：\n   价值说明：\n\n# 三、定制简历草稿\n请输出一版可直接使用的中文简历草稿，包含：\n- 个人摘要\n- 核心技能\n- 工作/实习经历\n- 项目经历（优先使用STAR表达）\n- 教育背景\n- 可选：补充建议\n\n补充要求：\n- 尽量用招聘方视角改写，让信息更利于筛选。\n- 经历描述优先写动作、结果、影响。\n- 对不确定的数据，用可补充标记，不要编造。\n\n原始输入：\n" + query + "\n\n上一步诊断结果：\n" + analysis
}

func fallbackResumeOutput(query string) string {
	return "已接收你的简历优化请求，但当前模型调用失败，请稍后重试。建议补充目标岗位JD、工作年限、最想突出的一段经历，这样后续不仅能生成优化版简历，也能更清楚展示每一处修改的价值。\n\n输入摘要：\n" + strings.TrimSpace(query)
}

func extractNodeResponse(payload map[string]any, nodeID string) string {
	if payload == nil {
		return ""
	}
	node, ok := payload[nodeID].(map[string]any)
	if !ok {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(node["response"]))
}
