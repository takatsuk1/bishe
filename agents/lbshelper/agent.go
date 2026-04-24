package lbshelper

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
	"strings"
	"time"
)

const (
	LBSHelperWorkflowID       = "lbshelper-default"
	LBSHelperWorkflowWorkerID = "lbshelper_worker"
	LBSHelperDefaultTaskType  = "lbshelper_default"
)

type ctxKeyTaskManager struct{}

type Agent struct {
	orchestratorEngine orchestrator.Engine
	llmClient          *llm.Client
	chatModel          string
	AmapTool           tools.Tool
	amapToolCatalog    string
	amapToolInfos      []tools.ToolInfo
}

type workflowNodeWorker struct {
	agent *Agent
}

type stepReporter struct {
	agent   string
	taskID  string
	manager internaltm.Manager
}

var lbsHelperNodeProgressText = map[string]string{
	"N_start":   "初始化路线规划任务",
	"N_extract": "提取路线意图与工具参数",
	"N_amap":    "调用 AMap MCP 进行路径规划",
	"N_summary": "整理路线建议与注意事项",
	"N_end":     "输出最终路线结果",
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
	logger.Infof("[TRACE] lbshelper llm_config url=%s model=%s api_key_set=%t", strings.TrimSpace(cfg.LLM.URL), agent.chatModel, strings.TrimSpace(cfg.LLM.APIKey) != "")

	amapURL := strings.TrimSpace(cfg.AMap.ServerURL)
	if amapURL == "" {
		amapURL = "https://mcp.amap.com/sse"
	}
	amapCfg := tools.MCPToolConfig{
		ServerURL: amapURL,
		ToolName:  "auto",
	}
	agent.AmapTool = tools.NewMCPTool(
		"amap",
		"调用 AMap MCP 服务；由 Agent 通过 tool_name 决定具体子工具",
		[]tools.ToolParameter{
			{Name: "tool_name", Type: tools.ParamTypeString, Required: false, Description: "要调用的 MCP 子工具名（如 maps_direction_driving）"},
			{Name: "arguments", Type: tools.ParamTypeObject, Required: false, Description: "MCP 子工具参数对象"},
			{Name: "query", Type: tools.ParamTypeString, Required: false, Description: "兼容字段"},
		},
		amapCfg,
	)

	agent.amapToolCatalog = "（暂未获取到工具清单，请按任务选择最匹配的 AMap MCP 子工具）"
	if mcpTool, ok := agent.AmapTool.(*tools.MCPTool); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if discovery, err := tools.NewMCPToolDiscovery(amapURL); err == nil {
			defer func() { _ = discovery.Close() }()
			if infos, listErr := discovery.ListTools(ctx); listErr == nil && len(infos) > 0 {
				names := make([]string, 0, len(infos))
				for _, info := range infos {
					names = append(names, info.Name)
				}
				agent.amapToolCatalog = "可用工具: " + strings.Join(names, ", ")
				agent.amapToolInfos = infos
			}
		}
		_ = mcpTool
	}
	logger.Infof("[TRACE] lbshelper startup amap tool catalog=%s", truncateText(agent.amapToolCatalog, 800))
	if len(agent.amapToolInfos) > 0 {
		// log a concise schema summary
		var sb strings.Builder
		for _, ti := range agent.amapToolInfos {
			sb.WriteString(ti.Name)
			sb.WriteString(": ")
			if len(ti.Parameters) > 0 {
				params := make([]string, 0, len(ti.Parameters))
				for _, p := range ti.Parameters {
					req := "optional"
					if p.Required {
						req = "required"
					}
					params = append(params, fmt.Sprintf("%s(%s)", p.Name, req))
				}
				sb.WriteString(strings.Join(params, ", "))
			}
			sb.WriteString("; ")
		}
		logger.Infof("[TRACE] lbshelper startup amap tool schemas=%s", truncateText(sb.String(), 1200))
	}

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
		logger.Infof("[TRACE] lbshelper monitor enabled")
	} else {
		logger.Infof("[TRACE] lbshelper monitor disabled: mysql unavailable")
	}

	agent.orchestratorEngine = orchestrator.NewEngine(engineCfg, orchestrator.NewInMemoryAgentRegistry())
	if err := agent.orchestratorEngine.RegisterWorker(orchestrator.AgentDescriptor{
		ID:           LBSHelperWorkflowWorkerID,
		Name:         "lbshelper workflow worker",
		Capabilities: []orchestrator.AgentCapability{"chat_model", "tool", "lbshelper"},
	}, &workflowNodeWorker{agent: agent}); err != nil {
		return nil, err
	}

	wf, err := buildLBSHelperWorkflow()
	if err != nil {
		return nil, err
	}
	if err = agent.orchestratorEngine.RegisterWorkflow(wf); err != nil {
		return nil, err
	}

	return agent, nil
}

