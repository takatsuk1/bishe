package careerradar

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
	CareerRadarWorkflowID       = "careerradar-default"
	CareerRadarWorkflowWorkerID = "careerradar_worker"
	CareerRadarDefaultTaskType  = "careerradar_default"
)

type ctxKeyTaskManager struct{}

type Agent struct {
	orchestratorEngine orchestrator.Engine
	llmClient          *llm.Client
	chatModel          string
	callAgentTool      tools.Tool
}

type workflowNodeWorker struct {
	agent *Agent
}

var careerRadarNodeProgressText = map[string]string{
	"start":    "Initialize career radar task",
	"plan":     "Generate deep research query",
	"research": "Call deep research agent",
	"analyze":  "Match jobs and detect risk signals",
	"end":      "Return result",
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
	logger.Infof("[TRACE] careerradar llm_config url=%s model=%s api_key_set=%t", strings.TrimSpace(cfg.LLM.URL), agent.chatModel, strings.TrimSpace(cfg.LLM.APIKey) != "")

	toolCfg := *cfg
	filteredAgents := make([]config.AgentConfig, 0, len(cfg.HostAgent.Agents))
	for _, ag := range cfg.HostAgent.Agents {
		if strings.EqualFold(strings.TrimSpace(ag.Name), "careerradar") {
			continue
		}
		filteredAgents = append(filteredAgents, ag)
	}
	toolCfg.HostAgent.Agents = filteredAgents
	callTool, err := tools.NewCallAgentTool(context.Background(), &toolCfg, nil)
	if err != nil {
		return nil, err
	}
	agent.callAgentTool = callTool

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
		logger.Infof("[TRACE] careerradar monitor enabled")
	} else {
		logger.Infof("[TRACE] careerradar monitor disabled: mysql unavailable")
	}

	agent.orchestratorEngine = orchestrator.NewEngine(engineCfg, orchestrator.NewInMemoryAgentRegistry())
	if err := agent.orchestratorEngine.RegisterWorker(orchestrator.AgentDescriptor{
		ID:           CareerRadarWorkflowWorkerID,
		Name:         "careerradar workflow worker",
		Capabilities: []orchestrator.AgentCapability{"chat_model", "tool", "careerradar"},
	}, &workflowNodeWorker{agent: agent}); err != nil {
		return nil, err
	}

	wf, err := buildCareerRadarWorkflow()
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

	logger.Infof("[TRACE] careerradar.ProcessInternal start task=%s query_len=%d", taskID, len(query))
	runID, err := a.orchestratorEngine.StartWorkflow(ctx, CareerRadarWorkflowID, map[string]any{
		"task_id": taskID,
		"query":   query,
		"text":    query,
		"input":   query,
		"user_id": userID,
		"api_key": apiKey,
	})
	if err != nil {
		return fmt.Errorf("failed to start careerradar workflow: %w", err)
	}
	stopProgress := a.startProgressReporter(ctx, taskID, runID, manager)
	defer stopProgress()

	runResult, err := a.orchestratorEngine.WaitRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("failed to wait careerradar workflow: %w", err)
	}
	if manager != nil {
		for _, nr := range runResult.NodeResults {
			stepState, ok := careerToTerminalStepState(nr.State)
			if !ok {
				continue
			}
			a.emitCareerStepEvent(ctx, manager, taskID, strings.TrimSpace(nr.NodeID), stepState)
		}
	}
	if runResult.State != orchestrator.RunStateSucceeded {
		if runResult.ErrorMessage != "" {
			return fmt.Errorf("careerradar workflow failed: %s", runResult.ErrorMessage)
		}
		return fmt.Errorf("careerradar workflow failed")
	}

	out, _ := runResult.FinalOutput["response"].(string)
	out = strings.TrimSpace(out)
	if out == "" {
		out = "已完成岗位雷达扫描，但暂无可展示结果，请补充更具体岗位要求后重试。"
	}
	if manager != nil {
		_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateCompleted, &internalproto.Message{
			Role:  internalproto.MessageRoleAgent,
			Parts: []internalproto.Part{internalproto.NewTextPart(out)},
		})
	}
	return nil
}

