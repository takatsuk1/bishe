package deepresearch

import (
	"ai/config"
	"ai/pkg/agentfmt"
	"ai/pkg/llm"
	"ai/pkg/logger"
	"ai/pkg/monitor"
	"ai/pkg/orchestrator"
	internalproto "ai/pkg/protocol"
	"ai/pkg/storage"
	internaltm "ai/pkg/taskmanager"
	"ai/pkg/tools"
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	DeepResearchWorkflowID       = "deepresearch-default"
	DeepResearchWorkflowWorkerID = "deepresearch_worker"
	DeepResearchDefaultTaskType  = "deepresearch_default"
)

type ctxKeyTaskManager struct{}

type Agent struct {
	orchestratorEngine orchestrator.Engine
	llmClient          *llm.Client
	chatModel          string
	tavilyAPIKey       string
	TavilyTool         tools.Tool
}

type workflowNodeWorker struct {
	agent *Agent
}

type stepReporter struct {
	agent   string
	taskID  string
	manager internaltm.Manager
}

var deepResearchNodeProgressText = map[string]string{
	"N_start":            "初始化研究任务",
	"N_loop":             "进入检索循环",
	"N_judge":            "评估当前信息是否足够",
	"N_condition":        "判断是否继续检索",
	"N_extract_keywords": "提取下一轮检索关键词",
	"N_tavily":           "调用 Tavily 联网检索",
	"N_end":              "整理并输出最终答案",
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
	agent.tavilyAPIKey = strings.TrimSpace(cfg.Tavily.APIKey)

	logger.Infof("[TRACE] deepresearch llm_config url=%s model=%s api_key_set=%t", strings.TrimSpace(cfg.LLM.URL), agent.chatModel, strings.TrimSpace(cfg.LLM.APIKey) != "")

	tavilyToolConfig := tools.HTTPToolConfig{
		Method:  "POST",
		URL:     "https://api.tavily.com/search",
		Timeout: 30 * time.Second,
	}
	tavilyToolConfig.Headers = map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer {{api_key}}",
	}
	tavilyToolConfig.BodyTemplate = "{\"query\":\"{{query}}\",\"search_depth\":\"{{search_depth}}\",\"max_results\":{{max_results}}}"
	agent.TavilyTool = tools.NewHTTPTool(
		"tavily",
		"调用 Tavily 搜索 API 进行实时检索",
		[]tools.ToolParameter{
			{Name: "api_key", Type: tools.ParamTypeString, Required: true, Description: "Tavily API Key"},
			{Name: "query", Type: tools.ParamTypeString, Required: true, Description: "检索关键词"},
			{Name: "search_depth", Type: tools.ParamTypeString, Required: false, Description: "检索深度，可选 basic/advanced"},
			{Name: "max_results", Type: tools.ParamTypeNumber, Required: false, Description: "返回结果数量上限"},
		},
		tavilyToolConfig,
	)

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
		logger.Infof("[TRACE] deepresearch monitor enabled")
	} else {
		logger.Infof("[TRACE] deepresearch monitor disabled: mysql unavailable")
	}

	agent.orchestratorEngine = orchestrator.NewEngine(engineCfg, orchestrator.NewInMemoryAgentRegistry())
	if err := agent.orchestratorEngine.RegisterWorker(orchestrator.AgentDescriptor{
		ID:           DeepResearchWorkflowWorkerID,
		Name:         "deepresearch workflow worker",
		Capabilities: []orchestrator.AgentCapability{"chat_model", "tool", "deepresearch"},
	}, &workflowNodeWorker{agent: agent}); err != nil {
		return nil, err
	}

	wf, err := buildDeepResearchWorkflow()
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

	logger.Infof("[TRACE] deepresearch.ProcessInternal start task=%s query_len=%d", taskID, len(query))
	runID, err := a.orchestratorEngine.StartWorkflow(ctx, DeepResearchWorkflowID, map[string]any{
		"task_id": taskID,
		"query":   query,
		"text":    query,
		"input":   query,
		"user_id": userID,
	})
	if err != nil {
		return fmt.Errorf("failed to start deepresearch workflow: %w", err)
	}
	logger.Infof("[TRACE] deepresearch.ProcessInternal started task=%s run_id=%s", taskID, runID)
	stopProgress := a.startProgressReporter(ctx, taskID, runID, manager)
	defer stopProgress()
	runResult, err := a.orchestratorEngine.WaitRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("failed to wait deepresearch workflow: %w", err)
	}
	logger.Infof("[TRACE] deepresearch.ProcessInternal done task=%s run_state=%s err=%s", taskID, runResult.State, runResult.ErrorMessage)
	for _, nr := range runResult.NodeResults {
		logger.Infof("[TRACE] deepresearch.ProcessInternal node_result task=%s node=%s state=%s node_task=%s err=%s", taskID, nr.NodeID, nr.State, nr.TaskID, nr.ErrorMsg)
	}
	if runResult.State != orchestrator.RunStateSucceeded {
		if runResult.ErrorMessage != "" {
			return fmt.Errorf("deepresearch workflow failed: %s", runResult.ErrorMessage)
		}
		return fmt.Errorf("deepresearch workflow failed")
	}
	out := ""
	streamedFinal := false
	if manager != nil {
		if streamed, err := a.streamStructuredResponseWithLLM(ctx, taskID, query, runResult.FinalOutput, manager); err == nil && strings.TrimSpace(streamed) != "" {
			out = streamed
			streamedFinal = true
		} else {
			if err != nil {
				logger.Warnf("[TRACE] deepresearch.stream_final failed task=%s err=%v", taskID, err)
			}
			out = a.buildStructuredResponse(ctx, taskID, query, runResult.FinalOutput)
		}
	} else {
		out = a.buildStructuredResponse(ctx, taskID, query, runResult.FinalOutput)
	}
	out = agentfmt.Clean(out)
	if out == "" {
		out = "Workflow executed successfully"
	}
	if manager != nil {
		finalText := out
		if streamedFinal {
			// Final content was already streamed via working updates; avoid duplicate append.
			finalText = ""
		}
		_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateCompleted, &internalproto.Message{
			Role:  internalproto.MessageRoleAgent,
			Parts: []internalproto.Part{internalproto.NewTextPart(finalText)},
		})
	}
	return nil
}

