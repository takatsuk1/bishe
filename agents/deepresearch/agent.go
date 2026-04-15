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
	"encoding/json"
	"fmt"
	"sort"
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

func (a *Agent) emitSearchKeywordStep(ctx context.Context, taskID string, round int, keyword string) {
	manager := taskManagerFromContext(ctx)
	if manager == nil {
		return
	}
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return
	}
	message := "检索关键词：" + keyword
	if round > 0 {
		message = fmt.Sprintf("第%d轮检索关键词：%s", round, keyword)
	}
	ev := internalproto.NewStepEvent("deepresearch", "search", "deepresearch.search.keyword", internalproto.StepStateInfo, message)
	ev.Round = round
	ev.Keyword = keyword
	token, err := internalproto.EncodeStepToken(ev)
	if err != nil {
		return
	}
	_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateWorking, &internalproto.Message{
		Role:  internalproto.MessageRoleAgent,
		Parts: []internalproto.Part{internalproto.NewTextPart(token)},
	})
}

func (a *Agent) emitLLMStreamingProgress(ctx context.Context, taskID string, chars int) {
	manager := taskManagerFromContext(ctx)
	if manager == nil {
		return
	}
	if chars <= 0 {
		return
	}
	ev := internalproto.NewStepEvent(
		"deepresearch",
		"llm",
		"deepresearch.llm.streaming",
		internalproto.StepStateInfo,
		fmt.Sprintf("大模型生成中，已接收约 %d 字符...", chars),
	)
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

type researchSource struct {
	Title   string
	URL     string
	Content string
	Score   float64
}

type llmSummary struct {
	Summary   string   `json:"summary"`
	KeyPoints []string `json:"key_points"`
	Details   []string `json:"details"`
}

func (a *Agent) buildStructuredResponse(ctx context.Context, taskID string, query string, finalOutput map[string]any) string {
	sources := collectResearchSources(finalOutput)
	raw := strings.TrimSpace(fmt.Sprint(finalOutput["response"]))
	raw = sanitizeRawResponse(raw)

	summary, points, details := a.summarizeSourcesWithLLM(ctx, taskID, query, sources)
	if summary == "" {
		summary = fallbackSummary(query, raw, sources)
	}
	if len(points) == 0 {
		points = fallbackKeyPoints(sources)
	}
	if len(details) == 0 {
		details = fallbackDetails(sources)
	}

	var b strings.Builder
	b.WriteString("### 研究结论\n")
	b.WriteString(summary)
	b.WriteString("\n\n")
	b.WriteString("### 关键要点\n")
	if len(points) == 0 {
		b.WriteString("1. 当前结果较少，建议换更具体的关键词后重试。\n")
	} else {
		for i, p := range points {
			b.WriteString(fmt.Sprintf("%d. %s\n", i+1, p))
		}
	}
	b.WriteString("\n")
	b.WriteString("### 详细信息\n")
	if len(details) == 0 {
		b.WriteString("- 暂无更多可提炼的细节。\n")
	} else {
		for i, d := range details {
			b.WriteString(fmt.Sprintf("- %s\n", d))
			if i >= 7 {
				break
			}
		}
	}
	b.WriteString("\n")
	b.WriteString("### 参考来源\n")
	if len(sources) == 0 {
		b.WriteString("- 暂无可用来源。\n")
	} else {
		for i, s := range sources {
			title := strings.TrimSpace(s.Title)
			if title == "" {
				title = "未命名来源"
			}
			if strings.TrimSpace(s.URL) != "" {
				b.WriteString(fmt.Sprintf("- [%d] %s\n  - %s\n", i+1, title, strings.TrimSpace(s.URL)))
			} else {
				b.WriteString(fmt.Sprintf("- [%d] %s\n", i+1, title))
			}
		}
	}
	b.WriteString("\n")
	b.WriteString("### 检索信息整理\n")
	if len(sources) == 0 {
		b.WriteString("- 暂无可整理的检索内容。\n")
	} else {
		for i, s := range sources {
			title := strings.TrimSpace(s.Title)
			if title == "" {
				title = "未命名来源"
			}
			b.WriteString(fmt.Sprintf("#### [%d] %s\n", i+1, title))
			if strings.TrimSpace(s.URL) != "" {
				b.WriteString(fmt.Sprintf("- 链接：%s\n", strings.TrimSpace(s.URL)))
			}
			content := strings.TrimSpace(s.Content)
			if content == "" {
				b.WriteString("- 摘录：无正文摘录\n\n")
				continue
			}
			b.WriteString(fmt.Sprintf("- 摘录：%s\n\n", truncateText(content, 360)))
		}
	}

	return strings.TrimSpace(b.String())
}

func (a *Agent) summarizeSourcesWithLLM(ctx context.Context, taskID string, query string, sources []researchSource) (string, []string, []string) {
	if len(sources) == 0 {
		return "", nil, nil
	}
	if a == nil || a.llmClient == nil {
		return "", nil, nil
	}
	baseURL := strings.TrimSpace(a.llmClient.BaseURL)
	apiKey := strings.TrimSpace(a.llmClient.APIKey)
	model := strings.TrimSpace(a.chatModel)
	if baseURL == "" || model == "" {
		return "", nil, nil
	}

	maxSources := len(sources)
	if maxSources > 12 {
		maxSources = 12
	}
	var refs strings.Builder
	for i := 0; i < maxSources; i++ {
		s := sources[i]
		refs.WriteString(fmt.Sprintf("[%d] 标题: %s\n", i+1, strings.TrimSpace(s.Title)))
		if strings.TrimSpace(s.URL) != "" {
			refs.WriteString(fmt.Sprintf("URL: %s\n", strings.TrimSpace(s.URL)))
		}
		refs.WriteString(fmt.Sprintf("摘要: %s\n\n", truncateText(strings.TrimSpace(s.Content), 420)))
	}

	system := "你是严谨的研究总结助手。请严格输出 JSON，不要输出 markdown。"
	user := "请根据以下联网资料，输出结构化总结。\n" +
		"输出格式必须是 JSON：{\"summary\":\"...\",\"key_points\":[\"...\"],\"details\":[\"...\"]}\n" +
		"要求：summary 6-10 句；key_points 5-8 条；details 8-12 条。\n" +
		"要求2：优先提炼学校概况、优势专业、录取信息、选科要求、招生渠道等具体信息。\n" +
		fmt.Sprintf("用户问题：%s\n\n资料：\n%s", strings.TrimSpace(query), refs.String())

	logger.Infof("[TRACE] deepresearch.summary_llm start task=%s model=%s source_count=%d", taskID, model, len(sources))
	resp, err := llm.NewClient(baseURL, apiKey).ChatCompletion(ctx, model, []llm.Message{{Role: "system", Content: system}, {Role: "user", Content: user}}, nil, nil)
	if err != nil {
		logger.Warnf("[TRACE] deepresearch.summary_llm failed task=%s err=%v", taskID, err)
		return "", nil, nil
	}

	resp = strings.TrimSpace(stripMarkdownCodeFence(resp))
	parsed := llmSummary{}
	if err := json.Unmarshal([]byte(resp), &parsed); err != nil {
		logger.Warnf("[TRACE] deepresearch.summary_llm invalid_json task=%s resp=%q", taskID, truncateText(resp, 120))
		return "", nil, nil
	}
	parsed.Summary = strings.TrimSpace(parsed.Summary)
	outPoints := make([]string, 0, len(parsed.KeyPoints))
	for _, p := range parsed.KeyPoints {
		p = strings.TrimSpace(p)
		if p != "" {
			outPoints = append(outPoints, p)
		}
	}
	outDetails := make([]string, 0, len(parsed.Details))
	for _, d := range parsed.Details {
		d = strings.TrimSpace(d)
		if d != "" {
			outDetails = append(outDetails, d)
		}
	}
	return parsed.Summary, outPoints, outDetails
}

func (a *Agent) streamStructuredResponseWithLLM(ctx context.Context, taskID string, query string, finalOutput map[string]any, manager internaltm.Manager) (string, error) {
	if manager == nil {
		return "", fmt.Errorf("task manager unavailable")
	}
	sources := collectResearchSources(finalOutput)
	if len(sources) == 0 {
		return "", fmt.Errorf("no sources for streaming summary")
	}
	if a == nil || a.llmClient == nil {
		return "", fmt.Errorf("llm client unavailable")
	}
	baseURL := strings.TrimSpace(a.llmClient.BaseURL)
	apiKey := strings.TrimSpace(a.llmClient.APIKey)
	model := strings.TrimSpace(a.chatModel)
	if baseURL == "" || model == "" {
		return "", fmt.Errorf("chat_model config missing url/model")
	}

	maxSources := len(sources)
	if maxSources > 12 {
		maxSources = 12
	}
	var refs strings.Builder
	for i := 0; i < maxSources; i++ {
		s := sources[i]
		refs.WriteString(fmt.Sprintf("[%d] 标题: %s\n", i+1, strings.TrimSpace(s.Title)))
		if strings.TrimSpace(s.URL) != "" {
			refs.WriteString(fmt.Sprintf("URL: %s\n", strings.TrimSpace(s.URL)))
		}
		refs.WriteString(fmt.Sprintf("摘录: %s\n\n", truncateText(strings.TrimSpace(s.Content), 420)))
	}

	system := "你是严谨的研究助理。请直接输出 Markdown，不要输出 JSON。"
	user := "请根据提供资料，输出结构化中文结论，必须使用以下标题：\n" +
		"### 研究结论\n### 关键要点\n### 详细信息\n### 参考来源\n### 检索信息整理\n" +
		"要求：\n" +
		"1) 内容与资料一致，不编造。\n" +
		"2) 关键要点使用编号列表。\n" +
		"3) 参考来源按 [1][2]... 给出标题和链接。\n\n" +
		fmt.Sprintf("用户问题：\n%s\n\n资料：\n%s", strings.TrimSpace(query), refs.String())

	logger.Infof("[TRACE] deepresearch.summary_llm.stream start task=%s model=%s source_count=%d", taskID, model, len(sources))
	a.emitSemanticStep(ctx, taskID, "deepresearch.final.organize.start", internalproto.StepStateInfo, "正在调用大模型整理结果：")
	var out strings.Builder
	client := llm.NewClient(baseURL, apiKey)
	_, err := client.ChatCompletionStream(ctx, model, []llm.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}, nil, nil, func(delta string) error {
		if delta == "" {
			return nil
		}
		out.WriteString(delta)
		return manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateWorking, &internalproto.Message{
			Role:  internalproto.MessageRoleAgent,
			Parts: []internalproto.Part{internalproto.NewTextPart(delta)},
		})
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}

func collectResearchSources(finalOutput map[string]any) []researchSource {
	out := make([]researchSource, 0, 8)
	seen := map[string]bool{}
	var walk func(v any)
	walk = func(v any) {
		switch vv := v.(type) {
		case map[string]any:
			if rawResults, ok := vv["results"].([]any); ok {
				for _, item := range rawResults {
					m, ok := item.(map[string]any)
					if !ok {
						continue
					}
					s := researchSource{
						Title:   strings.TrimSpace(fmt.Sprint(m["title"])),
						URL:     strings.TrimSpace(fmt.Sprint(m["url"])),
						Content: strings.TrimSpace(fmt.Sprint(m["content"])),
					}
					score, ok := m["score"].(float64)
					if ok {
						s.Score = score
					}
					if s.Title == "" && s.URL == "" && s.Content == "" {
						continue
					}
					key := strings.TrimSpace(s.URL) + "|" + strings.TrimSpace(s.Title)
					if key == "|" {
						key = truncateText(strings.TrimSpace(s.Content), 80)
					}
					if !seen[key] {
						seen[key] = true
						out = append(out, s)
					}
				}
			}
			for _, nested := range vv {
				walk(nested)
			}
		case []any:
			for _, item := range vv {
				walk(item)
			}
		}
	}
	walk(finalOutput)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].Title < out[j].Title
		}
		return out[i].Score > out[j].Score
	})
	if len(out) > 20 {
		out = out[:20]
	}
	return out
}

