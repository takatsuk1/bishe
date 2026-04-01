package host

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
	HostWorkflowID       = "host-default"
	HostWorkflowWorkerID = "host_worker"
	HostDefaultTaskType  = "host_default"
)

type ctxKeyTaskManager struct{}

type Agent struct {
	orchestratorEngine orchestrator.Engine
	llmClient          *llm.Client
	chatModel          string
	agentInfoManager   *tools.AgentInfoManager
	AgentInfoTool      tools.Tool
	CallAgentTool      tools.Tool
}

type workflowNodeWorker struct {
	agent *Agent
}

type stepReporter struct {
	agent   string
	taskID  string
	manager internaltm.Manager
}

var hostNodeProgressText = map[string]string{
	"N_start":         "初始化主控任务",
	"N_agent_info":    "读取可用 Agent 列表",
	"N_route":         "判断是否需要调用子 Agent",
	"N_condition":     "执行路由分支判断",
	"N_direct_answer": "直接生成答复",
	"N_call_agent":    "调用目标 Agent 处理请求",
	"N_end":           "输出最终结果",
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
		agent.chatModel = "qwen3.5-flash"
	}
	logger.Infof("[TRACE] host llm_config url=%s model=%s api_key_set=%t", strings.TrimSpace(cfg.LLM.URL), agent.chatModel, strings.TrimSpace(cfg.LLM.APIKey) != "")

	mgr, err := tools.NewAgentInfoManager(context.Background(), cfg)
	if err != nil {
		return nil, err
	}
	agent.agentInfoManager = mgr

	agent.AgentInfoTool = tools.NewRawHTTPTool(
		"agent_info",
		"查询当前系统可用的 Agent 列表与能力说明",
		[]tools.ToolParameter{},
		func(ctx context.Context, params map[string]any) (map[string]any, error) {
			_ = ctx
			_ = params
			infos := agent.agentInfoManager.GetAgentInfos()
			items := make([]map[string]any, 0, len(infos))
			for _, info := range infos {
				items = append(items, map[string]any{
					"name":        info.Name,
					"description": info.Description,
					"skills":      info.Skills,
				})
			}
			return map[string]any{"agents": items}, nil
		},
	)

	callTool, err := tools.NewCallAgentTool(context.Background(), cfg, nil)
	if err != nil {
		return nil, err
	}
	agent.CallAgentTool = callTool

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
	if err = agent.orchestratorEngine.RegisterWorker(orchestrator.AgentDescriptor{
		ID:           HostWorkflowWorkerID,
		Name:         "host workflow worker",
		Capabilities: []orchestrator.AgentCapability{"chat_model", "tool", "host"},
	}, &workflowNodeWorker{agent: agent}); err != nil {
		return nil, err
	}

	wf, err := buildHostWorkflow()
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
	apiKey := ""
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
		apiKey = strings.TrimSpace(fmt.Sprint(initialMsg.Metadata["api_key"]))
	}

	logger.Infof("[TRACE] host.ProcessInternal start task=%s query_len=%d", taskID, len(query))
	runID, err := a.orchestratorEngine.StartWorkflow(ctx, HostWorkflowID, map[string]any{
		"task_id": taskID,
		"query":   query,
		"text":    query,
		"input":   query,
		"user_id": userID,
		"api_key": apiKey,
	})
	if err != nil {
		return fmt.Errorf("failed to start host workflow: %w", err)
	}
	logger.Infof("[TRACE] host.ProcessInternal started task=%s run_id=%s", taskID, runID)
	stopProgress := a.startProgressReporter(ctx, taskID, runID, manager)
	defer stopProgress()
	runResult, err := a.orchestratorEngine.WaitRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("failed to wait host workflow: %w", err)
	}
	logger.Infof("[TRACE] host.ProcessInternal done task=%s run_state=%s err=%s", taskID, runResult.State, runResult.ErrorMessage)
	for _, nr := range runResult.NodeResults {
		logger.Infof("[TRACE] host.ProcessInternal node_result task=%s node=%s state=%s node_task=%s err=%s", taskID, nr.NodeID, nr.State, nr.TaskID, nr.ErrorMsg)
	}
	if runResult.State != orchestrator.RunStateSucceeded {
		if runResult.ErrorMessage != "" {
			return fmt.Errorf("host workflow failed: %s", runResult.ErrorMessage)
		}
		return fmt.Errorf("host workflow failed")
	}
	out, _ := runResult.FinalOutput["response"].(string)
	out = strings.TrimSpace(out)
	if out == "" {
		out = "Workflow executed successfully"
	}
	if manager != nil {
		_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateCompleted, &internalproto.Message{
			Role:  internalproto.MessageRoleAgent,
			Parts: []internalproto.Part{internalproto.NewTextPart(out)},
		})
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
	agentInfo := extractAgentInfoPayload(payload)
	switch intent {
	case "route_agent":
		finalPrompt = buildRoutePrompt(query, agentInfo)
	case "direct_answer":
		finalPrompt = buildDirectAnswerPrompt(query, agentInfo)
	}

	logger.Infof("[TRACE] host.chatmodel start task=%s intent=%s model=%s url=%s api_key_set=%t query_len=%d", taskID, intent, model, baseURL, apiKey != "", len(finalPrompt))
	if baseURL == "" || model == "" {
		return nil, fmt.Errorf("chat_model config missing url/model")
	}

	resp, err := llm.NewClient(baseURL, apiKey).ChatCompletion(ctx, model, []llm.Message{{Role: "user", Content: finalPrompt}}, nil, nil)
	if err != nil {
		return nil, err
	}
	resp = strings.TrimSpace(resp)
	if resp == "" {
		resp = "(empty LLM response)"
	}
	if intent == "route_agent" {
		resp = normalizeRouteDecision(resp)
	}
	logger.Infof("[TRACE] host.chatmodel done task=%s intent=%s resp_len=%d", taskID, intent, len(resp))

	return map[string]any{"response": resp}, nil
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

	tool, err := a.findToolByName(toolName)
	if err != nil {
		return nil, err
	}

	if strings.EqualFold(toolName, "call_agent") {
		infoOut := extractAgentInfoPayload(payload)
		if len(infoOut) == 0 {
			agentInfoTool, e := a.findToolByName("agent_info")
			if e != nil {
				return nil, e
			}
			infoOut, e = agentInfoTool.Execute(ctx, map[string]any{})
			if e != nil {
				return nil, e
			}
		}
		allowedAgents := collectAllowedAgents(infoOut)
		if len(allowedAgents) == 0 {
			return nil, fmt.Errorf("no available agents from agent_info")
		}

		agentName := resolveAllowedAgentName(query, allowedAgents)
		if agentName == "" || strings.EqualFold(agentName, "false") {
			return nil, fmt.Errorf("invalid agent name from routing model")
		}

		text := strings.TrimSpace(fmt.Sprint(payload["text"]))
		if text == "" {
			text = strings.TrimSpace(fmt.Sprint(payload["input"]))
		}
		if text == "" {
			text = strings.TrimSpace(fmt.Sprint(payload["query"]))
		}

		params := map[string]any{
			"agent_name":     agentName,
			"text":           text,
			"allowed_agents": stringSliceToAnySlice(allowedAgents),
			"task_id":        taskID,
		}
		if uid := strings.TrimSpace(fmt.Sprint(payload["user_id"])); uid != "" {
			params["user_id"] = uid
		}
		if token := strings.TrimSpace(fmt.Sprint(payload["api_key"])); token != "" {
			params["api_key"] = token
		}

		logger.Infof("[TRACE] host.call_agent request task=%s agent=%s", taskID, agentName)
		out, e := tool.Execute(ctx, params)
		if e != nil {
			return nil, e
		}
		resp := strings.TrimSpace(fmt.Sprintf("%v", out))
		if s, ok := out["response"].(string); ok && strings.TrimSpace(s) != "" {
			resp = strings.TrimSpace(s)
		}
		if resp == "" {
			resp = "(empty call_agent response)"
		}
		return map[string]any{"response": resp, "result": out}, nil
	}

	out, err := tool.Execute(ctx, map[string]any{})
	if err != nil {
		return nil, err
	}
	resp := strings.TrimSpace(fmt.Sprintf("%v", out))
	if resp == "" {
		resp = "(empty tool response)"
	}
	return map[string]any{"response": resp, "result": out}, nil
}