func (a *Agent) emitCareerStepEvent(ctx context.Context, manager internaltm.Manager, taskID string, nodeID string, state internalproto.StepState) {
	if manager == nil {
		return
	}
	message := careerRadarNodeProgressText[nodeID]
	if message == "" {
		message = fmt.Sprintf("Execute node %s", nodeID)
	}
	if state == internalproto.StepStateEnd {
		message = "Done: " + message
	}
	if state == internalproto.StepStateError {
		message = "Failed: " + message
	}
	ev := internalproto.NewStepEvent("careerradar", "workflow", nodeID, state, message)
	text := message
	if token, tokenErr := internalproto.EncodeStepToken(ev); tokenErr == nil {
		text = message + "\n" + token
	}
	_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateWorking, &internalproto.Message{
		Role:  internalproto.MessageRoleAgent,
		Parts: []internalproto.Part{internalproto.NewTextPart(text)},
	})
}

func (w *workflowNodeWorker) Execute(ctx context.Context, req orchestrator.ExecutionRequest) (orchestrator.ExecutionResult, error) {
	taskID, _ := req.Payload["task_id"].(string)
	query := extractCareerNodeQuery(req.Payload)
	logger.Infof("[TRACE] careerradar.node_input task=%s node=%s type=%s query_len=%d payload=%s", taskID, strings.TrimSpace(req.NodeID), req.NodeType, len(strings.TrimSpace(query)), snapshotAnyForLog(req.Payload, 2500))

	if req.NodeType == orchestrator.NodeTypeTool {
		output, err := w.agent.callTool(ctx, taskID, strings.TrimSpace(req.NodeID), req.Payload, req.NodeConfig)
		if err != nil {
			return orchestrator.ExecutionResult{}, err
		}
		logger.Infof("[TRACE] careerradar.node_output task=%s node=%s type=%s output=%s", taskID, strings.TrimSpace(req.NodeID), req.NodeType, snapshotAnyForLog(output, 2500))
		return orchestrator.ExecutionResult{Output: output}, nil
	}
	if req.NodeType != orchestrator.NodeTypeChatModel {
		return orchestrator.ExecutionResult{Output: map[string]any{"response": query}}, nil
	}

	output, err := w.agent.callChatModel(ctx, taskID, strings.TrimSpace(req.NodeID), query, req.Payload, req.NodeConfig)
	if err != nil {
		return orchestrator.ExecutionResult{}, err
	}
	logger.Infof("[TRACE] careerradar.node_output task=%s node=%s type=%s output=%s", taskID, strings.TrimSpace(req.NodeID), req.NodeType, snapshotAnyForLog(output, 2500))
	return orchestrator.ExecutionResult{Output: output}, nil
}

func (a *Agent) callTool(ctx context.Context, taskID string, nodeID string, payload map[string]any, nodeCfg map[string]any) (map[string]any, error) {
	toolName := strings.TrimSpace(fmt.Sprint(nodeCfg["tool_name"]))
	if !strings.EqualFold(toolName, "call_agent") {
		return nil, fmt.Errorf("unsupported tool: %s", toolName)
	}
	researchText := strings.TrimSpace(fmt.Sprint(getNodeField(payload, "plan", "response")))
	if researchText == "" {
		researchText = extractCareerNodeQuery(payload)
	}
	params := map[string]any{
		"agent_name":     "deepresearch",
		"text":           researchText,
		"allowed_agents": []any{"deepresearch"},
		"task_id":        taskID,
		"user_id":        strings.TrimSpace(fmt.Sprint(payload["user_id"])),
		"api_key":        strings.TrimSpace(fmt.Sprint(payload["api_key"])),
	}
	manager := taskManagerFromContext(ctx)
	stopHeartbeat := func() {}
	if manager != nil {
		_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateWorking, &internalproto.Message{
			Role:  internalproto.MessageRoleAgent,
			Parts: []internalproto.Part{internalproto.NewTextPart("正在调用 DeepResearch 检索岗位信息，请稍候...")},
		})
		stopCh := make(chan struct{})
		doneCh := make(chan struct{})
		go func() {
			defer close(doneCh)
			ticker := time.NewTicker(3 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-stopCh:
					return
				case <-ticker.C:
					_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateWorking, &internalproto.Message{
						Role:  internalproto.MessageRoleAgent,
						Parts: []internalproto.Part{internalproto.NewTextPart("DeepResearch 仍在检索中，正在整理岗位和风险信号...")},
					})
				}
			}
		}()
		stopHeartbeat = func() {
			close(stopCh)
			<-doneCh
		}
	}
	a.emitSemanticStep(ctx, taskID, "careerradar.research.start", internalproto.StepStateInfo, "正在调用 deepresearch 检索岗位信息")
	out, err := a.callAgentTool.Execute(ctx, params)
	stopHeartbeat()
	if err != nil {
		return nil, err
	}
	resp := strings.TrimSpace(fmt.Sprint(out["response"]))
	if resp == "" {
		resp = strings.TrimSpace(fmt.Sprint(out))
	}
	a.emitSemanticStep(ctx, taskID, "careerradar.research.end", internalproto.StepStateEnd, "检索完成：已返回岗位检索内容")
	return map[string]any{"response": resp, "result": out}, nil
}