func summarizeToolResponse(out map[string]any) string {
	count := len(collectResearchSources(map[string]any{"result": out}))
	if count > 0 {
		return fmt.Sprintf("检索完成，共获得 %d 条候选资料。", count)
	}
	if jsonMap, ok := out["json"].(map[string]any); ok {
		if answer := strings.TrimSpace(fmt.Sprint(jsonMap["answer"])); answer != "" && answer != "<nil>" {
			return truncateText(answer, 240)
		}
	}
	return "工具调用已完成。"
}

func summarizeSearchPreview(out map[string]any) string {
	sources := collectResearchSources(map[string]any{"result": out})
	if len(sources) == 0 {
		return "未获取到有效结果"
	}
	head := sources[0]
	if t := strings.TrimSpace(head.Title); t != "" {
		if c := strings.TrimSpace(head.Content); c != "" {
			return truncateText(t+"："+c, 140)
		}
		return truncateText(t, 140)
	}
	if c := strings.TrimSpace(head.Content); c != "" {
		return truncateText(c, 140)
	}
	if u := strings.TrimSpace(head.URL); u != "" {
		return truncateText(u, 140)
	}
	return "已返回候选资料"
}

func sanitizeRawResponse(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "<nil>" {
		return ""
	}
	lower := strings.ToLower(raw)
	if lower == "true" || lower == "false" {
		return ""
	}
	if strings.Contains(lower, "map[") || strings.Contains(lower, "status_code") || strings.Contains(lower, "headers:") {
		return ""
	}
	return raw
}

