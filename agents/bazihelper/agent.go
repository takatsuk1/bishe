package bazihelper

import (
	"ai/config"
	"ai/pkg/llm"
	"ai/pkg/logger"
	"ai/pkg/monitor"
	"ai/pkg/orchestrator"
	internalproto "ai/pkg/protocol"
	"ai/pkg/storage"
	internaltm "ai/pkg/taskmanager"
	"ai/pkg/tools"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	BaziHelperWorkflowID       = "bazihelper-default"
	BaziHelperWorkflowWorkerID = "bazihelper_worker"
	BaziHelperDefaultTaskType  = "bazihelper_default"
)

type ctxKeyTaskManager struct{}

type Agent struct {
	orchestratorEngine orchestrator.Engine
	llmClient          *llm.Client
	chatModel          string
	BaziTool           tools.Tool
	baziToolCatalog    string
}

type workflowNodeWorker struct {
	agent *Agent
}

type baziToolCall struct {
	ToolName  string         `json:"tool_name"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Reason    string         `json:"reason,omitempty"`
}

type baziToolPlan struct {
	NormalizedQuery string         `json:"normalized_query,omitempty"`
	SummaryFocus    string         `json:"summary_focus,omitempty"`
	Assumptions     []string       `json:"assumptions,omitempty"`
	Calls           []baziToolCall `json:"calls"`
}

var baziHelperNodeProgressText = map[string]string{
	"N_start":   "初始化八字任务",
	"N_extract": "提取出生信息与工具调用计划",
	"N_bazi":    "调用八字 MCP 工具",
	"N_summary": "整理命盘结果",
	"N_end":     "输出最终结果",
}

func NewAgent() (*Agent, error) {
	cfg := config.GetMainConfig()
	agent := &Agent{}

	agent.llmClient = llm.NewClient(cfg.LLM.URL, cfg.LLM.APIKey)
	agent.chatModel = strings.TrimSpace(cfg.LLM.ChatModel)
	if agent.chatModel == "" {
		agent.chatModel = strings.TrimSpace(cfg.LLM.ReasoningModel)
	}
	if agent.chatModel == "" {
		agent.chatModel = "qwen3-235b-a22b"
	}

	agent.BaziTool = tools.NewBaziMCPTool()

	agent.baziToolCatalog = strings.Join([]string{
		"可用 Bazi MCP 子工具如下：",
		"- getBaziDetail: 根据出生时间和性别获取完整八字命盘；gender 必填，男=1，女=0；solarDatetime 与 lunarDatetime 二选一。",
		"- getSolarTimes: 根据八字反推可能的公历时间；参数 bazi 例如：戊寅 己未 己卯 辛未。",
		"- getChineseCalendar: 查询指定日期黄历；可传 solarDatetime，未指定日期时可用今天。",
	}, "\n")

	engineCfg := orchestrator.Config{
		DefaultTaskTimeoutSec: cfg.Orchestrator.DefaultTaskTimeoutSec,
		RetryMaxAttempts:      cfg.Orchestrator.Retry.MaxAttempts,
		RetryBaseBackoffMs:    cfg.Orchestrator.Retry.BaseBackoffMs,
		RetryMaxBackoffMs:     cfg.Orchestrator.Retry.MaxBackoffMs,
	}
	if engineCfg.DefaultTaskTimeoutSec <= 0 {
		engineCfg.DefaultTaskTimeoutSec = 600
	}
	if engineCfg.RetryMaxAttempts <= 0 {
		engineCfg.RetryMaxAttempts = 3
	}
	if engineCfg.RetryBaseBackoffMs <= 0 {
		engineCfg.RetryBaseBackoffMs = 200
	}
	if engineCfg.RetryMaxBackoffMs <= 0 {
		engineCfg.RetryMaxBackoffMs = 5000
	}
	if mysqlStorage, mysqlErr := storage.GetMySQLStorage(); mysqlErr == nil && mysqlStorage != nil {
		engineCfg.MonitorService = monitor.NewService(mysqlStorage, nil)
	}

	agent.orchestratorEngine = orchestrator.NewEngine(engineCfg, orchestrator.NewInMemoryAgentRegistry())
	if err := agent.orchestratorEngine.RegisterWorker(orchestrator.AgentDescriptor{
		ID:           BaziHelperWorkflowWorkerID,
		Name:         "bazihelper workflow worker",
		Capabilities: []orchestrator.AgentCapability{"chat_model", "tool", "bazihelper"},
	}, &workflowNodeWorker{agent: agent}); err != nil {
		return nil, err
	}

	wf, err := buildBaziHelperWorkflow()
	if err != nil {
		return nil, err
	}
	if err = agent.orchestratorEngine.RegisterWorkflow(wf); err != nil {
		return nil, err
	}
	return agent, nil
}

func (a *Agent) ProcessInternal(ctx context.Context, taskID string, initialMsg internalproto.Message, manager internaltm.Manager) error {
	query := extractMessageText(initialMsg)
	if query == "" {
		return fmt.Errorf("invalid input parts")
	}
	if a.orchestratorEngine == nil {
		return fmt.Errorf("orchestrator engine not initialized")
	}

	ctx = withTaskManager(ctx, manager)
	userID := extractUserID(initialMsg.Metadata)

	runID, err := a.orchestratorEngine.StartWorkflow(ctx, BaziHelperWorkflowID, map[string]any{
		"task_id": taskID,
		"query":   query,
		"text":    query,
		"input":   query,
		"user_id": userID,
	})
	if err != nil {
		return fmt.Errorf("failed to start bazihelper workflow: %w", err)
	}
	logger.Infof("[DEBUG][bazihelper] ProcessInternal start task_id=%s run_id=%s user_id=%s query_len=%d query_preview=%q",
		taskID, runID, userID, len(query), truncateText(query, 240))
	stopProgress := a.startProgressReporter(ctx, taskID, runID, manager)
	defer stopProgress()

	runResult, err := a.orchestratorEngine.WaitRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("failed to wait bazihelper workflow: %w", err)
	}
	if runResult.State != orchestrator.RunStateSucceeded {
		if runResult.ErrorMessage != "" {
			return fmt.Errorf("bazihelper workflow failed: %s", runResult.ErrorMessage)
		}
		return fmt.Errorf("bazihelper workflow failed")
	}

	out, _ := runResult.FinalOutput["response"].(string)
	out = strings.TrimSpace(out)
	if out == "" {
		out = "Workflow executed successfully"
	}
	streamedFinal := baziStreamedToUser(runResult)
	if manager != nil {
		var doneMsg *internalproto.Message
		if !streamedFinal {
			doneMsg = &internalproto.Message{
				Role:  internalproto.MessageRoleAgent,
				Parts: []internalproto.Part{internalproto.NewTextPart(out)},
			}
		}
		_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateCompleted, doneMsg)
	}
	return nil
}

func (w *workflowNodeWorker) Execute(ctx context.Context, req orchestrator.ExecutionRequest) (orchestrator.ExecutionResult, error) {
	taskID, _ := req.Payload["task_id"].(string)
	query, _ := req.Payload["query"].(string)

	var (
		output map[string]any
		err    error
	)

	switch req.NodeType {
	case orchestrator.NodeTypeChatModel:
		output, err = w.agent.callChatModel(ctx, taskID, query, req.NodeConfig, req.Payload)
	case orchestrator.NodeTypeTool:
		output, err = w.agent.callTool(ctx, taskID, query, req.NodeConfig, req.Payload)
	default:
		response := strings.TrimSpace(query)
		if response == "" {
			response = "ok"
		}
		output = map[string]any{"response": response}
	}
	if err != nil {
		return orchestrator.ExecutionResult{}, err
	}
	return orchestrator.ExecutionResult{Output: output}, nil
}

func (a *Agent) callChatModel(ctx context.Context, taskID string, query string, nodeCfg map[string]any, payload map[string]any) (map[string]any, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query is empty")
	}

	intent := ""
	baseURL := strings.TrimSpace(a.llmClient.BaseURL)
	apiKey := strings.TrimSpace(a.llmClient.APIKey)
	model := strings.TrimSpace(a.chatModel)
	if nodeCfg != nil {
		if v, ok := nodeCfg["intent"].(string); ok {
			intent = strings.TrimSpace(v)
		}
		if v, ok := nodeCfg["url"].(string); ok && strings.TrimSpace(v) != "" {
			baseURL = strings.TrimSpace(v)
		}
		if v, ok := nodeCfg["apikey"].(string); ok && strings.TrimSpace(v) != "" {
			apiKey = strings.TrimSpace(v)
		}
		if v, ok := nodeCfg["model"].(string); ok && strings.TrimSpace(v) != "" {
			model = strings.TrimSpace(v)
		}
	}

	finalPrompt := query
	switch intent {
	case "extract_bazi_request":
		finalPrompt = buildExtractPlanPrompt(extractBaziUserQuery(payload, query), a.baziToolCatalog)
	case "summarize_bazi_result":
		finalPrompt = buildSummaryPrompt(payload, query)
	}

	streamToUser := strings.EqualFold(strings.TrimSpace(intent), "summarize_bazi_result")

	client := llm.NewClient(baseURL, apiKey)
	var pending strings.Builder
	lastPushAt := time.Time{}
	streamedToUser := false
	flushToUser := func(force bool) {
		if !streamToUser || pending.Len() == 0 {
			return
		}
		if !force && !lastPushAt.IsZero() && time.Since(lastPushAt) < 120*time.Millisecond && pending.Len() < 48 {
			return
		}
		a.emitAssistantDelta(ctx, taskID, pending.String())
		streamedToUser = true
		pending.Reset()
		lastPushAt = time.Now()
	}

	a.emitSemanticStep(ctx, taskID, "bazihelper.llm.start", internalproto.StepStateInfo, "正在调用大模型："+intent)
	resp, err := client.ChatCompletionStream(ctx, model, []llm.Message{{Role: "user", Content: finalPrompt}}, nil, nil, func(delta string) error {
		if strings.TrimSpace(delta) == "" {
			return nil
		}
		if streamToUser {
			pending.WriteString(delta)
			flushToUser(false)
		}
		return nil
	})
	if err == nil {
		flushToUser(true)
	}
	if err != nil {
		logger.Warnf("[bazihelper] llm failed task=%s intent=%s err=%v, using fallback", taskID, intent, err)
		return map[string]any{"response": "大模型调用失败，请稍后重试。", "streamed_to_user": streamedToUser}, nil
	}
	resp = strings.TrimSpace(resp)
	if resp == "" {
		resp = "(empty LLM response)"
	}
	a.emitSemanticStep(ctx, taskID, "bazihelper.llm.end", internalproto.StepStateEnd, "完成：大模型处理")

	switch intent {
	case "extract_bazi_request":
		logger.Infof("[DEBUG][bazihelper] callChatModel extract_bazi_request task_id=%s resp_len=%d resp_preview=%q",
			taskID, len(resp), truncateText(resp, 800))
	case "summarize_bazi_result":
		logger.Infof("[DEBUG][bazihelper] callChatModel summarize_bazi_result task_id=%s resp_len=%d", taskID, len(resp))
	}
	return map[string]any{"response": resp, "streamed_to_user": streamedToUser}, nil
}

func (a *Agent) callTool(ctx context.Context, taskID string, query string, nodeCfg map[string]any, payload map[string]any) (map[string]any, error) {
	toolName := ""
	if nodeCfg != nil {
		if v, ok := nodeCfg["tool_name"].(string); ok {
			toolName = strings.TrimSpace(v)
		}
	}
	if toolName == "" {
		return nil, fmt.Errorf("tool node missing config.tool_name")
	}

	switch toolName {
	case "bazi":
		return a.callBaziTool(ctx, taskID, query, payload)
	default:
		return nil, fmt.Errorf("tool %s not found", toolName)
	}
}

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

func buildBaziHelperWorkflow() (*orchestrator.Workflow, error) {
	wf, err := orchestrator.NewWorkflow(BaziHelperWorkflowID, "bazihelper workflow")
	if err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{ID: "N_start", Type: orchestrator.NodeTypeStart}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{ID: "N_extract", Type: orchestrator.NodeTypeChatModel, AgentID: BaziHelperWorkflowWorkerID, TaskType: "chat_model", Config: map[string]any{"intent": "extract_bazi_request"}, PreInput: "提取八字请求并生成工具调用 JSON。"}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{ID: "N_bazi", Type: orchestrator.NodeTypeTool, AgentID: BaziHelperWorkflowWorkerID, TaskType: BaziHelperDefaultTaskType, Config: map[string]any{"tool_name": "bazi"}}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{ID: "N_summary", Type: orchestrator.NodeTypeChatModel, AgentID: BaziHelperWorkflowWorkerID, TaskType: "chat_model", Config: map[string]any{"intent": "summarize_bazi_result"}, PreInput: "基于八字工具结果整理最终回复。"}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{ID: "N_end", Type: orchestrator.NodeTypeEnd}); err != nil {
		return nil, err
	}
	if err = wf.AddEdge("N_start", "N_extract"); err != nil {
		return nil, err
	}
	if err = wf.AddEdge("N_extract", "N_bazi"); err != nil {
		return nil, err
	}
	if err = wf.AddEdge("N_bazi", "N_summary"); err != nil {
		return nil, err
	}
	if err = wf.AddEdge("N_summary", "N_end"); err != nil {
		return nil, err
	}
	return wf, nil
}

func withTaskManager(ctx context.Context, m internaltm.Manager) context.Context {
	if ctx == nil || m == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyTaskManager{}, m)
}

func (a *Agent) startProgressReporter(ctx context.Context, taskID string, runID string, manager internaltm.Manager) func() {
	if manager == nil || a.orchestratorEngine == nil {
		return func() {}
	}
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		started := map[string]bool{}
		finished := map[string]bool{}
		for {
			run, err := a.orchestratorEngine.GetRun(ctx, runID)
			if err == nil {
				nodeID := strings.TrimSpace(run.CurrentNodeID)
				if nodeID != "" && !started[nodeID] {
					started[nodeID] = true
					a.emitStepEvent(ctx, manager, taskID, nodeID, internalproto.StepStateStart)
				}
				for _, nr := range run.NodeResults {
					id := strings.TrimSpace(nr.NodeID)
					if id == "" || finished[id] {
						continue
					}
					if stepState, ok := baziToTerminalStepState(nr.State); ok {
						finished[id] = true
						a.emitStepEvent(ctx, manager, taskID, id, stepState)
					}
				}
				if run.State != orchestrator.RunStateRunning {
					return
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-stopCh:
				return
			case <-ticker.C:
			}
		}
	}()
	return func() {
		close(stopCh)
		<-doneCh
	}
}

func (a *Agent) emitStepEvent(ctx context.Context, manager internaltm.Manager, taskID string, nodeID string, state internalproto.StepState) {
	if manager == nil {
		return
	}
	messageZh := baziHelperNodeProgressText[nodeID]
	if messageZh == "" {
		messageZh = fmt.Sprintf("执行节点 %s", nodeID)
	}
	if state == internalproto.StepStateEnd {
		messageZh = "完成：" + messageZh
	}
	if state == internalproto.StepStateError {
		messageZh = "失败：" + messageZh
	}
	ev := internalproto.NewStepEvent("bazihelper", "workflow", nodeID, state, messageZh)
	token, err := internalproto.EncodeStepToken(ev)
	if err != nil {
		return
	}
	_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateWorking, &internalproto.Message{
		Role:  internalproto.MessageRoleAgent,
		Parts: []internalproto.Part{internalproto.NewTextPart(token)},
	})
}

func baziToTerminalStepState(state orchestrator.TaskState) (internalproto.StepState, bool) {
	switch state {
	case orchestrator.TaskStateSucceeded:
		return internalproto.StepStateEnd, true
	case orchestrator.TaskStateFailed, orchestrator.TaskStateCanceled:
		return internalproto.StepStateError, true
	default:
		return "", false
	}
}

func extractMessageText(msg internalproto.Message) string {
	parts := make([]string, 0, len(msg.Parts))
	for _, part := range msg.Parts {
		if part.Type != internalproto.PartTypeText {
			continue
		}
		text := strings.TrimSpace(part.Text)
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
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

func taskManagerFromContext(ctx context.Context) internaltm.Manager {
	if ctx == nil {
		return nil
	}
	m, _ := ctx.Value(ctxKeyTaskManager{}).(internaltm.Manager)
	return m
}

func (a *Agent) emitSemanticStep(ctx context.Context, taskID string, name string, state internalproto.StepState, message string) {
	manager := taskManagerFromContext(ctx)
	if manager == nil {
		return
	}
	ev := internalproto.NewStepEvent("bazihelper", "semantic", strings.TrimSpace(name), state, strings.TrimSpace(message))
	token, err := internalproto.EncodeStepToken(ev)
	if err != nil {
		return
	}
	_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateWorking, &internalproto.Message{
		Role:  internalproto.MessageRoleAgent,
		Parts: []internalproto.Part{internalproto.NewTextPart(token)},
	})
}

func (a *Agent) emitAssistantDelta(ctx context.Context, taskID string, text string) {
	manager := taskManagerFromContext(ctx)
	if manager == nil {
		return
	}
	if text == "" {
		return
	}
	_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateWorking, &internalproto.Message{
		Role:  internalproto.MessageRoleAgent,
		Parts: []internalproto.Part{internalproto.NewTextPart(text)},
	})
}

func baziStreamedToUser(runResult orchestrator.RunResult) bool {
	for _, nr := range runResult.NodeResults {
		if nr.Output == nil {
			continue
		}
		v, ok := nr.Output["streamed_to_user"]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case bool:
			if t {
				return true
			}
		case string:
			if strings.EqualFold(strings.TrimSpace(t), "true") {
				return true
			}
		}
	}
	return false
}