func (a *Agent) findToolByName(name string) (tools.Tool, error) {
	switch strings.TrimSpace(name) {
	case "agent_info":
		if a.AgentInfoTool == nil {
			return nil, fmt.Errorf("tool agent_info is not initialized")
		}
		return a.AgentInfoTool, nil
	case "call_agent":
		if a.CallAgentTool == nil {
			return nil, fmt.Errorf("tool call_agent is not initialized")
		}
		return a.CallAgentTool, nil
	default:
		return nil, fmt.Errorf("tool %s not found", name)
	}
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
					messageZh := hostNodeProgressText[nodeID]
					if nodeID == "N_agent_info" {
						messageZh = "读取可用 Agent 列表"
					}
					if messageZh == "" {
						messageZh = fmt.Sprintf("执行节点 %s", nodeID)
					}
					ev := internalproto.NewStepEvent("host", "workflow", nodeID, internalproto.StepStateStart, messageZh)
					text := messageZh
					if token, tokenErr := internalproto.EncodeStepToken(ev); tokenErr == nil {
						text = messageZh + "\n" + token
					}
					_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateWorking, &internalproto.Message{
						Role:  internalproto.MessageRoleAgent,
						Parts: []internalproto.Part{internalproto.NewTextPart(text)},
					})
				}
				for _, nr := range run.NodeResults {
					id := strings.TrimSpace(nr.NodeID)
					if id == "" || finished[id] {
						continue
					}
					stepState, ok := hostToTerminalStepState(nr.State)
					if !ok {
						continue
					}
					finished[id] = true
					messageZh := hostNodeProgressText[id]
					if id == "N_agent_info" {
						messageZh = "读取可用 Agent 列表"
					}
					if messageZh == "" {
						messageZh = fmt.Sprintf("节点结束 %s", id)
					}
					if stepState == internalproto.StepStateEnd {
						messageZh = "完成：" + messageZh
					}
					if stepState == internalproto.StepStateError {
						messageZh = "失败：" + messageZh
					}
					ev := internalproto.NewStepEvent("host", "workflow", id, stepState, messageZh)
					if token, tokenErr := internalproto.EncodeStepToken(ev); tokenErr == nil {
						_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateWorking, &internalproto.Message{
							Role:  internalproto.MessageRoleAgent,
							Parts: []internalproto.Part{internalproto.NewTextPart(token)},
						})
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

func hostToTerminalStepState(state orchestrator.TaskState) (internalproto.StepState, bool) {
	switch state {
	case orchestrator.TaskStateSucceeded:
		return internalproto.StepStateEnd, true
	case orchestrator.TaskStateFailed, orchestrator.TaskStateCanceled:
		return internalproto.StepStateError, true
	default:
		return "", false
	}
}

func buildRoutePrompt(userQuery string, agentInfo map[string]any) string {
	var sb strings.Builder
	sb.WriteString("你是 Host 路由助手。\n")
	if names := collectAllowedAgents(agentInfo); len(names) > 0 {
		sb.WriteString("当前可调用 agent 列表: ")
		sb.WriteString(strings.Join(names, ", "))
		sb.WriteString("\n")
	}
	sb.WriteString("任务：判断是否需要调用其他 agent。\n")
	sb.WriteString("输出规则:\n")
	sb.WriteString("1. 不需要调用任何 agent 时，只输出 false。\n")
	sb.WriteString("2. 需要调用时，只输出目标 agent 名称。\n")
	sb.WriteString("3. 不要输出解释和额外文本。\n")
	sb.WriteString("用户问题:\n")
	sb.WriteString(userQuery)
	return sb.String()
}

func buildDirectAnswerPrompt(userQuery string, agentInfo map[string]any) string {
	var sb strings.Builder
	sb.WriteString("你是通用中文助手。\n")
	if names := collectAllowedAgents(agentInfo); len(names) > 0 {
		sb.WriteString("你知道当前可调用 agent 列表: ")
		sb.WriteString(strings.Join(names, ", "))
		sb.WriteString("。\n")
	}
	sb.WriteString("如果问题不需要调用其他 agent，请直接回答用户。\n")
	sb.WriteString("用户问题:\n")
	sb.WriteString(userQuery)
	return sb.String()
}

func normalizeRouteDecision(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "false"
	}
	lower := strings.ToLower(s)
	if lower == "false" || strings.Contains(lower, "不需要") || strings.Contains(lower, "无需") || strings.Contains(lower, "不用") {
		return "false"
	}

	if strings.Contains(s, "\n") {
		s = strings.TrimSpace(strings.Split(s, "\n")[0])
	}
	// Keep the sentence for later dynamic matching against allowed agent names.
	s = strings.Trim(s, "\"'` ")
	if s == "" {
		return "false"
	}
	return s
}