func fallbackSummary(query string, raw string, sources []researchSource) string {
	if raw != "" {
		return raw
	}
	if len(sources) == 0 {
		if strings.TrimSpace(query) == "" {
			return "已完成研究流程，但当前可用资料不足，建议补充更具体的问题后重试。"
		}
		return fmt.Sprintf("已完成对“%s”的研究流程，但当前可用资料较少，建议细化问题后再次检索。", strings.TrimSpace(query))
	}
	head := sources[0]
	if strings.TrimSpace(head.Title) != "" {
		return fmt.Sprintf("已完成检索与整理，核心信息集中在“%s”等来源。", strings.TrimSpace(head.Title))
	}
	return "已完成检索与整理，下面给出关键要点与可追溯来源。"
}

func fallbackKeyPoints(sources []researchSource) []string {
	out := make([]string, 0, 6)
	for _, s := range sources {
		if len(out) >= 6 {
			break
		}
		if t := strings.TrimSpace(s.Title); t != "" {
			out = append(out, t)
			continue
		}
		if c := strings.TrimSpace(s.Content); c != "" {
			out = append(out, truncateText(c, 56))
		}
	}
	return out
}

func fallbackDetails(sources []researchSource) []string {
	out := make([]string, 0, 8)
	for _, s := range sources {
		if len(out) >= 8 {
			break
		}
		title := strings.TrimSpace(s.Title)
		content := truncateText(strings.TrimSpace(s.Content), 120)
		if title == "" && content == "" {
			continue
		}
		if title != "" && content != "" {
			out = append(out, fmt.Sprintf("%s：%s", title, content))
			continue
		}
		if title != "" {
			out = append(out, title)
			continue
		}
		out = append(out, content)
	}
	return out
}

