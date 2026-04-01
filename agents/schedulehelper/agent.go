package schedulehelper

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
	ScheduleHelperWorkflowID       = "schedulehelper-default"
	ScheduleHelperWorkflowWorkerID = "schedulehelper_worker"
	ScheduleHelperDefaultTaskType  = "schedulehelper_default"
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

var scheduleHelperNodeProgressText = map[string]string{
	"N_start":  "初始化日程规划任务",
	"N_plan":   "分析需求并生成日程方案",
	"N_refine": "补充优先级与提醒建议",
	"N_end":    "输出最终日程建议",
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
	logger.Infof("[TRACE] schedulehelper llm_config url=%s model=%s api_key_set=%t", strings.TrimSpace(cfg.LLM.URL), agent.chatModel, strings.TrimSpace(cfg.LLM.APIKey) != "")

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
		logger.Infof("[TRACE] schedulehelper monitor enabled")
	} else {
		logger.Infof("[TRACE] schedulehelper monitor disabled: mysql unavailable")
	}

	agent.orchestratorEngine = orchestrator.NewEngine(engineCfg, orchestrator.NewInMemoryAgentRegistry())
	if err := agent.orchestratorEngine.RegisterWorker(orchestrator.AgentDescriptor{
		ID:           ScheduleHelperWorkflowWorkerID,
		Name:         "schedulehelper workflow worker",
		Capabilities: []orchestrator.AgentCapability{"chat_model", "schedulehelper"},
	}, &workflowNodeWorker{agent: agent}); err != nil {
		return nil, err
	}

	wf, err := buildScheduleHelperWorkflow()
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

	logger.Infof("[TRACE] schedulehelper.ProcessInternal start task=%s query_len=%d", taskID, len(query))
	runID, err := a.orchestratorEngine.StartWorkflow(ctx, ScheduleHelperWorkflowID, map[string]any{
		"task_id": taskID,
		"query":   query,
		"text":    query,
		"input":   query,
		"user_id": userID,
	})
	if err != nil {
		return fmt.Errorf("failed to start schedulehelper workflow: %w", err)
	}
	stopProgress := a.startProgressReporter(ctx, taskID, runID, manager)
	defer stopProgress()

	runResult, err := a.orchestratorEngine.WaitRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("failed to wait schedulehelper workflow: %w", err)
	}
	if runResult.State != orchestrator.RunStateSucceeded {
		if runResult.ErrorMessage != "" {
			return fmt.Errorf("schedulehelper workflow failed: %s", runResult.ErrorMessage)
		}
		return fmt.Errorf("schedulehelper workflow failed")
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

	if req.NodeType != orchestrator.NodeTypeChatModel {
		response := strings.TrimSpace(query)
		if response == "" {
			response = "ok"
		}
		return orchestrator.ExecutionResult{Output: map[string]any{"response": response}}, nil
	}

	output, err := w.agent.callChatModel(ctx, taskID, query, req.NodeConfig)
	if err != nil {
		return orchestrator.ExecutionResult{}, err
	}
	return orchestrator.ExecutionResult{Output: output}, nil
}

func (a *Agent) callChatModel(ctx context.Context, taskID string, query string, nodeCfg map[string]any) (map[string]any, error) {
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
	case "plan_schedule":
		finalPrompt = buildSchedulePrompt(query)
	case "refine_plan":
		finalPrompt = buildRefinePrompt(query)
	}

	logger.Infof("[TRACE] schedulehelper.chatmodel start task=%s intent=%s model=%s", taskID, intent, model)
	if baseURL == "" || model == "" {
		return nil, fmt.Errorf("chat_model config missing url/model")
	}

	resp, err := llm.NewClient(baseURL, apiKey).ChatCompletion(ctx, model, []llm.Message{{Role: "user", Content: finalPrompt}}, nil, nil)
	if err != nil {
		logger.Warnf("[schedulehelper] llm failed task=%s intent=%s err=%v, using fallback", taskID, intent, err)
		return map[string]any{"response": fallbackSchedulePlan(query)}, nil
	}
	resp = strings.TrimSpace(resp)
	if resp == "" {
		resp = fallbackSchedulePlan(query)
	}

	return map[string]any{"response": resp}, nil
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
					messageZh := scheduleHelperNodeProgressText[nodeID]
					if messageZh == "" {
						messageZh = fmt.Sprintf("执行节点 %s", nodeID)
					}
					ev := internalproto.NewStepEvent("schedulehelper", "workflow", nodeID, internalproto.StepStateStart, messageZh)
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
					stepState, ok := scheduleToTerminalStepState(nr.State)
					if !ok {
						continue
					}
					finished[id] = true
					messageZh := scheduleHelperNodeProgressText[id]
					if messageZh == "" {
						messageZh = fmt.Sprintf("执行节点 %s", id)
					}
					if stepState == internalproto.StepStateEnd {
						messageZh = "完成：" + messageZh
					}
					if stepState == internalproto.StepStateError {
						messageZh = "失败：" + messageZh
					}
					ev := internalproto.NewStepEvent("schedulehelper", "workflow", id, stepState, messageZh)
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