func (a *Agent) callChatModel(ctx context.Context, taskID string, nodeID string, query string, payload map[string]any, nodeCfg map[string]any) (map[string]any, error) {
	intent := strings.TrimSpace(fmt.Sprint(nodeCfg["intent"]))
	baseURL := strings.TrimSpace(a.llmClient.BaseURL)
	apiKey := strings.TrimSpace(a.llmClient.APIKey)
	model := strings.TrimSpace(a.chatModel)
	if baseURL == "" || model == "" {
		return nil, fmt.Errorf("chat_model config missing url/model")
	}

	finalPrompt := query
	switch intent {
	case "plan_research":
		finalPrompt = buildResearchPrompt(query)
	case "summarize_jobs":
		research := strings.TrimSpace(fmt.Sprint(getNodeField(payload, "research", "response")))
		finalPrompt = buildSummaryPrompt(query, research)
	}

	logger.Infof("[TRACE] careerradar.chatmodel start task=%s intent=%s model=%s", taskID, intent, model)
	a.emitSemanticStep(ctx, taskID, "careerradar.llm.start", internalproto.StepStateInfo, "正在调用大模型："+nodeID)
	client := llm.NewClient(baseURL, apiKey)
	var streamBuf strings.Builder
	lastEmitAt := time.Time{}
	resp, err := client.ChatCompletionStream(ctx, model, []llm.Message{{Role: "user", Content: finalPrompt}}, nil, nil, func(delta string) error {
		if strings.TrimSpace(delta) == "" {
			return nil
		}
		streamBuf.WriteString(delta)
		if !lastEmitAt.IsZero() && time.Since(lastEmitAt) < 150*time.Millisecond {
			return nil
		}
		lastEmitAt = time.Now()
		a.emitSemanticStep(ctx, taskID, "careerradar.llm.delta", internalproto.StepStateInfo, "正在调用大模型："+truncateText(streamBuf.String(), 140))
		return nil
	})
	if err != nil {
		if intent == "summarize_jobs" {
			return map[string]any{"response": fallbackSummary(fmt.Sprint(getNodeField(payload, "research", "response")))}, nil
		}
		return nil, err
	}
	resp = strings.TrimSpace(resp)
	if resp == "" {
		if intent == "summarize_jobs" {
			resp = fallbackSummary(fmt.Sprint(getNodeField(payload, "research", "response")))
		} else {
			resp = query
		}
	}
	a.emitSemanticStep(ctx, taskID, "careerradar.llm.end", internalproto.StepStateEnd, "完成：大模型处理")
	return map[string]any{"response": resp}, nil
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
	ev := internalproto.NewStepEvent("careerradar", "semantic", strings.TrimSpace(name), state, strings.TrimSpace(message))
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
					a.emitCareerStepEvent(ctx, manager, taskID, nodeID, internalproto.StepStateStart)
				}
				for _, nr := range run.NodeResults {
					id := strings.TrimSpace(nr.NodeID)
					if id == "" || finished[id] {
						continue
					}
					stepState, ok := careerToTerminalStepState(nr.State)
					if !ok {
						continue
					}
					finished[id] = true
					a.emitCareerStepEvent(ctx, manager, taskID, id, stepState)
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

func careerToTerminalStepState(state orchestrator.TaskState) (internalproto.StepState, bool) {
	switch state {
	case orchestrator.TaskStateSucceeded:
		return internalproto.StepStateEnd, true
	case orchestrator.TaskStateFailed, orchestrator.TaskStateCanceled:
		return internalproto.StepStateError, true
	default:
		return "", false
	}
}

func extractCareerNodeQuery(payload map[string]any) string {
	for _, key := range []string{"text", "input", "query"} {
		if v := strings.TrimSpace(fmt.Sprint(payload[key])); v != "" && v != "<nil>" {
			return v
		}
	}
	return ""
}

func getNodeField(payload map[string]any, node, field string) any {
	n, ok := payload[node].(map[string]any)
	if !ok {
		return nil
	}
	return n[field]
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

func buildResearchPrompt(query string) string {
	return "你是职场雷达检索规划助手。用户会给岗位意向，请将其改写成适合 deepresearch agent 的检索任务，要求覆盖：\n" +
		"1) 匹配岗位样本（公司/城市/级别/技能）\n2) 岗位描述中的高风险信号（加班文化、薪资范围模糊、隐形要求）\n" +
		"3) 输出中文，尽量具体可检索。\n\n用户输入：\n" + strings.TrimSpace(query)
}

func buildSummaryPrompt(userQuery, research string) string {
	return "你是职场雷达分析助手。请基于 deepresearch 返回的信息，输出结构化中文结果：\n" +
		"## 匹配岗位推荐（3-5个）\n每个岗位给出：岗位名、公司/行业、匹配理由、建议投递优先级。\n" +
		"## 高风险岗位描述识别\n重点识别并解释：加班文化、薪资描述模糊（如面议/范围极宽/无结构）、职责边界不清、要求不合理。\n" +
		"## 求职建议\n给出可执行建议（筛选关键词、面试提问点、避坑策略）。\n\n" +
		"用户意向：\n" + strings.TrimSpace(userQuery) + "\n\nDeepResearch结果：\n" + strings.TrimSpace(research)
}

func fallbackSummary(research string) string {
	r := strings.TrimSpace(research)
	if r == "" {
		return "暂未拿到有效研究结果。请补充岗位关键词（岗位方向/城市/薪资区间/经验年限）后重试。"
	}
	riskHits := make([]string, 0, 4)
	lower := strings.ToLower(r)
	if strings.Contains(lower, "996") || strings.Contains(lower, "大小周") || strings.Contains(lower, "加班") {
		riskHits = append(riskHits, "- 检测到疑似高强度工时描述（如 996/大小周/频繁加班）")
	}
	if strings.Contains(lower, "面议") || strings.Contains(lower, "薪资可谈") || strings.Contains(lower, "10-50k") {
		riskHits = append(riskHits, "- 检测到薪资描述可能模糊（面议/跨度过大）")
	}
	if len(riskHits) == 0 {
		riskHits = append(riskHits, "- 未检测到显著风险关键词，建议继续人工核验 JD 细节")
	}
	return "## 匹配岗位推荐\n请结合 DeepResearch 内容优先筛选“技能匹配度高 + 薪资范围清晰 + 职责边界明确”的岗位。\n\n" +
		"## 高风险岗位描述识别\n" + strings.Join(riskHits, "\n") + "\n\n" +
		"## 求职建议\n投递前重点确认：作息制度、薪资构成（固定/绩效/年终）、试用期薪资与转正标准。"
}

func buildCareerRadarWorkflow() (*orchestrator.Workflow, error) {
	wf, err := orchestrator.NewWorkflow(CareerRadarWorkflowID, "careerradar workflow")
	if err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{ID: "start", Type: orchestrator.NodeTypeStart}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{
		ID:       "plan",
		Type:     orchestrator.NodeTypeChatModel,
		AgentID:  CareerRadarWorkflowWorkerID,
		TaskType: CareerRadarDefaultTaskType,
		Config:   map[string]any{"intent": "plan_research"},
		PreInput: "生成 deepresearch 检索任务。",
	}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{
		ID:       "research",
		Type:     orchestrator.NodeTypeTool,
		AgentID:  CareerRadarWorkflowWorkerID,
		TaskType: CareerRadarDefaultTaskType,
		Config:   map[string]any{"tool_name": "call_agent"},
		PreInput: "调用 deepresearch 获取岗位情报。",
	}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{
		ID:       "analyze",
		Type:     orchestrator.NodeTypeChatModel,
		AgentID:  CareerRadarWorkflowWorkerID,
		TaskType: CareerRadarDefaultTaskType,
		Config:   map[string]any{"intent": "summarize_jobs"},
		PreInput: "分析匹配岗位并识别高风险岗位描述。",
	}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{ID: "end", Type: orchestrator.NodeTypeEnd}); err != nil {
		return nil, err
	}
	if err = wf.AddEdge("start", "plan"); err != nil {
		return nil, err
	}
	if err = wf.AddEdge("plan", "research"); err != nil {
		return nil, err
	}
	if err = wf.AddEdge("research", "analyze"); err != nil {
		return nil, err
	}
	if err = wf.AddEdge("analyze", "end"); err != nil {
		return nil, err
	}
	return wf, nil
}