func stripMarkdownCodeFence(input string) string {
	out := strings.TrimSpace(input)
	if strings.HasPrefix(out, "```") {
		out = strings.TrimPrefix(out, "```")
		out = strings.TrimSpace(strings.TrimPrefix(out, "json"))
		if idx := strings.LastIndex(out, "```"); idx >= 0 {
			out = strings.TrimSpace(out[:idx])
		}
	}
	return out
}

func truncateText(input string, max int) string {
	input = strings.TrimSpace(input)
	if max <= 0 || len(input) <= max {
		return input
	}
	return strings.TrimSpace(input[:max]) + "..."
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

func normalizeBoolResponse(in string) string {
	s := strings.ToLower(strings.TrimSpace(in))
	if s == "" {
		return "false"
	}
	if strings.Contains(s, "true") || strings.Contains(s, "是") || strings.Contains(s, "满足") || strings.Contains(s, "足够") {
		if strings.Contains(s, "false") || strings.Contains(s, "否") || strings.Contains(s, "不足") {
			if strings.HasPrefix(s, "false") || strings.HasPrefix(s, "否") || strings.HasPrefix(s, "不足") {
				return "false"
			}
		}
		return "true"
	}
	if strings.Contains(s, "false") || strings.Contains(s, "否") || strings.Contains(s, "不足") {
		return "false"
	}
	if strings.HasPrefix(s, "t") || strings.HasPrefix(s, "1") || strings.HasPrefix(s, "y") {
		return "true"
	}
	return "false"
}

func trimForTavilyQuery(in string) string {
	q := strings.TrimSpace(in)
	if strings.HasPrefix(q, "map[") && strings.HasSuffix(q, "]") {
		if i := strings.Index(q, "response:"); i >= 0 {
			q = strings.TrimSpace(q[i+len("response:"):])
			q = strings.TrimSuffix(q, "]")
		}
	}
	if i := strings.LastIndex(q, "=== 当前问题 ==="); i >= 0 {
		q = strings.TrimSpace(q[i+len("=== 当前问题 ==="):])
	}
	if i := strings.LastIndex(q, "用户:"); i >= 0 {
		q = strings.TrimSpace(q[i+len("用户:"):])
	}
	if len(q) > 400 {
		q = q[:400]
	}
	q = simplifyKeywordQuery(q)
	return q
}

func simplifyKeywordQuery(in string) string {
	in = strings.TrimSpace(in)
	if in == "" {
		return ""
	}
	parts := strings.FieldsFunc(in, func(r rune) bool {
		switch r {
		case ';', '；', ',', '，', '\n', '\r', '\t', '|', '/', '、':
			return true
		default:
			return false
		}
	})
	unique := make([]string, 0, 4)
	seen := map[string]bool{}
	for _, p := range parts {
		p = strings.TrimSpace(strings.Trim(p, "\"'`“”"))
		if p == "" {
			continue
		}
		key := strings.ToLower(p)
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, p)
		if len(unique) >= 3 {
			break
		}
	}
	if len(unique) == 0 {
		if len(in) > 80 {
			return strings.TrimSpace(in[:80])
		}
		return in
	}
	return strings.Join(unique, " ")
}

