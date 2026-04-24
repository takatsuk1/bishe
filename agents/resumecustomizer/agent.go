package resumecustomizer

import (
	"ai/config"
	"ai/pkg/llm"
	"ai/pkg/logger"
	"ai/pkg/monitor"
	"ai/pkg/orchestrator"
	internalproto "ai/pkg/protocol"
	"ai/pkg/storage"
	internaltm "ai/pkg/taskmanager"
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	ResumeCustomizerWorkflowID       = "resumecustomizer-default"
	ResumeCustomizerWorkflowWorkerID = "resumecustomizer_worker"
	ResumeCustomizerDefaultTaskType  = "resumecustomizer_default"
)

type ctxKeyTaskManager struct{}

type Agent struct {
	orchestratorEngine orchestrator.Engine
	llmClient          *llm.Client
	chatModel          string
}

type workflowNodeWorker struct {
	agent *Agent
}

var resumeCustomizerNodeTypeText = map[string]string{
	"start":   "start",
	"analyze": "chat_model",
	"tailor":  "chat_model",
	"end":     "end",
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
	logger.Infof("[TRACE] resumecustomizer llm_config url=%s model=%s api_key_set=%t", strings.TrimSpace(cfg.LLM.URL), agent.chatModel, strings.TrimSpace(cfg.LLM.APIKey) != "")

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
		logger.Infof("[TRACE] resumecustomizer monitor enabled")
	} else {
		logger.Infof("[TRACE] resumecustomizer monitor disabled: mysql unavailable")
	}

	agent.orchestratorEngine = orchestrator.NewEngine(engineCfg, orchestrator.NewInMemoryAgentRegistry())
	if err := agent.orchestratorEngine.RegisterWorker(orchestrator.AgentDescriptor{
		ID:           ResumeCustomizerWorkflowWorkerID,
		Name:         "resumecustomizer workflow worker",
		Capabilities: []orchestrator.AgentCapability{"chat_model", "resumecustomizer"},
	}, &workflowNodeWorker{agent: agent}); err != nil {
		return nil, err
	}

	wf, err := buildResumeCustomizerWorkflow()
	if err != nil {
		return nil, err
	}
	if err = agent.orchestratorEngine.RegisterWorkflow(wf); err != nil {
		return nil, err
	}

	return agent, nil
}

func (a *Agent) ProcessInternal(ctx context.Context, taskID string, initialMsg internalproto.Message, manager internaltm.Manager) error {
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

	logger.Infof("[TRACE] resumecustomizer.ProcessInternal start task=%s query_len=%d", taskID, len(query))
	runID, err := a.orchestratorEngine.StartWorkflow(ctx, ResumeCustomizerWorkflowID, map[string]any{
		"task_id": taskID,
		"query":   query,
		"text":    query,
		"input":   query,
		"user_id": userID,
	})
	if err != nil {
		return fmt.Errorf("failed to start resumecustomizer workflow: %w", err)
	}
	stopProgress := a.startProgressReporter(ctx, taskID, runID, manager)
	defer stopProgress()

	runResult, err := a.orchestratorEngine.WaitRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("failed to wait resumecustomizer workflow: %w", err)
	}
	if manager != nil {
		for _, nr := range runResult.NodeResults {
			stepState, ok := resumeToTerminalStepState(nr.State)
			if !ok {
				continue
			}
			a.emitResumeStepEvent(ctx, manager, taskID, strings.TrimSpace(nr.NodeID), stepState)
		}
	}
	if runResult.State != orchestrator.RunStateSucceeded {
		if runResult.ErrorMessage != "" {
			return fmt.Errorf("resumecustomizer workflow failed: %s", runResult.ErrorMessage)
		}
		return fmt.Errorf("resumecustomizer workflow failed")
	}

	out, _ := runResult.FinalOutput["response"].(string)
	out = strings.TrimSpace(out)
	if out == "" {
		out = "No usable extracted text found. Please re-upload a clearer PDF/Word/Excel file."
	}
	streamedFinal := resumeStreamedToUser(runResult)
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

func (a *Agent) emitResumeStepEvent(ctx context.Context, manager internaltm.Manager, taskID string, nodeID string, state internalproto.StepState) {
	if manager == nil {
		return
	}
	nodeName := strings.TrimSpace(nodeID)
	if nodeName == "" {
		nodeName = "unknown"
	}
	nodeType := strings.TrimSpace(resumeCustomizerNodeTypeText[nodeName])
	if nodeType == "" {
		nodeType = "unknown"
	}
	messageZh := fmt.Sprintf("节点名:%s 节点类型:%s", nodeName, nodeType)
	ev := internalproto.NewStepEvent("resumecustomizer", "workflow", nodeID, state, messageZh)
	text := ""
	if token, tokenErr := internalproto.EncodeStepToken(ev); tokenErr == nil {
		text = token
	} else {
		text = messageZh
	}
	_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateWorking, &internalproto.Message{
		Role:  internalproto.MessageRoleAgent,
		Parts: []internalproto.Part{internalproto.NewTextPart(text)},
	})
}

