package lbshelper

import (
	"ai/pkg/llm"
	"ai/pkg/logger"
	"ai/pkg/tools"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

func (a *Agent) executeAmapWithFallback(ctx context.Context, tool tools.Tool, taskID string, params map[string]any) (map[string]any, error) {
	out, err := tool.Execute(ctx, params)
	if err == nil {
		return out, nil
	}
	if !strings.Contains(strings.ToUpper(err.Error()), "INVALID_PARAMS") {
		return nil, err
	}
	fallback := map[string]any{
		"tool_name": "maps_text_search",
		"arguments": map[string]any{
			"keywords": strings.TrimSpace(fmt.Sprint(params["query"])),
		},
		"task_id": taskID,
	}
	logger.Infof("[TRACE] lbshelper.amap retry task=%s tool=maps_text_search reason=INVALID_PARAMS", taskID)
	a.emitInfoStep(ctx, taskID, "tool", "lbshelper.amap.retry", "参数不合法，回退到 maps_text_search")
	return tool.Execute(ctx, fallback)
}

func buildAmapCallPlanFromModel(toolCall map[string]any, primary map[string]any, rawQuery string) []map[string]any {
	query := strings.TrimSpace(fmt.Sprint(toolCall["query"]))
	if query == "" {
		query = strings.TrimSpace(fmt.Sprint(primary["query"]))
	}
	if query == "" {
		query = strings.TrimSpace(rawQuery)
	}
	plan := make([]map[string]any, 0, 4)
	appendUnique := func(toolName string, arguments map[string]any) {
		if strings.TrimSpace(toolName) == "" {
			return
		}
		if arguments == nil {
			arguments = map[string]any{}
		}
		for _, p := range plan {
			if strings.TrimSpace(fmt.Sprint(p["tool_name"])) != toolName {
				continue
			}
			existingArgs, _ := p["arguments"].(map[string]any)
			if sameAmapArguments(existingArgs, arguments) {
				return
			}
		}
		plan = append(plan, map[string]any{
			"query":     query,
			"tool_name": toolName,
			"arguments": arguments,
		})
	}

	if rawCalls, ok := toolCall["calls"].([]any); ok {
		for _, item := range rawCalls {
			m, _ := item.(map[string]any)
			if m == nil {
				continue
			}
			callQuery := strings.TrimSpace(fmt.Sprint(m["query"]))
			if callQuery == "" {
				callQuery = query
			}
			candidate := map[string]any{
				"query":     callQuery,
				"tool_name": strings.TrimSpace(fmt.Sprint(m["tool_name"])),
			}
			if args, ok := m["arguments"].(map[string]any); ok {
				candidate["arguments"] = args
			}
			norm := normalizeAmapCallParams(candidate, callQuery)
			normTool := strings.TrimSpace(fmt.Sprint(norm["tool_name"]))
			normArgs, _ := norm["arguments"].(map[string]any)
			appendUnique(normTool, normArgs)
		}
	}

	primaryTool := strings.TrimSpace(fmt.Sprint(primary["tool_name"]))
	primaryArgs, _ := primary["arguments"].(map[string]any)
	appendUnique(primaryTool, primaryArgs)

	if len(plan) == 0 {
		appendUnique("maps_text_search", map[string]any{"keywords": query})
	}
	return plan
}

func sameAmapArguments(a map[string]any, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if strings.TrimSpace(fmt.Sprint(v)) != strings.TrimSpace(fmt.Sprint(b[k])) {
			return false
		}
	}
	return true
}

func summarizeAmapCallData(out map[string]any) string {
	if len(out) == 0 {
		return "无返回数据"
	}
	if isErr, ok := out["is_error"].(bool); ok && isErr {
		return "工具返回错误"
	}
	content, _ := out["content"].([]any)
	for _, item := range content {
		m, _ := item.(map[string]any)
		text := strings.TrimSpace(fmt.Sprint(m["text"]))
		if text == "" {
			continue
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(text), &parsed); err != nil {
			continue
		}
		if pois, ok := parsed["pois"].([]any); ok {
			return fmt.Sprintf("POI 数量 %d", len(pois))
		}
		if lives, ok := parsed["lives"].([]any); ok {
			return fmt.Sprintf("天气结果 %d 条", len(lives))
		}
	}
	return "已返回结构化数据"
}

