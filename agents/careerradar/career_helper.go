// package careerradar

// import (
// 	internalproto "ai/pkg/protocol"
// 	internaltm "ai/pkg/taskmanager"
// 	"context"
// 	"encoding/json"
// 	"fmt"
// 	"strings"
// )

// func (a *Agent) emitCareerStepEvent(ctx context.Context, manager internaltm.Manager, taskID string, nodeID string, state internalproto.StepState) {
// 	if manager == nil {
// 		return
// 	}
// 	nodeName := strings.TrimSpace(nodeID)
// 	if nodeName == "" {
// 		nodeName = "unknown"
// 	}
// 	nodeType := strings.TrimSpace(careerRadarNodeTypeText[nodeName])
// 	if nodeType == "" {
// 		nodeType = "unknown"
// 	}
// 	message := fmt.Sprintf("节点名:%s 节点类型:%s", nodeName, nodeType)
// 	ev := internalproto.NewStepEvent("careerradar", "workflow", nodeID, state, message)
// 	text := ""
// 	if token, tokenErr := internalproto.EncodeStepToken(ev); tokenErr == nil {
// 		text = token
// 	} else {
// 		text = message
// 	}
// 	_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateWorking, &internalproto.Message{
// 		Role:  internalproto.MessageRoleAgent,
// 		Parts: []internalproto.Part{internalproto.NewTextPart(text)},
// 	})
// }

// func extractCareerNodeQuery(payload map[string]any) string {
// 	for _, key := range []string{"text", "input", "query"} {
// 		if v := strings.TrimSpace(fmt.Sprint(payload[key])); v != "" && v != "<nil>" {
// 			return v
// 		}
// 	}
// 	return ""
// }

// func getNodeField(payload map[string]any, node, field string) any {
// 	n, ok := payload[node].(map[string]any)
// 	if !ok {
// 		return nil
// 	}
// 	return n[field]
// }

// func snapshotAnyForLog(v any, maxLen int) string {
// 	b, err := json.Marshal(v)
// 	if err != nil {
// 		s := strings.TrimSpace(fmt.Sprint(v))
// 		if maxLen > 0 && len(s) > maxLen {
// 			return s[:maxLen] + "...(truncated)"
// 		}
// 		if s == "" {
// 			return "<empty>"
// 		}
// 		return s
// 	}
// 	s := strings.TrimSpace(string(b))
// 	if maxLen > 0 && len(s) > maxLen {
// 		return s[:maxLen] + "...(truncated)"
// 	}
// 	if s == "" {
// 		return "<empty>"
// 	}
// 	return s
// }

// func truncateText(input string, max int) string {
// 	input = strings.TrimSpace(input)
// 	if max <= 0 || len(input) <= max {
// 		return input
// 	}
// 	return strings.TrimSpace(input[:max]) + "..."
// }

// func buildResearchPrompt(query string) string {
// 	return "你是 deepresearch 的检索任务规划器。请基于用户输入生成用于 DeepResearch 的检索说明，要求：\n" +
// 		"1) 提取岗位关键词（岗位名称、行业/领域、城市、经验年限、薪资区间）并列出可用的搜索关键词；\n" +
// 		"2) 归纳岗位职责与任职要求，标注可能的风险或模糊项（如面议、职责边界不清、加班描述等）；\n" +
// 		"3) 输出结构化中文，便于自动检索与后续人工复核。\n\n用户输入：\n" + strings.TrimSpace(query)
// }

// func buildSummaryPrompt(userQuery, research string) string {
// 	extraRule := ""
// 	if strings.TrimSpace(research) != "" {
// 		extraRule = "\n重要约束：DeepResearch 已返回检索内容，你不得声称“检索结果为空”或“未获取到结果”。"
// 	}
// 	return "你是职场雷达分析助手。请基于 deepresearch 返回的信息，输出结构化中文结果：\n" +
// 		"## 匹配岗位推荐（3-5个）\n每个岗位给出：岗位名、公司/行业、匹配理由、建议投递优先级。\n" +
// 		"## 高风险岗位描述识别\n重点识别并解释：加班文化、薪资描述模糊（如面议/范围过宽/无结构）、职责边界不清、要求不合理。\n" +
// 		"## 求职建议\n给出可执行建议（筛选关键词、面试提问点、避坑策略）。\n\n" +
// 		extraRule + "\n\n" +
// 		"用户意向：\n" + strings.TrimSpace(userQuery) + "\n\nDeepResearch结果：\n" + strings.TrimSpace(research)
// }

// func fallbackSummary(research string) string {
// 	r := strings.TrimSpace(research)
// 	if r == "" {
// 		return "暂未拿到有效研究结果。请补充岗位关键词（岗位方向/城市/薪资区间/经验年限）后重试。"
// 	}
// 	riskHits := make([]string, 0, 4)
// 	lower := strings.ToLower(r)
// 	if strings.Contains(lower, "996") || strings.Contains(lower, "大小周") || strings.Contains(lower, "加班") {
// 		riskHits = append(riskHits, "- 检测到疑似高强度工时描述（如 996/大小周/频繁加班）")
// 	}
// 	if strings.Contains(lower, "面议") || strings.Contains(lower, "薪资可谈") || strings.Contains(lower, "10-50k") {
// 		riskHits = append(riskHits, "- 检测到薪资描述可能模糊（面议/跨度过大）")
// 	}
// 	if len(riskHits) == 0 {
// 		riskHits = append(riskHits, "- 未检测到显著风险关键词，建议继续人工核验 JD 细节")
// 	}
// 	return "## 匹配岗位推荐\n请结合 DeepResearch 内容优先筛选“技能匹配度高 + 薪资范围清晰 + 职责边界明确”的岗位。\n\n" +
// 		"## 高风险岗位描述识别\n" + strings.Join(riskHits, "\n") + "\n\n" +
// 		"## 求职建议\n投递前重点确认：作息制度、薪资构成（固定/绩效/年终）、试用期薪资与转正标准。"
// }