func (a *Agent) ProcessInternal(ctx context.Context, taskID string, initialMsg internalproto.Message,
	manager internaltm.Manager) error {
	if len(initialMsg.Parts) == 0 {
		return fmt.Errorf("invalid input parts")
	}
	queryParts := make([]string, 0, len(initialMsg.Parts))
	for _, part := range initialMsg.Parts {
		if part.Type != internalproto.PartTypeText {
			continue
		}
		text := strings.TrimSpace(part.Text)
		if text != "" {
			queryParts = append(queryParts, text)
		}
	}
	if len(queryParts) == 0 {
		return fmt.Errorf("invalid input parts")
	}
	if a.orchestratorEngine == nil {
		return fmt.Errorf("orchestrator engine not initialized")
	}

	ctx = withTaskManager(ctx, manager)
	query := strings.TrimSpace(strings.Join(queryParts, "\n"))
	userID := ""
	if initialMsg.Metadata != nil {
		userID = strings.TrimSpace(fmt.Sprint(initialMsg.Metadata["user_id"]))
		if userID == "" || userID == "<nil>" {
			userID = strings.TrimSpace(fmt.Sprint(initialMsg.Metadata["userId"]))
		}
		if userID == "" || userID == "<nil>" {
			userID = strings.TrimSpace(fmt.Sprint(initialMsg.Metadata["UserID"]))
		}
		if userID == "<nil>" {
			userID = ""
		}
	}

	logger.Infof("[TRACE] lbshelper.ProcessInternal start task=%s query_len=%d", taskID, len(query))
	runID, err := a.orchestratorEngine.StartWorkflow(ctx, LBSHelperWorkflowID, map[string]any{
		"task_id": taskID,
		"query":   query,
		"text":    query,
		"input":   query,
		"user_id": userID,
	})
	if err != nil {
		return fmt.Errorf("failed to start lbshelper workflow: %w", err)
	}
	logger.Infof("[TRACE] lbshelper.ProcessInternal started task=%s run_id=%s", taskID, runID)
	stopProgress := a.startProgressReporter(ctx, taskID, runID, manager)
	defer stopProgress()
	runResult, err := a.orchestratorEngine.WaitRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("failed to wait lbshelper workflow: %w", err)
	}
	logger.Infof("[TRACE] lbshelper.ProcessInternal done task=%s run_state=%s err=%s", taskID, runResult.State, runResult.ErrorMessage)
	for _, nr := range runResult.NodeResults {
		logger.Infof("[TRACE] lbshelper.ProcessInternal node_result task=%s node=%s state=%s node_task=%s err=%s", taskID, nr.NodeID, nr.State, nr.TaskID, nr.ErrorMsg)
	}
	if runResult.State != orchestrator.RunStateSucceeded {
		if runResult.ErrorMessage != "" {
			return fmt.Errorf("lbshelper workflow failed: %s", runResult.ErrorMessage)
		}
		return fmt.Errorf("lbshelper workflow failed")
	}
	out, _ := runResult.FinalOutput["response"].(string)
	out = strings.TrimSpace(out)
	if out == "" {
		out = "Workflow executed successfully"
	}
	streamedFinal := lbsStreamedToUser(runResult)
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
	logger.Infof("[TRACE] lbshelper.node_input task=%s node=%s type=%s query_len=%d payload=%s", taskID, strings.TrimSpace(req.NodeID), req.NodeType, len(strings.TrimSpace(query)), snapshotAnyForLog(req.Payload, 2000))

	var (
		output map[string]any
		err    error
	)

	switch req.NodeType {
	case orchestrator.NodeTypeChatModel:
		output, err = w.agent.callChatModel(ctx, taskID, strings.TrimSpace(req.NodeID), query, req.NodeConfig, req.Payload)
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
		logger.Infof("[TRACE] lbshelper.node_error task=%s node=%s type=%s err=%v", taskID, strings.TrimSpace(req.NodeID), req.NodeType, err)
		return orchestrator.ExecutionResult{}, err
	}
	logger.Infof("[TRACE] lbshelper.node_output task=%s node=%s type=%s output=%s", taskID, strings.TrimSpace(req.NodeID), req.NodeType, snapshotAnyForLog(output, 2000))
	return orchestrator.ExecutionResult{Output: output}, nil
}

