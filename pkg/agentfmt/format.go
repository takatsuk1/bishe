package agentfmt

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	// stepTokenRE 匹配步骤令牌的正则表达式
	stepTokenRE = regexp.MustCompile(`\[\]\(step://[^)]+\)`)
	// controlRE 匹配控制字符的正则表达式
	controlRE = regexp.MustCompile(`[\x00-\x08\x0B\x0C\x0E-\x1F\x7F]`)
	// multiNLRE 匹配连续三个或更多换行符的正则表达式
	multiNLRE = regexp.MustCompile(`\n{3,}`)
)

// noisySymbols 常见的乱码符号列表
var noisySymbols = []string{
	"", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "",
}

// Clean 清理文本，移除步骤令牌、控制字符和常见的乱码符号
// 参数:
//
//	raw - 原始文本
//
// 返回值:
//
//	清理后的文本
func Clean(raw string) string {
	// 统一换行符格式
	out := strings.ReplaceAll(raw, "\r\n", "\n")
	out = strings.ReplaceAll(out, "\r", "\n")
	// 移除步骤令牌
	out = stepTokenRE.ReplaceAllString(out, "")
	// 移除控制字符
	out = controlRE.ReplaceAllString(out, "")
	// 移除乱码符号
	for _, sym := range noisySymbols {
		out = strings.ReplaceAll(out, sym, "")
	}
	// 去除首尾空白
	out = strings.TrimSpace(out)
	if out == "" {
		return ""
	}
	// 处理每行末尾的空白
	lines := strings.Split(out, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	out = strings.TrimSpace(strings.Join(lines, "\n"))
	// 压缩连续的换行符
	out = multiNLRE.ReplaceAllString(out, "\n\n")
	return strings.TrimSpace(out)
}

// Beautify 返回一个干净、可读和结构化的markdown答案
// 参数:
//
//	agentName - 代理名称
//	query - 查询内容
//	raw - 原始文本
//
// 返回值:
//
//	结构化的markdown文本
func Beautify(agentName, query, raw string) string {
	// 清理原始文本
	cleaned := Clean(raw)
	if cleaned == "" {
		// 处理空结果的情况
		if strings.TrimSpace(query) == "" {
			return "暂未生成可展示内容，请重试。"
		}
		return "暂未生成可展示内容。\n\n## 需求\n" + strings.TrimSpace(query)
	}

	// 检查文本是否已经结构化
	if isStructured(cleaned) {
		return cleaned
	}

	// 分割段落
	parts := splitParagraphs(cleaned)
	core := parts[0]
	details := ""
	if len(parts) > 1 {
		details = strings.Join(parts[1:], "\n\n")
	}
	if details == "" {
		details = core
	}

	// 根据代理名称生成标题
	title := titleByAgent(agentName)
	return strings.TrimSpace(fmt.Sprintf("## %s\n%s\n\n## 详细说明\n%s", title, core, details))
}

// titleByAgent 根据代理名称生成对应的标题
// 参数:
//
//	agentName - 代理名称
//
// 返回值:
//
//	对应的中文标题
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

// isStructured 检查文本是否已经结构化
// 参数:
//
//	s - 要检查的文本
//
// 返回值:
//
//	如果文本已经结构化返回true，否则返回false
func isStructured(s string) bool {
	// 检查是否包含Markdown标题
	if strings.Contains(s, "\n#") || strings.HasPrefix(s, "#") {
		return true
	}
	// 检查是否包含摘要标记
	if strings.Contains(s, "TL;DR") || strings.Contains(s, "核心要点") {
		return true
	}
	// 检查是否包含来源标记
	if strings.Contains(s, "Sources:") || strings.Contains(s, "检索过程：") {
		return true
	}
	return false
}

// splitParagraphs 分割文本为段落
// 参数:
//
//	s - 要分割的文本
//
// 返回值:
//
//	段落数组
func splitParagraphs(s string) []string {
	// 按双换行符分割
	rawParts := strings.Split(s, "\n\n")
	parts := make([]string, 0, len(rawParts))
	// 过滤空段落
	for _, p := range rawParts {
		v := strings.TrimSpace(p)
		if v == "" {
			continue
		}
		parts = append(parts, v)
	}
	// 如果没有段落，返回原始文本
	if len(parts) == 0 {
		return []string{s}
	}
	return parts
}