func resolveAllowedAgentName(raw string, allowedAgents []string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	lower := strings.ToLower(raw)

	for _, name := range allowedAgents {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(name)) {
			return name
		}
	}

	token := raw
	if i := strings.IndexAny(token, " \t\n,.;:()[]{}\"'`，。；：！？（）【】"); i >= 0 {
		token = strings.TrimSpace(token[:i])
	}
	for _, name := range allowedAgents {
		if strings.EqualFold(token, strings.TrimSpace(name)) {
			return strings.TrimSpace(name)
		}
	}

	return ""
}

func collectAllowedAgents(agentInfoOut map[string]any) []string {
	result := make([]string, 0)
	seen := map[string]struct{}{}

	appendName := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		result = append(result, name)
	}

	agentsRaw, ok := agentInfoOut["agents"]
	if !ok {
		return result
	}

	switch arr := agentsRaw.(type) {
	case []map[string]any:
		for _, item := range arr {
			appendName(fmt.Sprint(item["name"]))
		}
	case []any:
		for _, v := range arr {
			if m, ok := v.(map[string]any); ok {
				appendName(fmt.Sprint(m["name"]))
			}
		}
	default:
		b, err := json.Marshal(arr)
		if err == nil {
			var generic []map[string]any
			if unmarshalErr := json.Unmarshal(b, &generic); unmarshalErr == nil {
				for _, m := range generic {
					appendName(fmt.Sprint(m["name"]))
				}
			}
		}
	}

	return result
}