func summarizeAmapResult(out map[string]any) string {
	if len(out) == 0 {
		return "(empty)"
	}
	b, err := json.Marshal(out)
	if err != nil {
		return strings.TrimSpace(fmt.Sprintf("%v", out))
	}
	const maxLen = 800
	s := strings.TrimSpace(string(b))
	if len(s) > maxLen {
		return s[:maxLen] + "...(truncated)"
	}
	return s
}

func normalizeAmapCallParams(params map[string]any, rawQuery string) map[string]any {
	out := map[string]any{}
	for k, v := range params {
		out[k] = v
	}

	query := strings.TrimSpace(fmt.Sprint(out["query"]))
	if query == "" {
		query = strings.TrimSpace(rawQuery)
	}
	if query == "" {
		query = "路线规划"
	}

	toolName := strings.TrimSpace(fmt.Sprint(out["tool_name"]))
	if !isSupportedAmapTool(toolName) {
		toolName = inferAmapToolName(query)
	}

	args, _ := out["arguments"].(map[string]any)
	if args == nil {
		args = map[string]any{}
	}

	switch toolName {
	case "maps_direction_driving", "maps_direction_transit_integrated", "maps_direction_walking", "maps_direction_bicycling":
		origin := strings.TrimSpace(fmt.Sprint(args["origin"]))
		destination := strings.TrimSpace(fmt.Sprint(args["destination"]))
		if origin == "" || destination == "" {
			if odFromQuery, odToQuery := parseOriginDestination(query); odFromQuery != "" && odToQuery != "" {
				origin = odFromQuery
				destination = odToQuery
			}
		}
		if origin == "" || destination == "" {
			toolName = "maps_text_search"
			args = map[string]any{"keywords": query}
			break
		}
		next := map[string]any{"origin": origin, "destination": destination}
		if toolName == "maps_direction_transit_integrated" {
			if city := strings.TrimSpace(fmt.Sprint(args["city"])); city != "" {
				next["city"] = city
			} else {
				next["city"] = origin
			}
			if cityd := strings.TrimSpace(fmt.Sprint(args["cityd"])); cityd != "" {
				next["cityd"] = cityd
			} else {
				next["cityd"] = destination
			}
		}
		args = next
	case "maps_text_search", "maps_around_search":
		keywords := strings.TrimSpace(fmt.Sprint(args["keywords"]))
		if keywords == "" {
			keywords = query
		}
		keywords = enrichTravelKeywords(query, keywords)
		args = map[string]any{"keywords": keywords}
	case "maps_weather":
		city := strings.TrimSpace(fmt.Sprint(args["city"]))
		if city == "" {
			city = firstMeaningfulToken(query)
		}
		if city == "" {
			city = "北京"
		}
		args = map[string]any{"city": city}
	case "maps_geo":
		address := strings.TrimSpace(fmt.Sprint(args["address"]))
		if address == "" {
			address = query
		}
		args = map[string]any{"address": address}
	case "maps_regeocode":
		location := strings.TrimSpace(fmt.Sprint(args["location"]))
		if location == "" {
			toolName = "maps_text_search"
			args = map[string]any{"keywords": query}
		}
	case "maps_distance":
		origins := strings.TrimSpace(fmt.Sprint(args["origins"]))
		destination := strings.TrimSpace(fmt.Sprint(args["destination"]))
		if origins == "" || destination == "" {
			if from, to := parseOriginDestination(query); from != "" && to != "" {
				origins, destination = from, to
			}
		}
		if origins == "" || destination == "" {
			toolName = "maps_text_search"
			args = map[string]any{"keywords": query}
		} else {
			args = map[string]any{"origins": origins, "destination": destination}
		}
	default:
		if len(args) == 0 {
			args = map[string]any{"keywords": query}
		}
	}

	out["query"] = query
	out["tool_name"] = toolName
	out["arguments"] = args
	return out
}

func isSupportedAmapTool(name string) bool {
	_, ok := amapSupportedTools[strings.TrimSpace(name)]
	return ok
}

func inferAmapToolName(query string) string {
	q := strings.ToLower(strings.TrimSpace(query))
	switch {
	case strings.Contains(q, "天气"):
		return "maps_weather"
	case strings.Contains(q, "附近"):
		return "maps_around_search"
	case strings.Contains(q, "经纬"), strings.Contains(q, "坐标"):
		return "maps_geo"
	case strings.Contains(q, "步行"):
		return "maps_direction_walking"
	case strings.Contains(q, "骑行"):
		return "maps_direction_bicycling"
	case strings.Contains(q, "公交"), strings.Contains(q, "地铁"):
		return "maps_direction_transit_integrated"
	case strings.Contains(q, "打车"), strings.Contains(q, "驾车"), strings.Contains(q, "开车"):
		return "maps_direction_driving"
	case strings.Contains(q, "到") || strings.Contains(q, "去"):
		return "maps_direction_driving"
	default:
		return "maps_text_search"
	}
}