func (w *workflowNodeWorker) Execute(ctx context.Context, req orchestrator.ExecutionRequest) (orchestrator.ExecutionResult, error) {
	taskID, _ := req.Payload["task_id"].(string)
	query, _ := req.Payload["query"].(string)
	logger.Infof("[TRACE] deepresearch.node_input task=%s node=%s type=%s query_len=%d payload=%s", taskID, strings.TrimSpace(req.NodeID), req.NodeType, len(strings.TrimSpace(query)), snapshotAnyForLog(req.Payload, 2000))

	var (
		output map[string]any
		err    error
	)

	switch req.NodeType {
	case orchestrator.NodeTypeChatModel:
		queryForNode := query
		switch strings.TrimSpace(req.NodeID) {
		case "N_judge":
			queryForNode = buildJudgeQuery(req.Payload)
		case "N_extract_keywords":
			queryForNode = buildKeywordExtractionQuery(req.Payload)
		}
		output, err = w.agent.callChatModel(ctx, taskID, strings.TrimSpace(req.NodeID), queryForNode, req.NodeConfig, req.Payload)
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
		logger.Infof("[TRACE] deepresearch.node_error task=%s node=%s type=%s err=%v", taskID, strings.TrimSpace(req.NodeID), req.NodeType, err)
		return orchestrator.ExecutionResult{}, err
	}
	logger.Infof("[TRACE] deepresearch.node_output task=%s node=%s type=%s output=%s", taskID, strings.TrimSpace(req.NodeID), req.NodeType, snapshotAnyForLog(output, 2000))
	return orchestrator.ExecutionResult{Output: output}, nil
}