func (a *Agent) callChatModel(ctx context.Context, taskID string, nodeID string, query string, nodeCfg map[string]any, payload map[string]any) (map[string]any, error) {
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
	case "extract_path_and_tool":
		userQuery := extractLBSUserQuery(payload, query)
		if userQuery == "" {
			userQuery = query
		}
		finalPrompt = buildExtractRoutePrompt(userQuery, a.amapToolCatalog)
	case "summarize_route":
		summaryInput := extractLBSSummaryInput(payload, query)
		finalPrompt = buildSummaryPrompt(summaryInput)
	}

	logger.Infof("[TRACE] lbshelper.chatmodel start task=%s intent=%s model=%s url=%s api_key_set=%t query_len=%d", taskID, intent, model, baseURL, apiKey != "", len(finalPrompt))
	if baseURL == "" || model == "" {
		return nil, fmt.Errorf("chat_model config missing url/model")
	}

	a.emitSemanticStep(ctx, taskID, "lbshelper.llm.start", internalproto.StepStateInfo, "正在调用大模型："+nodeID)
	client := llm.NewClient(baseURL, apiKey)
	var streamBuf strings.Builder
	var pending strings.Builder
	lastEmitAt := time.Time{}
	streamToUser := intent == "summarize_route"
	streamedToUser := false
	flushToUser := func(force bool) {
		if !streamToUser || pending.Len() == 0 {
			return
		}
		if !force && pending.Len() < 48 {
			return
		}
		a.emitAssistantDelta(ctx, taskID, pending.String())
		pending.Reset()
		streamedToUser = true
	}
	resp, err := client.ChatCompletionStream(ctx, model, []llm.Message{{Role: "user", Content: finalPrompt}}, nil, nil, func(delta string) error {
		if strings.TrimSpace(delta) == "" {
			return nil
		}
		streamBuf.WriteString(delta)
		if streamToUser {
			pending.WriteString(delta)
			flushToUser(false)
		}
		if !lastEmitAt.IsZero() && time.Since(lastEmitAt) < 150*time.Millisecond {
			return nil
		}
		lastEmitAt = time.Now()
		a.emitSemanticStep(ctx, taskID, "lbshelper.llm.delta", internalproto.StepStateInfo, "正在调用大模型："+truncateText(streamBuf.String(), 140))
		return nil
	})
	if err == nil {
		flushToUser(true)
	}
	if err != nil {
		return nil, err
	}
	resp = strings.TrimSpace(resp)
	if resp == "" {
		resp = "(empty LLM response)"
	}
	a.emitSemanticStep(ctx, taskID, "lbshelper.llm.end", internalproto.StepStateEnd, "完成：大模型处理")
	logger.Infof("[TRACE] lbshelper.chatmodel done task=%s intent=%s resp_len=%d", taskID, intent, len(resp))
	if intent == "extract_path_and_tool" {
		toolCall := extractToolCall(resp)
		toolName := strings.TrimSpace(fmt.Sprint(toolCall["tool_name"]))
		if toolName == "" {
			toolName = "(未指定)"
		}
		a.emitInfoStep(ctx, taskID, "llm", "lbshelper.extract.result", fmt.Sprintf("提取结果：建议工具=%s", toolName))
	}
	if intent == "summarize_route" {
		a.emitInfoStep(ctx, taskID, "llm", "lbshelper.summary.result", "已完成行程方案生成")
	}

	output := map[string]any{"response": resp}
	if streamedToUser {
		output["streamed_to_user"] = true
	}
	return output, nil
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

	params := map[string]any{}
	if nodeCfg != nil {
		if m, ok := nodeCfg["params"].(map[string]any); ok {
			for k, v := range m {
				params[k] = v
			}
		}
	}

	toolCall := map[string]any{}
	userQuery := extractLBSUserQuery(payload, query)
	if userQuery == "" {
		userQuery = strings.TrimSpace(query)
	}
	if extractOut, ok := payload["N_extract"].(map[string]any); ok {
		if raw := strings.TrimSpace(fmt.Sprint(extractOut["response"])); raw != "" {
			toolCall = extractToolCall(raw)
		}
	}
	if len(toolCall) == 0 {
		toolCall = extractToolCall(userQuery)
	}
	if q, ok := toolCall["query"].(string); ok && strings.TrimSpace(q) != "" {
		params["query"] = strings.TrimSpace(q)
	} else {
		params["query"] = userQuery
	}
	if tn, ok := toolCall["tool_name"].(string); ok && strings.TrimSpace(tn) != "" {
		params["tool_name"] = strings.TrimSpace(tn)
	}
	if args, ok := toolCall["arguments"].(map[string]any); ok && len(args) > 0 {
		params["arguments"] = args
	}

	if _, ok := params["tool_name"]; !ok {
		params["tool_name"] = ""
	}
	if _, ok := params["arguments"]; !ok {
		params["arguments"] = map[string]any{}
	}
	// If arguments not provided, ask LLM to generate arguments based on discovered tool schema
	params = normalizeAmapCallParams(params, userQuery)
	// If arguments empty and we have schema info for the selected tool, generate via LLM
	if args, _ := params["arguments"].(map[string]any); len(args) == 0 {
		if tn, _ := params["tool_name"].(string); strings.TrimSpace(tn) != "" {
			if ti := a.findAmapToolInfo(strings.TrimSpace(tn)); ti != nil {
				if gen := a.generateAmapArguments(ctx, taskID, *ti, userQuery); gen != nil {
					params["arguments"] = gen
				}
			}
		}
	}
	params["task_id"] = taskID

	tool, err := a.findToolByName(toolName)
	if err != nil {
		return nil, err
	}

	plan := buildAmapCallPlanFromModel(toolCall, params, userQuery)
	a.emitInfoStep(ctx, taskID, "tool", "lbshelper.amap.plan", fmt.Sprintf("AMap 调用计划：共 %d 次", len(plan)))

	callResults := make([]map[string]any, 0, len(plan))
	var primaryResult map[string]any
	for i, call := range plan {
		call["task_id"] = taskID
		logger.Infof("[TRACE] lbshelper.amap request task=%s idx=%d/%d tool=%v args=%v", taskID, i+1, len(plan), call["tool_name"], call["arguments"])
		a.emitAmapInfoStep(ctx, taskID, call, i+1, len(plan))

		out, execErr := a.executeAmapWithFallback(ctx, tool, taskID, call)
		if execErr != nil {
			logger.Infof("[TRACE] lbshelper.amap error task=%s idx=%d/%d tool=%v err=%v", taskID, i+1, len(plan), call["tool_name"], execErr)
			a.emitInfoStep(ctx, taskID, "tool", "lbshelper.amap.call_error", fmt.Sprintf("第 %d/%d 次调用失败：%v", i+1, len(plan), execErr))
			callResults = append(callResults, map[string]any{
				"index":     i + 1,
				"tool_name": call["tool_name"],
				"arguments": call["arguments"],
				"error":     execErr.Error(),
			})
			continue
		}

		if primaryResult == nil {
			primaryResult = out
		}
		logger.Infof("[TRACE] lbshelper.amap result task=%s idx=%d/%d snippet=%s", taskID, i+1, len(plan), summarizeAmapResult(out))
		summary := summarizeAmapCallData(out)
		a.emitInfoStep(ctx, taskID, "tool", "lbshelper.amap.call_result", fmt.Sprintf("第 %d/%d 次结果：%s", i+1, len(plan), summary))

		callResults = append(callResults, map[string]any{
			"index":     i + 1,
			"tool_name": call["tool_name"],
			"arguments": call["arguments"],
			"output":    out,
			"summary":   summary,
		})
	}

	aggregated := map[string]any{
		"query":        strings.TrimSpace(fmt.Sprint(params["query"])),
		"calls":        callResults,
		"primary_tool": strings.TrimSpace(fmt.Sprint(params["tool_name"])),
	}
	if primaryResult != nil {
		aggregated["primary_result"] = primaryResult
	}
	a.emitInfoStep(ctx, taskID, "tool", "lbshelper.amap.done", fmt.Sprintf("AMap 调用完成：成功/失败共 %d 次", len(callResults)))

	b, _ := json.Marshal(aggregated)
	resp := strings.TrimSpace(string(b))
	if resp == "" {
		resp = "(empty tool response)"
	}
	return map[string]any{"response": resp, "result": aggregated}, nil
}

