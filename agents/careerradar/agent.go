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

var careerRadarNodeTypeText = map[string]string{
	"start":    "start",
	"plan":     "chat_model",
	"research": "tool",
	"analyze":  "chat_model",
	"end":      "end",
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
	if manager != nil {
		a.emitCareerStepEvent(ctx, manager, taskID, "start", internalproto.StepStateStart)
	}
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
		a.emitCareerStepEvent(ctx, manager, taskID, "start", internalproto.StepStateEnd)
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
		out = "已完成职场雷达扫描，但暂时无可展示结果，请补充更具体岗位要求后重试。"
	}
	streamedFinal := careerStreamedToUser(runResult)
	if manager != nil {
		a.emitCareerStepEvent(ctx, manager, taskID, "end", internalproto.StepStateEnd)
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

func (a *Agent) emitCareerStepEvent(ctx context.Context, manager internaltm.Manager, taskID string, nodeID string, state internalproto.StepState) {
	if manager == nil {
		return
	}
	nodeName := strings.TrimSpace(nodeID)
	if nodeName == "" {
		nodeName = "unknown"
	}
	nodeType := strings.TrimSpace(careerRadarNodeTypeText[nodeName])
	if nodeType == "" {
		nodeType = "unknown"
	}
	message := fmt.Sprintf("节点名:%s 节点类型:%s", nodeName, nodeType)
	ev := internalproto.NewStepEvent("careerradar", "workflow", nodeID, state, message)
	text := ""
	if token, tokenErr := internalproto.EncodeStepToken(ev); tokenErr == nil {
		text = token
	} else {
		text = message
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
	stopHeartbeat := func() {}
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
	researchText := ""
	switch intent {
	case "plan_research":
		finalPrompt = buildResearchPrompt(query)
	case "summarize_jobs":
		researchText = strings.TrimSpace(fmt.Sprint(getNodeField(payload, "research", "response")))
		finalPrompt = buildSummaryPrompt(query, researchText)
	}

	logger.Infof("[TRACE] careerradar.chatmodel start task=%s intent=%s model=%s", taskID, intent, model)
	a.emitSemanticStep(ctx, taskID, "careerradar.llm.start", internalproto.StepStateInfo, "姝ｅ湪璋冪敤澶фā鍨嬶細"+nodeID)
	client := llm.NewClient(baseURL, apiKey)
	var streamBuf strings.Builder
	var pending strings.Builder
	lastEmitAt := time.Time{}
	streamToUser := strings.EqualFold(intent, "summarize_jobs") || strings.EqualFold(nodeID, "analyze")
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
		a.emitSemanticStep(ctx, taskID, "careerradar.llm.delta", internalproto.StepStateInfo, "姝ｅ湪璋冪敤澶фā鍨嬶細"+truncateText(streamBuf.String(), 140))
		return nil
	})
	if err == nil {
		flushToUser(true)
	}
	if err != nil {
		if intent == "summarize_jobs" {
			return map[string]any{
				"response":         fallbackSummary(fmt.Sprint(getNodeField(payload, "research", "response"))),
				"streamed_to_user": streamedToUser,
			}, nil
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
	if intent == "summarize_jobs" && strings.TrimSpace(researchText) != "" {
		resp = strings.ReplaceAll(resp, "检索结果为空", "检索结果已返回")
		resp = strings.ReplaceAll(resp, "DeepResearch 检索结果为空", "DeepResearch 已返回检索结果")
	}
	a.emitSemanticStep(ctx, taskID, "careerradar.llm.end", internalproto.StepStateEnd, "瀹屾垚锛氬ぇ妯″瀷澶勭悊")
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
		// Poll a bit faster to avoid missing very short-lived node transitions.
		ticker := time.NewTicker(80 * time.Millisecond)
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

func careerStreamedToUser(runResult orchestrator.RunResult) bool {
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
	return "浣犳槸鑱屽満闆疯揪妫€绱㈣鍒掑姪鎵嬨€傜敤鎴蜂細缁欏矖浣嶆剰鍚戯紝璇峰皢鍏舵敼鍐欐垚閫傚悎 deepresearch agent 鐨勬绱换鍔★紝瑕佹眰瑕嗙洊锛歕n" +
		"1) 鍖归厤宀椾綅鏍锋湰锛堝叕鍙?鍩庡競/绾у埆/鎶€鑳斤級\n2) 宀椾綅鎻忚堪涓殑楂橀闄╀俊鍙凤紙鍔犵彮鏂囧寲銆佽柂璧勮寖鍥存ā绯娿€侀殣褰㈣姹傦級\n" +
		"3) 杈撳嚭涓枃锛屽敖閲忓叿浣撳彲妫€绱€俓n\n鐢ㄦ埛杈撳叆锛歕n" + strings.TrimSpace(query)
}

func buildSummaryPrompt(userQuery, research string) string {
	extraRule := ""
	if strings.TrimSpace(research) != "" {
		extraRule = "\n重要约束：DeepResearch 已返回检索内容，你不得声称“检索结果为空”或“未获取到结果”。"
	}
	return "你是职场雷达分析助手。请基于 deepresearch 返回的信息，输出结构化中文结果：\n" +
		"## 匹配岗位推荐（3-5个）\n每个岗位给出：岗位名、公司/行业、匹配理由、建议投递优先级。\n" +
		"## 高风险岗位描述识别\n重点识别并解释：加班文化、薪资描述模糊（如面议/范围过宽/无结构）、职责边界不清、要求不合理。\n" +
		"## 求职建议\n给出可执行建议（筛选关键词、面试提问点、避坑策略）。\n\n" +
		extraRule + "\n\n" +
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
