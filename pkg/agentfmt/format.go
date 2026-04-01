package agentfmt

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	stepTokenRE = regexp.MustCompile(`\[\]\(step://[^)]+\)`)
	controlRE   = regexp.MustCompile(`[\x00-\x08\x0B\x0C\x0E-\x1F\x7F]`)
	multiNLRE   = regexp.MustCompile(`\n{3,}`)
)

var noisySymbols = []string{
	"", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "",
}

// Clean removes step tokens, control chars and common garbled symbols.
func Clean(raw string) string {
	out := strings.ReplaceAll(raw, "\r\n", "\n")
	out = strings.ReplaceAll(out, "\r", "\n")
	out = stepTokenRE.ReplaceAllString(out, "")
	out = controlRE.ReplaceAllString(out, "")
	for _, sym := range noisySymbols {
		out = strings.ReplaceAll(out, sym, "")
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return ""
	}
	lines := strings.Split(out, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	out = strings.TrimSpace(strings.Join(lines, "\n"))
	out = multiNLRE.ReplaceAllString(out, "\n\n")
	return strings.TrimSpace(out)
}

// Beautify returns a clean, readable and structured markdown answer.
func Beautify(agentName, query, raw string) string {
	cleaned := Clean(raw)
	if cleaned == "" {
		if strings.TrimSpace(query) == "" {
			return "暂未生成可展示内容，请重试。"
		}
		return "暂未生成可展示内容。\n\n## 需求\n" + strings.TrimSpace(query)
	}

	if isStructured(cleaned) {
		return cleaned
	}

	parts := splitParagraphs(cleaned)
	core := parts[0]
	details := ""
	if len(parts) > 1 {
		details = strings.Join(parts[1:], "\n\n")
	}
	if details == "" {
		details = core
	}

	title := titleByAgent(agentName)
	return strings.TrimSpace(fmt.Sprintf("## %s\n%s\n\n## 详细说明\n%s", title, core, details))
}

func titleByAgent(agentName string) string {
	switch strings.ToLower(strings.TrimSpace(agentName)) {
	case "deepresearch":
		return "调研结论"
	case "lbshelper":
		return "行程建议"
	case "urlreader":
		return "网页解读"
	case "host":
		return "回复"
	default:
		return "结果"
	}
}

func isStructured(s string) bool {
	if strings.Contains(s, "\n#") || strings.HasPrefix(s, "#") {
		return true
	}
	if strings.Contains(s, "TL;DR") || strings.Contains(s, "核心要点") {
		return true
	}
	if strings.Contains(s, "Sources:") || strings.Contains(s, "检索过程：") {
		return true
	}
	return false
}

func splitParagraphs(s string) []string {
	rawParts := strings.Split(s, "\n\n")
	parts := make([]string, 0, len(rawParts))
	for _, p := range rawParts {
		v := strings.TrimSpace(p)
		if v == "" {
			continue
		}
		parts = append(parts, v)
	}
	if len(parts) == 0 {
		return []string{s}
	}
	return parts
}