func scheduleToTerminalStepState(state orchestrator.TaskState) (internalproto.StepState, bool) {
	switch state {
	case orchestrator.TaskStateSucceeded:
		return internalproto.StepStateEnd, true
	case orchestrator.TaskStateFailed, orchestrator.TaskStateCanceled:
		return internalproto.StepStateError, true
	default:
		return "", false
	}
}

func buildSchedulePrompt(userQuery string) string {
	var sb strings.Builder
	sb.WriteString("你是个人生活日程规划助手。\n")
	sb.WriteString("请根据用户需求输出今天到本周的可执行计划，包含：\n")
	sb.WriteString("1) 任务优先级\n2) 时间块安排\n3) 必要提醒点\n")
	sb.WriteString("输出中文，结构清晰。\n")
	sb.WriteString("用户需求:\n")
	sb.WriteString(userQuery)
	return sb.String()
}

func buildRefinePrompt(planText string) string {
	var sb strings.Builder
	sb.WriteString("你是计划优化助手。\n")
	sb.WriteString("请在现有计划基础上补充风险提醒、备用方案和可量化目标。\n")
	sb.WriteString("保持简洁，输出为分点列表。\n")
	sb.WriteString("当前计划:\n")
	sb.WriteString(planText)
	return sb.String()
}

func fallbackSchedulePlan(query string) string {
	q := strings.TrimSpace(query)
	if q == "" {
		q = "今日任务"
	}
	return "建议日程：\n1. 上午：处理最重要事项（90分钟专注）\n2. 下午：推进次优先任务并留 30 分钟复盘\n3. 晚上：总结完成情况并准备明日待办\n提醒：每完成一个时间块后记录进度。\n需求摘要：" + q
}

func buildScheduleHelperWorkflow() (*orchestrator.Workflow, error) {
	wf, err := orchestrator.NewWorkflow(ScheduleHelperWorkflowID, "schedulehelper default workflow")
	if err != nil {
		return nil, err
	}

	if err = wf.AddNode(orchestrator.Node{ID: "N_start", Type: orchestrator.NodeTypeStart}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{
		ID:       "N_plan",
		Type:     orchestrator.NodeTypeChatModel,
		AgentID:  ScheduleHelperWorkflowWorkerID,
		TaskType: "chat_model",
		Config: map[string]any{
			"intent": "plan_schedule",
		},
		PreInput: "根据用户输入生成日程规划。",
	}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{
		ID:       "N_refine",
		Type:     orchestrator.NodeTypeChatModel,
		AgentID:  ScheduleHelperWorkflowWorkerID,
		TaskType: "chat_model",
		Config: map[string]any{
			"intent": "refine_plan",
		},
		PreInput: "对计划进行进一步优化。",
	}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{ID: "N_end", Type: orchestrator.NodeTypeEnd}); err != nil {
		return nil, err
	}

	if err = wf.AddEdge("N_start", "N_plan"); err != nil {
		return nil, err
	}
	if err = wf.AddEdge("N_plan", "N_refine"); err != nil {
		return nil, err
	}
	if err = wf.AddEdge("N_refine", "N_end"); err != nil {
		return nil, err
	}

	return wf, nil
}