func (a *Agent) callChatModel(ctx context.Context, taskID string, nodeID string, query string, nodeCfg map[string]any, payload map[string]any) (map[string]any, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query is empty")
	}

	normalizeBool := false
	if nodeCfg != nil {
		if v, ok := nodeCfg["normalize_bool"].(bool); ok && v {
			normalizeBool = true
			if !hasSearchEvidence(payload) {
				logger.Infof("[TRACE] deepresearch.judge task=%s no_search_evidence force=false", taskID)
				return map[string]any{"response": "false"}, nil
			}
		}
	}

	baseURL := strings.TrimSpace(a.llmClient.BaseURL)
	apiKey := strings.TrimSpace(a.llmClient.APIKey)
	model := strings.TrimSpace(a.chatModel)
	if nodeCfg != nil {
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
	logger.Infof("[TRACE] deepresearch.chatmodel start task=%s model=%s url=%s api_key_set=%t query_len=%d", taskID, model, baseURL, apiKey != "", len(query))
	if baseURL == "" || model == "" {
		return nil, fmt.Errorf("chat_model config missing url/model")
	}
	nodeID = strings.TrimSpace(nodeID)

	client := llm.NewClient(baseURL, apiKey)
	var (
		resp string
		err  error
	)
	if normalizeBool {
		resp, err = client.ChatCompletion(ctx, model, []llm.Message{{Role: "user", Content: query}}, nil, nil)
	} else {
		streamingKeyword := nodeID == "N_extract_keywords"
		var keywordPreview strings.Builder
		lastKeywordEmit := time.Time{}
		if streamingKeyword {
			a.emitSemanticStep(ctx, taskID, "deepresearch.keyword.extract.start", internalproto.StepStateInfo, "正在调用大模型提取关键词：")
		}
		resp, err = client.ChatCompletionStream(ctx, model, []llm.Message{{Role: "user", Content: query}}, nil, nil, func(delta string) error {
			if !streamingKeyword || strings.TrimSpace(delta) == "" {
				return nil
			}
			keywordPreview.WriteString(delta)
			if !lastKeywordEmit.IsZero() && time.Since(lastKeywordEmit) < 120*time.Millisecond {
				return nil
			}
			lastKeywordEmit = time.Now()
			a.emitSemanticStep(ctx, taskID, "deepresearch.keyword.extract.delta", internalproto.StepStateInfo, "正在调用大模型提取关键词："+truncateText(keywordPreview.String(), 160))
			return nil
		})
		if streamingKeyword {
			if strings.TrimSpace(resp) != "" {
				a.emitSemanticStep(ctx, taskID, "deepresearch.keyword.extract.delta", internalproto.StepStateInfo, "正在调用大模型提取关键词："+truncateText(resp, 160))
			}
			a.emitSemanticStep(ctx, taskID, "deepresearch.keyword.extract.end", internalproto.StepStateEnd, "完成：关键词提取")
		}
	}
	if err != nil {
		return nil, err
	}
	resp = strings.TrimSpace(resp)
	if resp == "" {
		resp = "(empty LLM response)"
	}
	if normalizeBool {
		resp = normalizeBoolResponse(resp)
	}
	logger.Infof("[TRACE] deepresearch.chatmodel done task=%s resp_len=%d", taskID, len(resp))

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

	params := map[string]any{}
	if nodeCfg != nil {
		if m, ok := nodeCfg["params"].(map[string]any); ok {
			for k, v := range m {
				params[k] = v
			}
		}
	}
	if _, ok := params["query"]; !ok {
		params["query"] = strings.TrimSpace(query)
	}
	if _, ok := params["task_id"]; !ok {
		params["task_id"] = taskID
	}
	if strings.EqualFold(toolName, "tavily") {
		keywordFromExtract := false
		if extractOut, ok := payload["N_extract_keywords"].(map[string]any); ok {
			if extracted := strings.TrimSpace(fmt.Sprint(extractOut["response"])); extracted != "" {
				params["query"] = extracted
				keywordFromExtract = true
			}
		}
		if _, ok := params["api_key"]; !ok {
			params["api_key"] = a.tavilyAPIKey
		}
		if _, ok := params["search_depth"]; !ok {
			params["search_depth"] = "basic"
		}
		if _, ok := params["max_results"]; !ok {
			params["max_results"] = 5
		}
		q := strings.TrimSpace(fmt.Sprint(params["query"]))
		orig := extractOriginalQuestion(payload)
		q = trimForTavilyQuery(q)
		if !keywordFromExtract {
			q = anchorSearchQuery(q, orig)
		}
		// Ensure final query respects Tavily max length (400 chars).
		q = trimForTavilyQuery(q)
		params["query"] = q
		round := extractLoopRound(payload)
		if round > 0 {
			logger.Infof("[TRACE] deepresearch.search round=%d task=%s keyword=%q", round, taskID, q)
		} else {
			logger.Infof("[TRACE] deepresearch.search task=%s keyword=%q", taskID, q)
		}
		a.emitSemanticStep(ctx, taskID, "deepresearch.search.start", internalproto.StepStateInfo, "正在搜索内容：关键词："+q)
		logger.Infof("[TRACE] deepresearch.tavily request task=%s query_len=%d query=%q", taskID, len(q), q)
	}

	tool, err := a.findToolByName(toolName)
	if err != nil {
		return nil, err
	}
	out, err := tool.Execute(ctx, params)
	if err != nil {
		return nil, err
	}
	resp := summarizeToolResponse(out)
	if strings.EqualFold(toolName, "tavily") {
		a.emitSemanticStep(ctx, taskID, "deepresearch.search.end", internalproto.StepStateEnd, "搜索完成："+summarizeSearchPreview(out))
	}
	return map[string]any{"response": resp, "result": out}, nil
}