func (w *workflowNodeWorker) Execute(ctx context.Context, req orchestrator.ExecutionRequest) (orchestrator.ExecutionResult, error) {
	taskID, _ := req.Payload["task_id"].(string)
	query := extractResumeNodeQuery(req.Payload)
	logger.Infof("[TRACE] resumecustomizer.node_input task=%s node=%s type=%s query_len=%d payload=%s", taskID, strings.TrimSpace(req.NodeID), req.NodeType, len(strings.TrimSpace(query)), snapshotAnyForLog(req.Payload, 2000))

	if req.NodeType != orchestrator.NodeTypeChatModel {
		response := strings.TrimSpace(query)
		if response == "" {
			response = "ok"
		}
		output := map[string]any{"response": response}
		logger.Infof("[TRACE] resumecustomizer.node_output task=%s node=%s type=%s output=%s", taskID, strings.TrimSpace(req.NodeID), req.NodeType, snapshotAnyForLog(output, 2000))
		return orchestrator.ExecutionResult{Output: output}, nil
	}

	output, err := w.agent.callChatModel(ctx, taskID, strings.TrimSpace(req.NodeID), query, req.NodeConfig)
	if err != nil {
		logger.Infof("[TRACE] resumecustomizer.node_error task=%s node=%s type=%s err=%v", taskID, strings.TrimSpace(req.NodeID), req.NodeType, err)
		return orchestrator.ExecutionResult{}, err
	}
	logger.Infof("[TRACE] resumecustomizer.node_output task=%s node=%s type=%s output=%s", taskID, strings.TrimSpace(req.NodeID), req.NodeType, snapshotAnyForLog(output, 2000))
	return orchestrator.ExecutionResult{Output: output}, nil
}

func (a *Agent) callChatModel(ctx context.Context, taskID string, nodeID string, query string, nodeCfg map[string]any) (map[string]any, error) {
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
	case "analyze_resume":
		finalPrompt = buildAnalyzePrompt(query)
	case "tailor_resume":
		finalPrompt = buildTailorPrompt(query)
	}
	streamToUser := strings.EqualFold(strings.TrimSpace(intent), "tailor_resume") || strings.EqualFold(strings.TrimSpace(nodeID), "tailor")

	logger.Infof("[TRACE] resumecustomizer.chatmodel start task=%s intent=%s model=%s", taskID, intent, model)
	if baseURL == "" || model == "" {
		return nil, fmt.Errorf("chat_model config missing url/model")
	}

	a.emitSemanticStep(ctx, taskID, "resumecustomizer.llm.start", internalproto.StepStateInfo, "正在调用大模型："+nodeID)
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
		logger.Warnf("[resumecustomizer] llm failed task=%s intent=%s err=%v, using fallback", taskID, intent, err)
		return map[string]any{"response": fallbackResumeOutput(query)}, nil
	}
	resp = strings.TrimSpace(resp)
	if resp == "" {
		resp = fallbackResumeOutput(query)
	}
	a.emitSemanticStep(ctx, taskID, "resumecustomizer.llm.end", internalproto.StepStateEnd, "完成：大模型处理")

	return map[string]any{
		"response":         resp,
		"streamed_to_user": streamedToUser,
	}, nil
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
	ev := internalproto.NewStepEvent("resumecustomizer", "semantic", strings.TrimSpace(name), state, strings.TrimSpace(message))
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
					a.emitResumeStepEvent(ctx, manager, taskID, nodeID, internalproto.StepStateStart)
				}
				for _, nr := range run.NodeResults {
					id := strings.TrimSpace(nr.NodeID)
					if id == "" || finished[id] {
						continue
					}
					stepState, ok := resumeToTerminalStepState(nr.State)
					if !ok {
						continue
					}
					finished[id] = true
					a.emitResumeStepEvent(ctx, manager, taskID, id, stepState)
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

func resumeToTerminalStepState(state orchestrator.TaskState) (internalproto.StepState, bool) {
	switch state {
	case orchestrator.TaskStateSucceeded:
		return internalproto.StepStateEnd, true
	case orchestrator.TaskStateFailed, orchestrator.TaskStateCanceled:
		return internalproto.StepStateError, true
	default:
		return "", false
	}
}

func resumeStreamedToUser(runResult orchestrator.RunResult) bool {
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

func buildResumeCustomizerWorkflow() (*orchestrator.Workflow, error) {
	wf, err := orchestrator.NewWorkflow(ResumeCustomizerWorkflowID, "resumecustomizer default workflow")
	if err != nil {
		return nil, err
	}

	if err = wf.AddNode(orchestrator.Node{ID: "start", Type: orchestrator.NodeTypeStart}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{
		ID:       "analyze",
		Type:     orchestrator.NodeTypeChatModel,
		AgentID:  ResumeCustomizerWorkflowWorkerID,
		TaskType: ResumeCustomizerDefaultTaskType,
		Config: map[string]any{
			"intent": "analyze_resume",
		},
		PreInput: "分析用户简历和目标岗位信息，提炼关键词与改进点。",
	}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{
		ID:       "tailor",
		Type:     orchestrator.NodeTypeChatModel,
		AgentID:  ResumeCustomizerWorkflowWorkerID,
		TaskType: ResumeCustomizerDefaultTaskType,
		Config: map[string]any{
			"intent": "tailor_resume",
		},
		PreInput: "基于诊断结果生成定制简历。",
	}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{ID: "end", Type: orchestrator.NodeTypeEnd}); err != nil {
		return nil, err
	}

	if err = wf.AddEdge("start", "analyze"); err != nil {
		return nil, err
	}
	if err = wf.AddEdge("analyze", "tailor"); err != nil {
		return nil, err
	}
	if err = wf.AddEdge("tailor", "end"); err != nil {
		return nil, err
	}

	return wf, nil
}