func buildJudgeQuery(payload map[string]any) string {
	base := "你是检索评估器。请基于当前已检索信息，判断是否已经足够回答用户问题。仅输出 true 或 false，不要输出任何其它内容。"
	orig := extractOriginalQuestion(payload)
	evidence := extractLatestSearchSnippet(payload)
	if evidence == "" {
		return base + "\n\n用户问题：\n" + orig
	}
	return base + "\n\n用户问题：\n" + orig + "\n\n已检索信息（摘要）：\n" + evidence
}

func buildKeywordExtractionQuery(payload map[string]any) string {
	orig := extractOriginalQuestion(payload)
	evidence := extractLatestSearchSnippet(payload)
	if evidence == "" {
		return "当前信息不足。请围绕用户原始问题提取下一轮联网检索关键词。\n" +
			"要求：\n" +
			"1) 必须包含用户问题中的核心实体或专有名词。\n" +
			"2) 禁止输出泛词（如：相关信息、背景信息、待确认主题、具体查询目标）。\n" +
			"3) 只输出关键词，使用分号分隔，不要输出其他内容。\n" +
			"4) 最多输出不超过 3 个关键词。\n\n" +
			"用户问题：\n" + orig
	}
	return "请基于用户问题与已检索信息，提取下一轮更具体的联网检索关键词。\n" +
		"要求：\n" +
		"1) 必须包含用户问题中的核心实体或专有名词。\n" +
		"2) 必须体现信息缺口（如创始人、业务、官网、最新动态、产品）。\n" +
		"3) 禁止输出泛词（如：相关信息、背景信息、待确认主题、具体查询目标）。\n" +
		"4) 只输出关键词，使用分号分隔，不要输出其他内容。\n" +
		"5) 最多输出不超过 3 个关键词。\n\n" +
		"用户问题：\n" + orig + "\n\n已检索信息（摘要）：\n" + evidence
}