func withTaskManager(ctx context.Context, m internaltm.Manager) context.Context {
	if ctx == nil || m == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyTaskManager{}, m)
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
	ev := internalproto.NewStepEvent("lbshelper", "semantic", strings.TrimSpace(name), state, strings.TrimSpace(message))
	token, err := internalproto.EncodeStepToken(ev)
	if err != nil {
		return
	}
	_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateWorking, &internalproto.Message{
		Role:  internalproto.MessageRoleAgent,
		Parts: []internalproto.Part{internalproto.NewTextPart(token)},
	})
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
					if id == "" {
						continue
					}
					if !started[id] {
						started[id] = true
					}
					if finished[id] {
						continue
					}
					if stepState, ok := toTerminalStepState(nr.State); ok {
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
	messageZh := lbsHelperNodeProgressText[nodeID]
	if messageZh == "" {
		messageZh = fmt.Sprintf("执行节点 %s", nodeID)
	}
	if state == internalproto.StepStateEnd {
		messageZh = "完成：" + messageZh
	}
	if state == internalproto.StepStateError {
		messageZh = "失败：" + messageZh
	}
	ev := internalproto.NewStepEvent("lbshelper", "workflow", nodeID, state, messageZh)
	token, err := internalproto.EncodeStepToken(ev)
	if err != nil {
		return
	}
	_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateWorking, &internalproto.Message{
		Role:  internalproto.MessageRoleAgent,
		Parts: []internalproto.Part{internalproto.NewTextPart(token)},
	})
}

