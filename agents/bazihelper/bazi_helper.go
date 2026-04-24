package bazihelper

import (
	"ai/pkg/logger"
	"ai/pkg/tools"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

func (a *Agent) callBaziTool(ctx context.Context, taskID string, query string, payload map[string]any) (map[string]any, error) {
	userQuery := extractBaziUserQuery(payload, query)
	planRaw := ""
	if extractOut, ok := payload["N_extract"].(map[string]any); ok {
		planRaw = strings.TrimSpace(fmt.Sprint(extractOut["response"]))
	}
	logger.Infof("[DEBUG][bazihelper] callBaziTool task_id=%s workflow_query_len=%d user_query_len=%d user_query_preview=%q plan_raw_len=%d plan_raw_preview=%q",
		taskID, len(query), len(userQuery), truncateText(userQuery, 240), len(planRaw), truncateText(planRaw, 600))
	plan := extractBaziToolPlan(planRaw, userQuery)
	if planJSON, err := json.Marshal(plan); err == nil {
		logger.Infof("[DEBUG][bazihelper] callBaziTool parsed_plan task_id=%s plan_json=%s", taskID, truncateText(string(planJSON), 4000))
	} else {
		logger.Infof("[DEBUG][bazihelper] callBaziTool parsed_plan task_id=%s marshal_err=%v", taskID, err)
	}
	if len(plan.Calls) == 0 {
		return nil, fmt.Errorf("no valid bazi tool plan generated")
	}

	tool, err := a.findToolByName("bazi")
	if err != nil {
		return nil, err
	}

	callResults := make([]map[string]any, 0, len(plan.Calls))
	for idx, call := range plan.Calls {
		normArgs := normalizeBaziArguments(call.ToolName, call.Arguments, userQuery)
		params := map[string]any{
			"tool_name": call.ToolName,
			"arguments": normArgs,
			"query":     userQuery,
			"task_id":   taskID,
		}
		argType := fmt.Sprintf("%T", params["arguments"])
		argsJSON, ajErr := json.Marshal(normArgs)
		if ajErr != nil {
			logger.Infof("[DEBUG][bazihelper] MCP call prep task_id=%s idx=%d tool=%s arguments_type=%s arguments_marshal_err=%v",
				taskID, idx+1, call.ToolName, argType, ajErr)
		} else {
			logger.Infof("[DEBUG][bazihelper] MCP call prep task_id=%s idx=%d tool=%s arguments_type=%s arguments_json=%s reason=%q",
				taskID, idx+1, call.ToolName, argType, truncateText(string(argsJSON), 1200), truncateText(call.Reason, 200))
		}
		out, execErr := tool.Execute(ctx, params)
		if execErr != nil {
			logger.Infof("[DEBUG][bazihelper] MCP call err task_id=%s idx=%d tool=%s err=%v", taskID, idx+1, call.ToolName, execErr)
			callResults = append(callResults, map[string]any{
				"index":     idx + 1,
				"tool_name": call.ToolName,
				"arguments": params["arguments"],
				"error":     execErr.Error(),
			})
			continue
		}
		isErr, _ := out["is_error"].(bool)
		logger.Infof("[DEBUG][bazihelper] MCP call ok task_id=%s idx=%d tool=%s is_error=%v out_keys=%s",
			taskID, idx+1, call.ToolName, isErr, baziDebugOutKeys(out))
		callResults = append(callResults, map[string]any{
			"index":     idx + 1,
			"tool_name": call.ToolName,
			"arguments": params["arguments"],
			"output":    out,
		})
	}

	result := map[string]any{
		"plan":             plan,
		"calls":            callResults,
		"normalized_query": plan.NormalizedQuery,
		"summary_focus":    plan.SummaryFocus,
		"assumptions":      plan.Assumptions,
	}
	b, _ := json.Marshal(result)
	logger.Infof("[DEBUG][bazihelper] callBaziTool done task_id=%s calls=%d aggregate_json_len=%d", taskID, len(callResults), len(b))
	return map[string]any{"response": string(b), "result": result}, nil
}

func (a *Agent) findToolByName(name string) (tools.Tool, error) {
	switch strings.TrimSpace(name) {
	case "bazi":
		if client, ok := tools.GetStdioMCPManager().Get(tools.BaziMCPToolID); ok {
			return tools.WrapBaziStdioMCPClient(client), nil
		}
		if a.BaziTool == nil {
			return nil, fmt.Errorf("tool bazi is not initialized")
		}
		return a.BaziTool, nil
	default:
		return nil, fmt.Errorf("tool %s not found", name)
	}
}

func buildExtractPlanPrompt(userQuery string, toolCatalog string) string {
	var sb strings.Builder
	sb.WriteString("你是八字命理助手的工具规划器。\n")
	sb.WriteString(toolCatalog)
	sb.WriteString("\n")
	sb.WriteString("请根据用户问题，抽取出生信息、问题焦点，并自动决定要调用哪些 Bazi MCP 子工具。\n")
	sb.WriteString("只输出 JSON，不要输出 markdown。\n")
	sb.WriteString("JSON 结构:\n")
	sb.WriteString("{\"normalized_query\":\"...\",\"summary_focus\":\"...\",\"assumptions\":[\"...\"],\"calls\":[{\"tool_name\":\"getBaziDetail|getSolarTimes|getChineseCalendar\",\"arguments\":{...},\"reason\":\"...\"}]}\n")
	sb.WriteString("规则:\n")
	sb.WriteString("1. 如果用户给的是出生时间并想排盘/分析，优先用 getBaziDetail。\n")
	sb.WriteString("2. 如果用户给的是八字并想反推时间，使用 getSolarTimes。\n")
	sb.WriteString("3. 如果用户问黄历、宜忌、择日，使用 getChineseCalendar。\n")
	sb.WriteString("4. getBaziDetail 必须补齐 gender；男=1，女=0；无法确定时默认 1 并在 assumptions 写明。\n")
	sb.WriteString("5. 若用户提供的是公历时间，请尽量转换为 ISO 时间字符串，例如 2008-03-01T13:00:00+08:00。\n")
	sb.WriteString("6. 至少输出 1 个 calls 元素。\n")
	sb.WriteString("7. 每个 calls[].arguments 必须与对应子工具的入参一致，禁止传空对象 {}；缺省字段由你在 JSON 里显式补齐。\n")
	sb.WriteString("8. getChineseCalendar 必须在 arguments 里提供 solarDatetime（RFC3339，含时区，如 2026-04-12T12:00:00+08:00）；用户说「今天/现在」则用你推断的当前日期时刻。\n")
	sb.WriteString("9. getBaziDetail 必须在 arguments 里提供 gender，以及 solarDatetime 与 lunarDatetime 二者之一（推荐 solarDatetime）。\n")
	sb.WriteString("10. getSolarTimes 必须在 arguments 里提供 bazi 字符串（四柱天干地支，字间空格）。\n")
	sb.WriteString("11. 处理相对时间：当用户提到「今天」、「明天」、「后天」、「昨天」、「前天」等相对时间时，请根据当前日期计算出准确的日期，并转换为 ISO 时间字符串。\n")
	sb.WriteString("12. 时间计算规则例：首先要准确获取当前日期，然后根据用户问题计算出目标日期。\n")
	sb.WriteString("当前时间: ")
	sb.WriteString(time.Now().Format("2006-01-02 15:04:05"))
	sb.WriteString("\n")
	sb.WriteString("用户问题:\n")
	sb.WriteString(strings.TrimSpace(userQuery))
	return sb.String()
}

func buildSummaryPrompt(payload map[string]any, fallback string) string {
	var sb strings.Builder
	sb.WriteString("你是八字解读助手。请基于工具返回结果给出清晰、结构化的中文解读。\n")
	sb.WriteString("要求:\n")
	sb.WriteString("1. 先给一句话总结。\n")
	sb.WriteString("2. 再分段输出：基础信息、命盘重点、阶段趋势、建议。\n")
	sb.WriteString("3. 如果存在 assumptions，要明确说明是假设条件。\n")
	sb.WriteString("4. 不要编造不存在的工具结果。\n")
	sb.WriteString("5. 说明仅供文化交流与娱乐参考，不替代专业现实决策。\n\n")
	sb.WriteString("用户问题:\n")
	sb.WriteString(extractBaziUserQuery(payload, fallback))
	sb.WriteString("\n\n工具结果:\n")
	sb.WriteString(extractBaziSummaryInput(payload, fallback))
	return sb.String()
}

func extractUserID(meta map[string]any) string {
	if meta == nil {
		return ""
	}
	for _, key := range []string{"user_id", "userId", "UserID"} {
		if value := strings.TrimSpace(fmt.Sprint(meta[key])); value != "" && value != "<nil>" {
			return value
		}
	}
	return ""
}

var baziStepTokenRe = regexp.MustCompile(`\[\]\(step://[^\)]*\)`)

func extractBaziUserQuery(payload map[string]any, fallback string) string {
	for _, key := range []string{"input", "text", "query"} {
		raw := strings.TrimSpace(fmt.Sprint(payload[key]))
		if raw == "" || raw == "<nil>" {
			continue
		}
		if q := extractCurrentQuestion(raw); q != "" {
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
			if q := extractCurrentQuestion(raw); q != "" {
				return q
			}
		}
	}
	return extractCurrentQuestion(strings.TrimSpace(fallback))
}

func extractCurrentQuestion(in string) string {
	s := strings.TrimSpace(baziStepTokenRe.ReplaceAllString(in, " "))
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

func extractBaziSummaryInput(payload map[string]any, fallback string) string {
	if baziOut, ok := payload["N_bazi"].(map[string]any); ok {
		if raw := strings.TrimSpace(fmt.Sprint(baziOut["response"])); raw != "" && raw != "<nil>" {
			return raw
		}
		if result, ok := baziOut["result"].(map[string]any); ok && len(result) > 0 {
			if data, err := json.Marshal(result); err == nil {
				return strings.TrimSpace(string(data))
			}
		}
	}
	return strings.TrimSpace(fallback)
}

func extractBaziToolPlan(raw string, userQuery string) baziToolPlan {
	plan := baziToolPlan{}
	candidate := strings.TrimSpace(raw)
	if strings.HasPrefix(candidate, "```") {
		candidate = strings.TrimPrefix(candidate, "```json")
		candidate = strings.TrimPrefix(candidate, "```")
		candidate = strings.TrimSuffix(candidate, "```")
		candidate = strings.TrimSpace(candidate)
	}
	if strings.Contains(candidate, "{") {
		start := strings.Index(candidate, "{")
		end := strings.LastIndex(candidate, "}")
		if start >= 0 && end > start {
			candidate = candidate[start : end+1]
		}
	}
	if uErr := json.Unmarshal([]byte(candidate), &plan); uErr != nil {
		logger.Infof("[DEBUG][bazihelper] extractBaziToolPlan json_unmarshal_err=%v candidate_preview=%q",
			uErr, truncateText(candidate, 400))
	}
	if strings.TrimSpace(plan.NormalizedQuery) == "" {
		plan.NormalizedQuery = strings.TrimSpace(userQuery)
	}
	if len(plan.Calls) == 0 {
		fb := fallbackBaziCall(userQuery)
		logger.Infof("[DEBUG][bazihelper] extractBaziToolPlan empty_calls_using_fallback user_query_preview=%q tool=%s args_preview=%q",
			truncateText(userQuery, 200), fb.ToolName, truncateText(fmt.Sprint(fb.Arguments), 300))
		plan.Calls = []baziToolCall{fb}
		plan.Assumptions = append(plan.Assumptions, "未能稳定解析模型规划结果，已使用兜底工具调用。")
	}
	for i := range plan.Calls {
		plan.Calls[i].ToolName = normalizeBaziToolName(plan.Calls[i].ToolName, userQuery)
		if plan.Calls[i].Arguments == nil {
			plan.Calls[i].Arguments = map[string]any{}
		}
		plan.Calls[i].Arguments = normalizeBaziArguments(plan.Calls[i].ToolName, plan.Calls[i].Arguments, userQuery)
	}
	return plan
}

func fallbackBaziCall(userQuery string) baziToolCall {
	if containsAny(userQuery, []string{"黄历", "宜忌", "择日", "今日"}) {
		return baziToolCall{ToolName: "getChineseCalendar", Arguments: map[string]any{"solarDatetime": time.Now().Format(time.RFC3339)}, Reason: "兜底查询黄历信息"}
	}
	if looksLikeBaziText(userQuery) {
		return baziToolCall{ToolName: "getSolarTimes", Arguments: map[string]any{"bazi": normalizeBaziText(userQuery)}, Reason: "兜底反推八字对应时间"}
	}
	return baziToolCall{ToolName: "getChineseCalendar", Arguments: map[string]any{"solarDatetime": time.Now().Format(time.RFC3339)}, Reason: "兜底返回当前黄历信息"}
}

func normalizeBaziToolName(name string, userQuery string) string {
	switch strings.TrimSpace(name) {
	case "getBaziDetail", "getSolarTimes", "getChineseCalendar":
		return strings.TrimSpace(name)
	default:
		if looksLikeBaziText(userQuery) {
			return "getSolarTimes"
		}
		if containsAny(userQuery, []string{"黄历", "宜忌", "择日", "今日"}) {
			return "getChineseCalendar"
		}
		return "getBaziDetail"
	}
}

func isEmptyBaziArg(v any) bool {
	if v == nil {
		return true
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t) == ""
	}
	s := strings.TrimSpace(fmt.Sprint(v))
	return s == "" || s == "<nil>"
}

func normalizeBaziArguments(toolName string, args map[string]any, userQuery string) map[string]any {
	if args == nil {
		args = map[string]any{}
	}
	switch toolName {
	case "getBaziDetail":
		if isEmptyBaziArg(args["gender"]) {
			args["gender"] = inferGender(userQuery)
		}
	case "getChineseCalendar":
		if isEmptyBaziArg(args["solarDatetime"]) {
			// 使用当前时间作为默认值
			solarDatetime := time.Now().Format(time.RFC3339)
			logger.Infof("[DEBUG][bazihelper] normalizeBaziArguments getChineseCalendar solarDatetime=%q (userQuery=%q)", solarDatetime, userQuery)
			args["solarDatetime"] = solarDatetime
		}
	case "getSolarTimes":
		if isEmptyBaziArg(args["bazi"]) && looksLikeBaziText(userQuery) {
			args["bazi"] = normalizeBaziText(userQuery)
		}
	}
	return args
}

func inferGender(userQuery string) int {
	if containsAny(userQuery, []string{"女", "女生", "女性", "姑娘", "她"}) {
		return 0
	}
	return 1
}

func looksLikeBaziText(input string) bool {
	return regexp.MustCompile(`[\p{Han}]{2}\s+[\p{Han}]{2}\s+[\p{Han}]{2}\s+[\p{Han}]{2}`).FindString(strings.TrimSpace(input)) != ""
}

func normalizeBaziText(input string) string {
	if matched := regexp.MustCompile(`[\p{Han}]{2}\s+[\p{Han}]{2}\s+[\p{Han}]{2}\s+[\p{Han}]{2}`).FindString(strings.TrimSpace(input)); matched != "" {
		return matched
	}
	return strings.TrimSpace(input)
}

func containsAny(input string, keywords []string) bool {
	for _, keyword := range keywords {
		if strings.Contains(input, keyword) {
			return true
		}
	}
	return false
}

func truncateText(input string, max int) string {
	input = strings.TrimSpace(input)
	if max <= 0 || len(input) <= max {
		return input
	}
	return strings.TrimSpace(input[:max]) + "..."
}

func baziDebugOutKeys(out map[string]any) string {
	if len(out) == 0 {
		return "[]"
	}
	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	return strings.Join(keys, ",")
}