func (a *Agent) findToolByName(name string) (tools.Tool, error) {
	switch strings.TrimSpace(name) {
	case "tavily":
		if a.TavilyTool == nil {
			return nil, fmt.Errorf("tool tavily is not initialized")
		}
		return a.TavilyTool, nil
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
					a.emitStepEvent(ctx, manager, taskID, nodeID, internalproto.StepStateStart)
				}

				for _, nr := range run.NodeResults {
					id := strings.TrimSpace(nr.NodeID)
					if id == "" {
						continue
					}
					if !started[id] {
						// Avoid replaying historical start events late; only current-node transitions emit start.
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
	messageZh := deepResearchNodeProgressText[nodeID]
	if messageZh == "" {
		messageZh = fmt.Sprintf("执行节点 %s", nodeID)
	}
	if state == internalproto.StepStateEnd {
		messageZh = "完成：" + messageZh
	}
	if state == internalproto.StepStateError {
		messageZh = "失败：" + messageZh
	}
	ev := internalproto.NewStepEvent("deepresearch", "workflow", nodeID, state, messageZh)
	token, err := internalproto.EncodeStepToken(ev)
	if err != nil {
		return
	}
	_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateWorking, &internalproto.Message{
		Role:  internalproto.MessageRoleAgent,
		Parts: []internalproto.Part{internalproto.NewTextPart(token)},
	})
}

func (a *Agent) emitSemanticStep(ctx context.Context, taskID string, name string, state internalproto.StepState, message string) {
	manager := taskManagerFromContext(ctx)
	if manager == nil {
		return
	}
	ev := internalproto.NewStepEvent("deepresearch", "semantic", strings.TrimSpace(name), state, strings.TrimSpace(message))
	token, err := internalproto.EncodeStepToken(ev)
	if err != nil {
		return
	}
	_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateWorking, &internalproto.Message{
		Role:  internalproto.MessageRoleAgent,
		Parts: []internalproto.Part{internalproto.NewTextPart(token)},
	})
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

func buildDeepResearchWorkflow() (*orchestrator.Workflow, error) {
	wf, err := orchestrator.NewWorkflow(DeepResearchWorkflowID, "deepresearch loop-search workflow")
	if err != nil {
		return nil, err
	}

	if err = wf.AddNode(orchestrator.Node{ID: "N_start", Type: orchestrator.NodeTypeStart}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{
		ID:   "N_loop",
		Type: orchestrator.NodeTypeLoop,
		Config: map[string]any{
			"max_iterations": 2,
		},
		LoopConfig: &orchestrator.LoopConfig{
			MaxIterations: 2,
			ContinueTo:    "N_judge",
			ExitTo:        "N_end",
		},
	}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{
		ID:       "N_judge",
		Type:     orchestrator.NodeTypeChatModel,
		AgentID:  DeepResearchWorkflowWorkerID,
		TaskType: "chat_model",
		Config: map[string]any{
			"normalize_bool": true,
		},
		PreInput: "你是检索评估器。请基于当前已检索信息，判断是否已经足够回答用户问题。仅输出 true 或 false，不要输出任何其它内容。",
	}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{
		ID:   "N_condition",
		Type: orchestrator.NodeTypeCondition,
		Config: map[string]any{
			"left_type":   "path",
			"left_value":  "N_judge.response",
			"operator":    "eq",
			"right_type":  "value",
			"right_value": "true",
		},
		Metadata: map[string]string{
			"true_to":  "N_end",
			"false_to": "N_extract_keywords",
		},
	}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{
		ID:       "N_extract_keywords",
		Type:     orchestrator.NodeTypeChatModel,
		AgentID:  DeepResearchWorkflowWorkerID,
		TaskType: "chat_model",
		PreInput: "当前信息不足，请提取用于下一轮联网检索的相关关键词。仅输出关键词，可以多个，使用分号分隔，不要输出其他内容。",
	}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{
		ID:       "N_tavily",
		Type:     orchestrator.NodeTypeTool,
		AgentID:  DeepResearchWorkflowWorkerID,
		TaskType: DeepResearchDefaultTaskType,
		Config: map[string]any{
			"tool_name": "tavily",
			"params": map[string]any{
				"search_depth": "basic",
				"max_results":  5,
			},
		},
	}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{ID: "N_end", Type: orchestrator.NodeTypeEnd}); err != nil {
		return nil, err
	}

	if err = wf.AddEdgeWithLabel("N_start", "N_loop", "in", nil); err != nil {
		return nil, err
	}
	if err = wf.AddEdgeWithLabel("N_loop", "N_judge", "body", nil); err != nil {
		return nil, err
	}
	if err = wf.AddEdge("N_judge", "N_condition"); err != nil {
		return nil, err
	}
	if err = wf.AddEdgeWithLabel("N_condition", "N_end", "true", nil); err != nil {
		return nil, err
	}
	if err = wf.AddEdgeWithLabel("N_condition", "N_extract_keywords", "false", nil); err != nil {
		return nil, err
	}
	if err = wf.AddEdge("N_extract_keywords", "N_tavily"); err != nil {
		return nil, err
	}
	if err = wf.AddEdgeWithLabel("N_tavily", "N_loop", "loop", nil); err != nil {
		return nil, err
	}
	if err = wf.AddEdgeWithLabel("N_loop", "N_end", "exit", nil); err != nil {
		return nil, err
	}

	return wf, nil
}