func (a *Agent) emitAmapInfoStep(ctx context.Context, taskID string, params map[string]any, idx int, total int) {
	manager := taskManagerFromContext(ctx)
	if manager == nil {
		return
	}
	toolName := strings.TrimSpace(fmt.Sprint(params["tool_name"]))
	argText := ""
	if args, ok := params["arguments"].(map[string]any); ok && len(args) > 0 {
		if b, err := json.Marshal(args); err == nil {
			argText = strings.TrimSpace(string(b))
		}
	}
	if argText == "" {
		argText = "{}"
	}
	if len(argText) > 240 {
		argText = argText[:240] + "...(truncated)"
	}
	prefix := ""
	if idx > 0 && total > 0 {
		prefix = fmt.Sprintf("第 %d/%d 次，", idx, total)
	}
	message := fmt.Sprintf("%s调用 AMap 子工具：%s，参数：%s", prefix, toolName, argText)
	ev := internalproto.NewStepEvent("lbshelper", "tool", "lbshelper.amap.call", internalproto.StepStateInfo, message)
	token, err := internalproto.EncodeStepToken(ev)
	if err != nil {
		return
	}
	_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateWorking, &internalproto.Message{
		Role:  internalproto.MessageRoleAgent,
		Parts: []internalproto.Part{internalproto.NewTextPart(token)},
	})
}

