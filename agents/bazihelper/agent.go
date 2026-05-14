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
	"fmt"
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

	// 当判断为整理八字结果的节点时，开启流式输出功能，
	// 实时把模型输出的内容通过 manager 写回给前端，
	// 让用户可以边看结果边等待最终完成，而不是等全部结果生成后一次性看到。
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
