package urlreader

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
	URLReaderWorkflowID       = "urlreader-default"
	URLReaderWorkflowWorkerID = "urlreader_worker"
	URLReaderDefaultTaskType  = "urlreader_default"
)

var urlRegex = regexp.MustCompile(`https?://[^\s"'<>\]\)]+`)

type ctxKeyTaskManager struct{}

type Agent struct {
	orchestratorEngine orchestrator.Engine
	llmClient          *llm.Client
	chatModel          string
	FetchTool          tools.Tool
}

type workflowNodeWorker struct {
	agent *Agent
}

type stepReporter struct {
	agent   string
	taskID  string
	manager internaltm.Manager
}

var urlReaderNodeProgressText = map[string]string{
	"N_start":       "初始化网页读取任务",
	"N_extract_url": "提取目标链接",
	"N_fetch":       "调用 Fetch MCP 抓取网页内容",
	"N_summary":     "整理网页关键信息",
	"N_end":         "输出最终摘要结果",
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
	logger.Infof("[TRACE] urlreader llm_config url=%s model=%s api_key_set=%t", strings.TrimSpace(cfg.LLM.URL), agent.chatModel, strings.TrimSpace(cfg.LLM.APIKey) != "")

	agent.FetchTool = nil

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
		logger.Infof("[TRACE] urlreader monitor enabled")
	} else {
		logger.Infof("[TRACE] urlreader monitor disabled: mysql unavailable")
	}

	agent.orchestratorEngine = orchestrator.NewEngine(engineCfg, orchestrator.NewInMemoryAgentRegistry())
	if err := agent.orchestratorEngine.RegisterWorker(orchestrator.AgentDescriptor{
		ID:           URLReaderWorkflowWorkerID,
		Name:         "urlreader workflow worker",
		Capabilities: []orchestrator.AgentCapability{"chat_model", "tool", "urlreader"},
	}, &workflowNodeWorker{agent: agent}); err != nil {
		return nil, err
	}

	wf, err := buildURLReaderWorkflow()
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

	logger.Infof("[TRACE] urlreader.ProcessInternal start task=%s query_len=%d", taskID, len(query))
	runID, err := a.orchestratorEngine.StartWorkflow(ctx, URLReaderWorkflowID, map[string]any{
		"task_id": taskID,
		"query":   query,
		"text":    query,
		"input":   query,
		"user_id": userID,
	})
	if err != nil {
		return fmt.Errorf("failed to start urlreader workflow: %w", err)
	}
	logger.Infof("[TRACE] urlreader.ProcessInternal started task=%s run_id=%s", taskID, runID)
	stopProgress := a.startProgressReporter(ctx, taskID, runID, manager)
	defer stopProgress()
	runResult, err := a.orchestratorEngine.WaitRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("failed to wait urlreader workflow: %w", err)
	}
	logger.Infof("[TRACE] urlreader.ProcessInternal done task=%s run_state=%s err=%s", taskID, runResult.State, runResult.ErrorMessage)
	for _, nr := range runResult.NodeResults {
		logger.Infof("[TRACE] urlreader.ProcessInternal node_result task=%s node=%s state=%s node_task=%s err=%s", taskID, nr.NodeID, nr.State, nr.TaskID, nr.ErrorMsg)
	}
	if runResult.State != orchestrator.RunStateSucceeded {
		if runResult.ErrorMessage != "" {
			return fmt.Errorf("urlreader workflow failed: %s", runResult.ErrorMessage)
		}
		return fmt.Errorf("urlreader workflow failed")
	}
	out, _ := runResult.FinalOutput["response"].(string)
	out = strings.TrimSpace(out)
	if out == "" {
		out = "Workflow executed successfully"
	}
	streamedFinal := urlreaderStreamedToUser(runResult)
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

	logger.Infof("[TRACE] urlreader.node_input task=%s node=%s type=%s query_len=%d payload=%s", taskID, strings.TrimSpace(req.NodeID), req.NodeType, len(strings.TrimSpace(query)), snapshotAnyForLog(req.Payload, 2000))
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
		logger.Infof("[TRACE] urlreader.node_error task=%s node=%s type=%s err=%v", taskID, strings.TrimSpace(req.NodeID), req.NodeType, err)
		return orchestrator.ExecutionResult{}, err
	}
	logger.Infof("[TRACE] urlreader.node_output task=%s node=%s type=%s output=%s", taskID, strings.TrimSpace(req.NodeID), req.NodeType, snapshotAnyForLog(output, 2000))
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
	case "extract_url":
		if directURL := extractURLCandidateFromPayload(payload); directURL != "" {
			logger.Infof("[TRACE] urlreader.extract_url shortcut task=%s url=%s", taskID, directURL)
			return map[string]any{"response": directURL}, nil
		}
		finalPrompt = buildExtractURLPrompt(extractOriginalQuestion(payload))
	case "summarize_content":
		finalPrompt = buildSummaryPrompt(buildSummaryInput(payload, query))
	}

	logger.Infof("[TRACE] urlreader.chatmodel start task=%s intent=%s model=%s url=%s api_key_set=%t query_len=%d", taskID, intent, model, baseURL, apiKey != "", len(finalPrompt))
	if baseURL == "" || model == "" {
		return nil, fmt.Errorf("chat_model config missing url/model")
	}

	a.emitSemanticStep(ctx, taskID, "urlreader.llm.start", internalproto.StepStateInfo, "正在调用大模型："+nodeID)
	client := llm.NewClient(baseURL, apiKey)
	var streamBuf strings.Builder
	var pending strings.Builder
	lastEmitAt := time.Time{}
	streamToUser := intent == "summarize_content"
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
		a.emitSemanticStep(ctx, taskID, "urlreader.llm.delta", internalproto.StepStateInfo, "正在调用大模型："+truncateText(streamBuf.String(), 140))
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
	if intent == "extract_url" {
		if fromResp := firstURL(resp); fromResp != "" {
			resp = fromResp
		} else if fallback := extractURLCandidateFromPayload(payload); fallback != "" {
			resp = fallback
		} else {
			resp = ""
		}
	}
	a.emitSemanticStep(ctx, taskID, "urlreader.llm.end", internalproto.StepStateEnd, "完成：大模型处理")
	logger.Infof("[TRACE] urlreader.chatmodel done task=%s intent=%s resp_len=%d", taskID, intent, len(resp))

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

	urlText := ""
	if extractOut, ok := payload["N_extract_url"].(map[string]any); ok {
		urlText = firstURL(strings.TrimSpace(fmt.Sprint(extractOut["response"])))
	}
	if urlText == "" {
		urlText = firstURL(query)
	}
	if urlText == "" {
		urlText = extractURLCandidateFromPayload(payload)
	}
	if urlText == "" {
		return nil, fmt.Errorf("no valid url found in extracted result")
	}

	params := map[string]any{"url": urlText, "task_id": taskID}
	tool, err := a.findToolByName(toolName)
	if err != nil {
		return nil, err
	}
	logger.Infof("[TRACE] urlreader.tool start task=%s tool=%s params=%s", taskID, toolName, snapshotAnyForLog(params, 1000))
	out, err := tool.Execute(ctx, params)
	if err != nil {
		return nil, err
	}
	resp := strings.TrimSpace(fmt.Sprintf("%v", out))
	if resp == "" {
		resp = "(empty tool response)"
	}
	logger.Infof("[TRACE] urlreader.tool done task=%s tool=%s resp_len=%d result=%s", taskID, toolName, len(resp), snapshotAnyForLog(out, 2000))
	return map[string]any{"response": resp, "result": out}, nil
}