func (a *Agent) emitInfoStep(ctx context.Context, taskID string, phase string, name string, message string) {
	manager := taskManagerFromContext(ctx)
	if manager == nil {
		return
	}
	ev := internalproto.NewStepEvent("lbshelper", strings.TrimSpace(phase), strings.TrimSpace(name), internalproto.StepStateInfo, strings.TrimSpace(message))
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
	if strings.TrimSpace(text) == "" {
		return
	}
	_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateWorking, &internalproto.Message{
		Role:  internalproto.MessageRoleAgent,
		Parts: []internalproto.Part{internalproto.NewTextPart(text)},
	})
}

func lbsStreamedToUser(runResult orchestrator.RunResult) bool {
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

func toTerminalStepState(state orchestrator.TaskState) (internalproto.StepState, bool) {
	switch state {
	case orchestrator.TaskStateSucceeded:
		return internalproto.StepStateEnd, true
	case orchestrator.TaskStateFailed, orchestrator.TaskStateCanceled:
		return internalproto.StepStateError, true
	default:
		return "", false
	}
}

func buildLBSHelperWorkflow() (*orchestrator.Workflow, error) {
	wf, err := orchestrator.NewWorkflow(LBSHelperWorkflowID, "lbshelper route workflow")
	if err != nil {
		return nil, err
	}

	if err = wf.AddNode(orchestrator.Node{ID: "N_start", Type: orchestrator.NodeTypeStart}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{
		ID:       "N_extract",
		Type:     orchestrator.NodeTypeChatModel,
		AgentID:  LBSHelperWorkflowWorkerID,
		TaskType: "chat_model",
		Config: map[string]any{
			"intent": "extract_path_and_tool",
		},
		PreInput: "提取用户问题中的路径规划文本，并产出 amap MCP 的调用 JSON。",
	}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{
		ID:       "N_amap",
		Type:     orchestrator.NodeTypeTool,
		AgentID:  LBSHelperWorkflowWorkerID,
		TaskType: LBSHelperDefaultTaskType,
		Config: map[string]any{
			"tool_name": "amap",
		},
	}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{
		ID:       "N_summary",
		Type:     orchestrator.NodeTypeChatModel,
		AgentID:  LBSHelperWorkflowWorkerID,
		TaskType: "chat_model",
		Config: map[string]any{
			"intent": "summarize_route",
		},
		PreInput: "分析并整理 amap 工具返回结果。",
	}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{ID: "N_end", Type: orchestrator.NodeTypeEnd}); err != nil {
		return nil, err
	}

	if err = wf.AddEdge("N_start", "N_extract"); err != nil {
		return nil, err
	}
	if err = wf.AddEdge("N_extract", "N_amap"); err != nil {
		return nil, err
	}
	if err = wf.AddEdge("N_amap", "N_summary"); err != nil {
		return nil, err
	}
	if err = wf.AddEdge("N_summary", "N_end"); err != nil {
		return nil, err
	}

	return wf, nil
}