func stringSliceToAnySlice(in []string) []any {
	out := make([]any, 0, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}

func buildRoutePromptV2(userQuery string, agentInfo map[string]any) string {
	var sb strings.Builder
	sb.WriteString("你是 Host 路由助手。任务：判断是否需要调用其他 agent。\n")
	if names := collectAllowedAgents(agentInfo); len(names) > 0 {
		sb.WriteString("当前可调用 agent 列表: ")
		sb.WriteString(strings.Join(names, ", "))
		sb.WriteString("\n")
	}
	sb.WriteString("输出规则:\n")
	sb.WriteString("1. 如果不需要调用任何 agent，只输出 false。\n")
	sb.WriteString("2. 如果需要调用，只输出目标 agent 名称。\n")
	sb.WriteString("3. 禁止输出解释或额外文本。\n")
	sb.WriteString("用户问题:\n")
	sb.WriteString(userQuery)
	return sb.String()
}

func buildDirectAnswerPromptV2(userQuery string, agentInfo map[string]any) string {
	var sb strings.Builder
	sb.WriteString("你是通用中文助手。\n")
	if names := collectAllowedAgents(agentInfo); len(names) > 0 {
		sb.WriteString("你掌握当前可调用 agent 列表: ")
		sb.WriteString(strings.Join(names, ", "))
		sb.WriteString("。当用户询问可调用哪些 agent 时，请准确回答。\n")
	}
	sb.WriteString("如果问题无需调用其他 agent，请直接给出清晰、简洁、有帮助的回答。\n")
	sb.WriteString("用户问题:\n")
	sb.WriteString(userQuery)
	return sb.String()
}

func extractAgentInfoPayload(payload map[string]any) map[string]any {
	if len(payload) == 0 {
		return map[string]any{}
	}
	node, _ := payload["N_agent_info"].(map[string]any)
	if len(node) == 0 {
		return map[string]any{}
	}
	if result, ok := node["result"].(map[string]any); ok && len(result) > 0 {
		return result
	}
	if agents, ok := node["agents"]; ok {
		return map[string]any{"agents": agents}
	}
	return map[string]any{}
}

func buildHostWorkflow() (*orchestrator.Workflow, error) {
	wf, err := orchestrator.NewWorkflow(HostWorkflowID, "host default workflow")
	if err != nil {
		return nil, err
	}

	if err = wf.AddNode(orchestrator.Node{ID: "N_start", Type: orchestrator.NodeTypeStart}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{
		ID:       "N_agent_info",
		Type:     orchestrator.NodeTypeTool,
		AgentID:  HostWorkflowWorkerID,
		TaskType: HostDefaultTaskType,
		Config: map[string]any{
			"tool_name": "agent_info",
		},
	}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{
		ID:       "N_route",
		Type:     orchestrator.NodeTypeChatModel,
		AgentID:  HostWorkflowWorkerID,
		TaskType: "chat_model",
		Config: map[string]any{
			"intent": "route_agent",
		},
		PreInput: "判断是否调用其他 agent。不调用则返回 false，调用则返回 agentName。",
	}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{
		ID:   "N_condition",
		Type: orchestrator.NodeTypeCondition,
		Config: map[string]any{
			"left_type":   "path",
			"left_value":  "N_route.response",
			"operator":    "eq",
			"right_type":  "value",
			"right_value": "false",
		},
		Metadata: map[string]string{
			"true_to":  "N_direct_answer",
			"false_to": "N_call_agent",
		},
	}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{
		ID:       "N_direct_answer",
		Type:     orchestrator.NodeTypeChatModel,
		AgentID:  HostWorkflowWorkerID,
		TaskType: "chat_model",
		Config: map[string]any{
			"intent": "direct_answer",
		},
		PreInput: "无需调用 agent 时，直接回答用户问题。",
	}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{
		ID:       "N_call_agent",
		Type:     orchestrator.NodeTypeTool,
		AgentID:  HostWorkflowWorkerID,
		TaskType: HostDefaultTaskType,
		Config: map[string]any{
			"tool_name": "call_agent",
		},
	}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{ID: "N_end", Type: orchestrator.NodeTypeEnd}); err != nil {
		return nil, err
	}

	if err = wf.AddEdge("N_start", "N_agent_info"); err != nil {
		return nil, err
	}
	if err = wf.AddEdge("N_agent_info", "N_route"); err != nil {
		return nil, err
	}
	if err = wf.AddEdge("N_route", "N_condition"); err != nil {
		return nil, err
	}
	if err = wf.AddEdgeWithLabel("N_condition", "N_direct_answer", "true", nil); err != nil {
		return nil, err
	}
	if err = wf.AddEdgeWithLabel("N_condition", "N_call_agent", "false", nil); err != nil {
		return nil, err
	}
	if err = wf.AddEdge("N_direct_answer", "N_end"); err != nil {
		return nil, err
	}
	if err = wf.AddEdge("N_call_agent", "N_end"); err != nil {
		return nil, err
	}

	return wf, nil
}