var odRE = regexp.MustCompile(`(?:从)?\s*([^\s,，。；;]+?)\s*(?:到|去往|去)\s*([^\s,，。；;]+)`)

func parseOriginDestination(query string) (string, string) {
	m := odRE.FindStringSubmatch(strings.TrimSpace(query))
	if len(m) < 3 {
		return "", ""
	}
	from := strings.TrimSpace(m[1])
	to := strings.TrimSpace(m[2])
	if from == "" || to == "" {
		return "", ""
	}
	return from, to
}

func firstMeaningfulToken(query string) string {
	for _, token := range strings.FieldsFunc(query, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', ',', '，', '。', ';', '；', ':', '：':
			return true
		default:
			return false
		}
	}) {
		token = strings.TrimSpace(token)
		if token != "" {
			return token
		}
	}
	return ""
}

func enrichTravelKeywords(query string, current string) string {
	current = strings.TrimSpace(current)
	q := strings.TrimSpace(query)
	if current == "" {
		current = q
	}
	if current == "" {
		return ""
	}
	lower := strings.ToLower(q)
	if strings.Contains(lower, "一日游") || strings.Contains(lower, "旅游") || strings.Contains(lower, "行程") {
		city := extractCityName(q)
		if city == "" {
			city = firstMeaningfulToken(q)
		}
		if city != "" {
			return strings.TrimSpace(city + " 热门景点 旅游攻略")
		}
		return "热门景点 旅游攻略"
	}
	return current
}

var cityRE = regexp.MustCompile(`([\p{Han}]{2,10})(?:市|区|县)`)

func extractCityName(query string) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return ""
	}
	m := cityRE.FindStringSubmatch(query)
	if len(m) >= 2 {
		return strings.TrimSpace(m[1])
	}
	if strings.Contains(query, "重庆") {
		return "重庆"
	}
	if strings.Contains(query, "北京") {
		return "北京"
	}
	if strings.Contains(query, "上海") {
		return "上海"
	}
	if strings.Contains(query, "成都") {
		return "成都"
	}
	if strings.Contains(query, "广州") {
		return "广州"
	}
	if strings.Contains(query, "深圳") {
		return "深圳"
	}
	return ""
}

var amapSupportedTools = map[string]struct{}{
	"maps_direction_bicycling":          {},
	"maps_direction_driving":            {},
	"maps_direction_transit_integrated": {},
	"maps_direction_walking":            {},
	"maps_distance":                     {},
	"maps_geo":                          {},
	"maps_regeocode":                    {},
	"maps_ip_location":                  {},
	"maps_schema_personal_map":          {},
	"maps_around_search":                {},
	"maps_search_detail":                {},
	"maps_text_search":                  {},
	"maps_schema_navi":                  {},
	"maps_schema_take_taxi":             {},
	"maps_weather":                      {},
}

func (a *Agent) findToolByName(name string) (tools.Tool, error) {
	switch strings.TrimSpace(name) {
	case "amap":
		if a.AmapTool == nil {
			return nil, fmt.Errorf("tool amap is not initialized")
		}
		return a.AmapTool, nil
	default:
		return nil, fmt.Errorf("tool %s not found", name)
	}
}

func (a *Agent) findAmapToolInfo(name string) *tools.ToolInfo {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	for _, ti := range a.amapToolInfos {
		if strings.EqualFold(strings.TrimSpace(ti.Name), name) {
			cur := ti
			return &cur
		}
	}
	return nil
}