func (a *Agent) findToolByName(name string) (tools.Tool, error) {
	switch strings.TrimSpace(name) {
	case "fetch":
		if client, ok := tools.GetStdioMCPManager().Get("fetch"); ok {
			return tools.WrapMCPClientAsTool(
				client,
				"fetch",
				"通过本地 Fetch MCP 拉取网页内容",
				[]tools.ToolParameter{{Name: "url", Type: tools.ParamTypeString, Required: true, Description: "需要读取的网页地址（http/https）"}},
			), nil
		}
		if a.FetchTool == nil {
			return nil, fmt.Errorf("fetch mcp server not running; please start it in Tool page")
		}
		return a.FetchTool, nil
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
	ev := internalproto.NewStepEvent("urlreader", "semantic", strings.TrimSpace(name), state, strings.TrimSpace(message))
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

func urlreaderStreamedToUser(runResult orchestrator.RunResult) bool {
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
					messageZh := urlReaderNodeProgressText[nodeID]
					if messageZh == "" {
						messageZh = fmt.Sprintf("执行节点 %s", nodeID)
					}
					ev := internalproto.NewStepEvent("urlreader", "workflow", nodeID, internalproto.StepStateStart, messageZh)
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
					stepState, ok := urlReaderToTerminalStepState(nr.State)
					if !ok {
						continue
					}
					finished[id] = true
					messageZh := urlReaderNodeProgressText[id]
					if messageZh == "" {
						messageZh = fmt.Sprintf("执行节点 %s", id)
					}
					if stepState == internalproto.StepStateEnd {
						messageZh = "完成：" + messageZh
					}
					if stepState == internalproto.StepStateError {
						messageZh = "失败：" + messageZh
					}
					ev := internalproto.NewStepEvent("urlreader", "workflow", id, stepState, messageZh)
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

func urlReaderToTerminalStepState(state orchestrator.TaskState) (internalproto.StepState, bool) {
	switch state {
	case orchestrator.TaskStateSucceeded:
		return internalproto.StepStateEnd, true
	case orchestrator.TaskStateFailed, orchestrator.TaskStateCanceled:
		return internalproto.StepStateError, true
	default:
		return "", false
	}
}

func firstURL(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if m := urlRegex.FindString(text); m != "" {
		m = strings.TrimSpace(m)
		m = strings.TrimRight(m, "])}>,.;:!?。；：！？")
		return m
	}
	return ""
}

func buildExtractURLPrompt(userQuery string) string {
	prompt := strings.Builder{}
	prompt.WriteString("你是 URL 提取助手。\n")
	prompt.WriteString("任务: 从用户问题中提取一个最相关的 URL 链接。\n")
	prompt.WriteString("输出要求: 仅输出一个完整 URL（以 http:// 或 https:// 开头），不要输出任何其他内容。\n")
	prompt.WriteString("如果有多个链接，输出最适合回答用户问题的一个。\n")
	prompt.WriteString("用户问题:\n")
	prompt.WriteString(userQuery)
	return prompt.String()
}

func buildSummaryPrompt(toolOutput string) string {
	prompt := strings.Builder{}
	prompt.WriteString("你是网页内容整理助手。\n")
	prompt.WriteString("请基于以下 fetch 返回内容进行结构化整理，给出关键结论、要点摘要与可执行建议。\n")
	prompt.WriteString("如果内容不足或异常，请明确指出。\n\n")
	prompt.WriteString("fetch 返回内容:\n")
	prompt.WriteString(toolOutput)
	return prompt.String()
}

func extractOriginalQuestion(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	for _, key := range []string{"input", "text", "query"} {
		raw := strings.TrimSpace(fmt.Sprint(payload[key]))
		if raw == "" || raw == "<nil>" {
			continue
		}
		if q := extractCurrentQuestionSection(raw); q != "" {
			return q
		}
		return raw
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
			if q := extractCurrentQuestionSection(raw); q != "" {
				return q
			}
			return raw
		}
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

func extractURLCandidateFromPayload(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	candidates := []string{
		strings.TrimSpace(fmt.Sprint(payload["input"])),
		strings.TrimSpace(fmt.Sprint(payload["text"])),
		strings.TrimSpace(fmt.Sprint(payload["query"])),
	}
	for _, c := range candidates {
		if u := firstURL(c); u != "" {
			return u
		}
	}
	if history, ok := payload["history_outputs"].([]any); ok {
		for _, item := range history {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			out, ok := m["output"].(map[string]any)
			if !ok {
				continue
			}
			if u := firstURL(strings.TrimSpace(fmt.Sprint(out["query"]))); u != "" {
				return u
			}
		}
	}
	return ""
}

func buildSummaryInput(payload map[string]any, fallback string) string {
	if payload != nil {
		if fetchOut, ok := payload["N_fetch"].(map[string]any); ok {
			if s := strings.TrimSpace(fmt.Sprint(fetchOut["response"])); s != "" && s != "<nil>" {
				return s
			}
			if b, err := json.Marshal(fetchOut["result"]); err == nil {
				if s := strings.TrimSpace(string(b)); s != "" {
					return s
				}
			}
		}
	}
	return strings.TrimSpace(fallback)
}

func buildURLReaderWorkflow() (*orchestrator.Workflow, error) {
	wf, err := orchestrator.NewWorkflow(URLReaderWorkflowID, "urlreader default workflow")
	if err != nil {
		return nil, err
	}

	if err = wf.AddNode(orchestrator.Node{ID: "N_start", Type: orchestrator.NodeTypeStart}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{
		ID:       "N_extract_url",
		Type:     orchestrator.NodeTypeChatModel,
		AgentID:  URLReaderWorkflowWorkerID,
		TaskType: "chat_model",
		Config: map[string]any{
			"intent": "extract_url",
		},
		PreInput: "提取用户问题中的 URL 链接。",
	}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{
		ID:       "N_fetch",
		Type:     orchestrator.NodeTypeTool,
		AgentID:  URLReaderWorkflowWorkerID,
		TaskType: URLReaderDefaultTaskType,
		Config: map[string]any{
			"tool_name": "fetch",
		},
	}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{
		ID:       "N_summary",
		Type:     orchestrator.NodeTypeChatModel,
		AgentID:  URLReaderWorkflowWorkerID,
		TaskType: "chat_model",
		Config: map[string]any{
			"intent": "summarize_content",
		},
		PreInput: "分析工具返回结果并进行整理。",
	}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{ID: "N_end", Type: orchestrator.NodeTypeEnd}); err != nil {
		return nil, err
	}

	if err = wf.AddEdge("N_start", "N_extract_url"); err != nil {
		return nil, err
	}
	if err = wf.AddEdge("N_extract_url", "N_fetch"); err != nil {
		return nil, err
	}
	if err = wf.AddEdge("N_fetch", "N_summary"); err != nil {
		return nil, err
	}
	if err = wf.AddEdge("N_summary", "N_end"); err != nil {
		return nil, err
	}

	return wf, nil
}