func extractOriginalQuestion(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	if raw := strings.TrimSpace(fmt.Sprint(payload["input"])); raw != "" && raw != "<nil>" {
		if q := extractCurrentQuestionSection(raw); q != "" {
			return q
		}
	}
	if raw := strings.TrimSpace(fmt.Sprint(payload["text"])); raw != "" && raw != "<nil>" {
		if q := extractCurrentQuestionSection(raw); q != "" {
			return q
		}
	}
	if raw := strings.TrimSpace(fmt.Sprint(payload["query"])); raw != "" && raw != "<nil>" {
		if q := extractCurrentQuestionSection(raw); q != "" {
			return q
		}
		return raw
	}
	return ""
}

func extractCurrentQuestionSection(in string) string {
	s := strings.TrimSpace(in)
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
	return s
}

func extractLatestSearchSnippet(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	tavilyOut, ok := payload["N_tavily"].(map[string]any)
	if !ok {
		return ""
	}
	result, ok := tavilyOut["result"].(map[string]any)
	if !ok {
		return ""
	}
	jsonMap, ok := result["json"].(map[string]any)
	if !ok {
		return ""
	}
	items, ok := jsonMap["results"].([]any)
	if !ok || len(items) == 0 {
		return ""
	}
	var b strings.Builder
	max := len(items)
	if max > 3 {
		max = 3
	}
	for i := 0; i < max; i++ {
		m, ok := items[i].(map[string]any)
		if !ok {
			continue
		}
		title := strings.TrimSpace(fmt.Sprint(m["title"]))
		content := truncateText(strings.TrimSpace(fmt.Sprint(m["content"])), 180)
		if title == "" && content == "" {
			continue
		}
		if title != "" {
			b.WriteString("- ")
			b.WriteString(title)
			if content != "" {
				b.WriteString(": ")
				b.WriteString(content)
			}
			b.WriteString("\n")
			continue
		}
		b.WriteString("- ")
		b.WriteString(content)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func anchorSearchQuery(query string, original string) string {
	q := strings.TrimSpace(query)
	orig := strings.TrimSpace(original)
	if q == "" {
		return orig
	}
	if isGenericKeywordQuery(q) {
		if orig != "" {
			return orig
		}
		return q
	}
	if orig == "" {
		return q
	}
	if strings.Contains(q, orig) || strings.Contains(orig, q) {
		return q
	}
	if len([]rune(q)) <= 40 {
		return strings.TrimSpace(orig + " " + q)
	}
	return q
}

func isGenericKeywordQuery(in string) bool {
	s := strings.TrimSpace(strings.ToLower(in))
	if s == "" {
		return true
	}
	genericPhrases := []string{
		"待确认主题", "具体查询目标", "补充相关背景", "相关信息", "背景信息", "更多信息",
		"general", "background", "topic", "information",
	}
	for _, g := range genericPhrases {
		if strings.Contains(s, strings.ToLower(g)) {
			return true
		}
	}
	meaningful := strings.TrimSpace(strings.NewReplacer(";", "", "；", "", ",", "", "，", "", " ", "").Replace(s))
	if len([]rune(meaningful)) <= 3 {
		return true
	}
	return false
}

func extractLoopRound(payload map[string]any) int {
	if payload == nil {
		return 0
	}
	v, ok := payload["__loop_iter_N_loop"]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case int32:
		return int(n)
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

func hasSearchEvidence(payload map[string]any) bool {
	if payload == nil {
		return false
	}
	var walk func(v any) bool
	walk = func(v any) bool {
		switch vv := v.(type) {
		case map[string]any:
			if _, ok := vv["N_tavily"]; ok {
				return true
			}
			if rs, ok := vv["results"]; ok {
				switch arr := rs.(type) {
				case []any:
					if len(arr) > 0 {
						return true
					}
				case []map[string]any:
					if len(arr) > 0 {
						return true
					}
				}
			}
			for _, nested := range vv {
				if walk(nested) {
					return true
				}
			}
		case []any:
			for _, item := range vv {
				if walk(item) {
					return true
				}
			}
		}
		return false
	}
	return walk(payload)
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