func (a *Agent) generateAmapArguments(ctx context.Context, taskID string, ti tools.ToolInfo, userQuery string) map[string]any {
	// Build a prompt describing the tool schema and ask LLM to output JSON arguments object
	prompt := strings.Builder{}
	prompt.WriteString("你是地图工具参数生成助手。\n")
	prompt.WriteString("目标子工具：")
	prompt.WriteString(ti.Name)
	prompt.WriteString("\n说明：")
	prompt.WriteString(ti.Description)
	prompt.WriteString("\n参数：\n")
	for _, p := range ti.Parameters {
		req := "可选"
		if p.Required {
			req = "必填"
		}
		prompt.WriteString(fmt.Sprintf("- %s (%s) : %s\n", p.Name, req, p.Description))
	}
	prompt.WriteString("\n请根据用户问题生成该工具调用的 JSON 参数对象，仅输出一个 JSON 对象，不要其它文本。用户问题:\n")
	prompt.WriteString(userQuery)

	baseURL := strings.TrimSpace(a.llmClient.BaseURL)
	apiKey := strings.TrimSpace(a.llmClient.APIKey)
	model := strings.TrimSpace(a.chatModel)
	client := llm.NewClient(baseURL, apiKey)
	resp, err := client.ChatCompletion(ctx, model, []llm.Message{{Role: "user", Content: prompt.String()}}, nil, nil)
	if err != nil {
		logger.Infof("[TRACE] lbshelper.generateAmapArguments llm error=%v", err)
		return nil
	}
	// parse JSON object from resp
	parsed := extractToolCall(resp)
	if args, ok := parsed["arguments"].(map[string]any); ok && len(args) > 0 {
		return args
	}
	// maybe LLM returned just the object
	if len(parsed) > 0 {
		// if parsed contains standard parameter keys, return parsed
		hasParam := false
		out := map[string]any{}
		for _, p := range ti.Parameters {
			if v, ok := parsed[p.Name]; ok && v != nil {
				out[p.Name] = v
				hasParam = true
			}
		}
		if hasParam {
			return out
		}
	}
	return nil
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

var lbsStepTokenRe = regexp.MustCompile(`\[\]\(step://[^\)]*\)`)

func extractLBSUserQuery(payload map[string]any, fallback string) string {
	for _, key := range []string{"input", "text", "query"} {
		raw := strings.TrimSpace(fmt.Sprint(payload[key]))
		if raw == "" || raw == "<nil>" {
			continue
		}
		if q := extractLBSCurrentQuestion(raw); q != "" {
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
			if q := extractLBSCurrentQuestion(raw); q != "" {
				return q
			}
		}
	}
	return extractLBSCurrentQuestion(strings.TrimSpace(fallback))
}

func extractLBSCurrentQuestion(in string) string {
	s := strings.TrimSpace(lbsStepTokenRe.ReplaceAllString(in, " "))
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

func extractLBSSummaryInput(payload map[string]any, fallback string) string {
	if amapOut, ok := payload["N_amap"].(map[string]any); ok {
		if raw := strings.TrimSpace(fmt.Sprint(amapOut["response"])); raw != "" && raw != "<nil>" {
			return raw
		}
		if result, ok := amapOut["result"].(map[string]any); ok && len(result) > 0 {
			if b, err := json.Marshal(result); err == nil {
				return strings.TrimSpace(string(b))
			}
		}
	}
	if latest, ok := payload["latest_output"].(map[string]any); ok {
		if raw := strings.TrimSpace(fmt.Sprint(latest["response"])); raw != "" && raw != "<nil>" {
			return raw
		}
	}
	return strings.TrimSpace(fallback)
}

func extractToolCall(raw string) map[string]any {
	out := map[string]any{}
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
	var parsed map[string]any
	if err := json.Unmarshal([]byte(candidate), &parsed); err == nil && len(parsed) > 0 {
		for k, v := range parsed {
			out[k] = v
		}
	}
	if _, ok := out["query"]; !ok {
		out["query"] = strings.TrimSpace(raw)
	}
	return out
}

func buildExtractRoutePrompt(userQuery string, toolCatalog string) string {
	prompt := strings.Builder{}
	prompt.WriteString("你是地图路径规划助手。\n")
	prompt.WriteString("任务: 从用户问题中提取路径规划核心文本，并自主决定需要调用哪些 AMap MCP 子工具与参数。\n")
	prompt.WriteString("处理相对时间：当用户提到「今天」、「明天」、「后天」、「昨天」、「前天」等相对时间时，请根据当前日期计算出准确的日期，并转换为 ISO 时间字符串。\n")
	prompt.WriteString("时间计算规则例：首先要准确获取当前日期，然后根据用户问题计算出目标日期。\n")
	prompt.WriteString("当前时间: ")
	prompt.WriteString(time.Now().Format("2006-01-02 15:04:05"))
	prompt.WriteString("\n")
	prompt.WriteString("可用 AMap MCP 工具信息: ")
	prompt.WriteString(strings.TrimSpace(toolCatalog))
	prompt.WriteString("\n")
	prompt.WriteString("输出要求: 仅输出 JSON 对象，不要输出任何其它文本或 markdown。\n")
	prompt.WriteString("JSON 结构: {\"query\":\"...\",\"calls\":[{\"tool_name\":\"...\",\"arguments\":{...},\"query\":\"...\"}],\"tool_name\":\"...\",\"arguments\":{...}}\n")
	prompt.WriteString("字段说明:\n")
	prompt.WriteString("- query: 用户核心目标。\n")
	prompt.WriteString("- calls: 你规划的多次调用列表（至少 1 次），每个元素都是一次独立 AMap 子工具调用。\n")
	prompt.WriteString("- tool_name/arguments: 兼容字段，等于 calls[0] 的内容。\n")
	prompt.WriteString("行程信息覆盖要求: 你需要通过 calls 组合尽可能覆盖景点、交通动线、餐饮、天气/注意事项等关键信息。\n")
	prompt.WriteString("参数规范:\n")
	prompt.WriteString("- maps_direction_*: arguments 至少包含 origin 和 destination。\n")
	prompt.WriteString("- maps_text_search/maps_around_search: arguments 使用 keywords。\n")
	prompt.WriteString("- maps_weather: arguments 使用 city。\n")
	prompt.WriteString("若某项无法确定参数，可先用 maps_text_search 获取候选信息，再安排后续 calls。\n")
	prompt.WriteString("用户问题:\n")
	prompt.WriteString(userQuery)
	return prompt.String()
}

func buildSummaryPrompt(toolOutput string) string {
	prompt := strings.Builder{}
	prompt.WriteString("你是旅行行程规划助手。\n")
	prompt.WriteString("请基于以下 AMap MCP 工具返回内容，直接输出可执行的完整行程规划。\n")
	prompt.WriteString("硬性输出要求：\n")
	prompt.WriteString("1. 直接给最终行程，不要输出诊断语，如“缺失信息分析报告”“无法生成”。\n")
	prompt.WriteString("2. 必须覆盖以下信息；缺失项也要给出“建议值/默认值”，不要留空。\n")
	prompt.WriteString("【基础刚需】\n")
	prompt.WriteString("- 核心时间：出发/返程、每日可用时长、关键节点（航班/高铁/节假日限制/闭馆）\n")
	prompt.WriteString("- 往返与交通基点：出发地、目的地、到达方式与落地时间\n")
	prompt.WriteString("- 出行人群与底线：人数年龄、体能、禁忌、预算档位\n")
	prompt.WriteString("- 核心目标：风景/美食/亲子/人文等优先级、必去点与必吃项\n")
	prompt.WriteString("【落地执行】\n")
	prompt.WriteString("- 每日动线：早中晚顺序、停留时长、顺路衔接、午休/空档\n")
	prompt.WriteString("- 当地交通：地铁/打车/公交/租车建议、步行距离预判、停车或接驳\n")
	prompt.WriteString("- 住宿定位：入住区域、入住退房时间、酒店刚需\n")
	prompt.WriteString("- 餐饮安排：每餐建议、预约提示、避雷提醒、应急补给\n")
	prompt.WriteString("【预约与规则】\n")
	prompt.WriteString("- 门票预约渠道、限流时段、优惠政策、闭馆时间\n")
	prompt.WriteString("- 演出/游船/特色餐饮的预订与改退规则\n")
	prompt.WriteString("- 当地政策与限制（安检/宠物/穿搭）\n")
	prompt.WriteString("【应急兜底】\n")
	prompt.WriteString("- 证件清单与电子备份\n")
	prompt.WriteString("- 附近医院/药店/客服电话/保险建议\n")
	prompt.WriteString("- 天气与 Plan B、极端天气调整、行李穿搭\n")
	prompt.WriteString("3. 输出结构固定为：\n")
	prompt.WriteString("- 一句话极简总结\n")
	prompt.WriteString("- 行程总览\n")
	prompt.WriteString("- 每日分时段计划（上午/中午/下午/晚上）\n")
	prompt.WriteString("- 交通与住宿\n")
	prompt.WriteString("- 餐饮与打卡\n")
	prompt.WriteString("- 预约规则清单\n")
	prompt.WriteString("- 应急保障清单\n")
	prompt.WriteString("4. 若工具结果不全，可结合常识补全并标注“建议方案（基于常识）”。\n\n")
	prompt.WriteString("工具返回内容:\n")
	prompt.WriteString(toolOutput)
	return prompt.String()
}
