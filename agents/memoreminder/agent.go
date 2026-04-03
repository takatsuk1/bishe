package memoreminder

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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	MemoReminderWorkflowID       = "memoreminder-default"
	MemoReminderWorkflowWorkerID = "memoreminder_worker"
	MemoReminderDefaultTaskType  = "memoreminder_default"
)

type ctxKeyTaskManager struct{}

type Reminder struct {
	ID       string    `json:"id"`
	Content  string    `json:"content"`
	RemindAt time.Time `json:"remind_at"`
	Script   string    `json:"script"`
	Reminded bool      `json:"reminded"`
}

type Agent struct {
	orchestratorEngine orchestrator.Engine
	llmClient          *llm.Client
	chatModel          string
	JsonFileTool       tools.Tool
	watcherStarted     bool
	watcherMutex       sync.Mutex
}

type workflowNodeWorker struct {
	agent *Agent
}

var memoReminderNodeProgressText = map[string]string{
	"N_start":      "初始化备忘录提醒任务",
	"N_parse":      "解析并结构化提醒信息",
	"N_write_json": "写入提醒到JSON文件",
	"N_ack":        "生成用户确认回复",
	"N_end":        "完成提醒录入",
}

const remindersFile = "reminders.json"

func NewAgent() (*Agent, error) {
	cfg := config.GetMainConfig()
	agent := &Agent{}

	agent.llmClient = llm.NewClient(cfg.LLM.URL, cfg.LLM.APIKey)
	agent.chatModel = strings.TrimSpace(cfg.LLM.ChatModel)
	if agent.chatModel == "" {
		agent.chatModel = strings.TrimSpace(cfg.LLM.ReasoningModel)
	}
	if agent.chatModel == "" {
		agent.chatModel = "qwen-3.5-flash"
	}
	logger.Infof("[TRACE] memoreminder llm_config url=%s model=%s api_key_set=%t", strings.TrimSpace(cfg.LLM.URL), agent.chatModel, strings.TrimSpace(cfg.LLM.APIKey) != "")

	agent.JsonFileTool = tools.NewMCPTool(
		"json_file",
		"本地 JSON 文件读写 MCP 服务",
		[]tools.ToolParameter{
			{Name: "action", Type: tools.ParamTypeString, Required: true, Description: "read 或 write"},
			{Name: "path", Type: tools.ParamTypeString, Required: true, Description: "JSON 文件路径"},
			{Name: "json", Type: tools.ParamTypeObject, Required: false, Description: "写入 JSON 内容"},
		},
		tools.MCPToolConfig{
			Mode:     "stdio",
			Command:  "go",
			Args:     []string{"run", "./tools/jsonfilemcp", "--root", "."},
			ToolName: "json_file",
		},
	)

	engineCfg := orchestrator.Config{
		DefaultTaskTimeoutSec: cfg.Orchestrator.DefaultTaskTimeoutSec,
		RetryMaxAttempts:      cfg.Orchestrator.Retry.MaxAttempts,
		RetryBaseBackoffMs:    cfg.Orchestrator.Retry.BaseBackoffMs,
		RetryMaxBackoffMs:     cfg.Orchestrator.Retry.MaxBackoffMs,
	}
	if engineCfg.DefaultTaskTimeoutSec <= 0 {
		engineCfg.DefaultTaskTimeoutSec = 60
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
		logger.Infof("[TRACE] memoreminder monitor enabled")
	} else {
		logger.Infof("[TRACE] memoreminder monitor disabled: mysql unavailable")
	}

	agent.orchestratorEngine = orchestrator.NewEngine(engineCfg, orchestrator.NewInMemoryAgentRegistry())
	if err := agent.orchestratorEngine.RegisterWorker(orchestrator.AgentDescriptor{
		ID:           MemoReminderWorkflowWorkerID,
		Name:         "memoreminder workflow worker",
		Capabilities: []orchestrator.AgentCapability{"chat_model", "action"},
	}, &workflowNodeWorker{agent: agent}); err != nil {
		return nil, err
	}

	wf, err := buildMemoReminderWorkflow()
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

	// 启动后台 Watcher（只启动一次）
	a.startWatcherOnce()

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

	logger.Infof("[TRACE] memoreminder.ProcessInternal start task=%s query_len=%d", taskID, len(query))
	runID, err := a.orchestratorEngine.StartWorkflow(ctx, MemoReminderWorkflowID, map[string]any{
		"task_id": taskID,
		"text":    query,
		"input":   query,
		"user_id": userID,
	})
	if err != nil {
		return fmt.Errorf("failed to start memoreminder workflow: %w", err)
	}
	logger.Infof("[TRACE] memoreminder.ProcessInternal started task=%s run_id=%s", taskID, runID)

	stopProgress := a.startProgressReporter(ctx, taskID, runID, manager)
	defer stopProgress()

	runResult, err := a.orchestratorEngine.WaitRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("failed to wait memoreminder workflow: %w", err)
	}
	logger.Infof("[TRACE] memoreminder.ProcessInternal done task=%s run_state=%s", taskID, runResult.State)
	for _, nr := range runResult.NodeResults {
		logger.Infof("[TRACE] memoreminder.ProcessInternal node_result task=%s node=%s state=%s node_task=%s err=%s", taskID, nr.NodeID, nr.State, nr.TaskID, nr.ErrorMsg)
	}

	if runResult.State != orchestrator.RunStateSucceeded {
		if runResult.ErrorMessage != "" {
			return fmt.Errorf("memoreminder workflow failed: %s", runResult.ErrorMessage)
		}
		return fmt.Errorf("memoreminder workflow failed")
	}

	out, _ := runResult.FinalOutput["response"].(string)
	if out == "" {
		out = "备忘录已记录，我会在提醒时间前10分钟通过弹窗提醒你"
	}

	_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateCompleted, &internalproto.Message{
		Role:  internalproto.MessageRoleAgent,
		Parts: []internalproto.Part{internalproto.NewTextPart(out)},
	})

	return nil
}

func (a *Agent) startWatcherOnce() {
	a.watcherMutex.Lock()
	defer a.watcherMutex.Unlock()

	if a.watcherStarted {
		return
	}
	a.watcherStarted = true

	go a.reminderWatcher()
}

func (a *Agent) reminderWatcher() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		reminders, err := loadReminders()
		if err != nil {
			logger.Warnf("[MemoReminder] failed to load reminders: %v", err)
			continue
		}

		now := time.Now()
		updated := false
		for i := range reminders {
			r := &reminders[i]
			if r.Reminded {
				continue
			}

			timeDiff := r.RemindAt.Sub(now)
			if timeDiff <= 10*time.Minute && timeDiff > 0 {
				// 触发弹窗脚本，后续可通过 script_exec 工具调用
				logger.Infof("[MemoReminder] Triggering reminder: %s (time_diff: %v)", r.ID, timeDiff)
				r.Reminded = true
				updated = true
				// 这里可以调用 script_exec 工具或直接弹窗
				a.triggerNotification(r)
			}
		}

		if updated {
			if err := saveReminders(reminders); err != nil {
				logger.Warnf("[MemoReminder] failed to save reminders: %v", err)
			}
		}
	}
}

func (a *Agent) triggerNotification(r *Reminder) {
	// 生成 PowerShell 弹窗脚本
	script := fmt.Sprintf(`
Add-Type -AssemblyName PresentationFramework
[System.Windows.MessageBox]::Show("%s", "备忘录提醒", 0)
`, r.Content)

	logger.Infof("[MemoReminder] Executing notification for reminder: %s", r.ID)

	// 通过 PowerShell 执行弹窗脚本
	cmd := exec.Command("powershell", "-NoProfile", "-Command", script)

	// 运行命令，忽略错误（弹窗命令可能返回非零退出码）
	if err := cmd.Run(); err != nil {
		logger.Warnf("[MemoReminder] Notification command failed: %v", err)
		// 继续处理，不中断流程
	}
}

func (a *Agent) startProgressReporter(ctx context.Context, taskID, runID string, manager internaltm.Manager) func() {
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

func (w *workflowNodeWorker) Execute(ctx context.Context, req orchestrator.ExecutionRequest) (orchestrator.ExecutionResult, error) {
	agent := w.agent
	taskID := strings.TrimSpace(req.TaskID)
	if taskID == "" {
		taskID = strings.TrimSpace(fmt.Sprint(req.Payload["task_id"]))
	}
	query := strings.TrimSpace(fmt.Sprint(req.Payload["query"]))
	if query == "" {
		query = strings.TrimSpace(fmt.Sprint(req.Payload["input"]))
	}
	if query == "" {
		query = strings.TrimSpace(fmt.Sprint(req.Payload["text"]))
	}
	logger.Infof("[TRACE] memoreminder.node_input task=%s node=%s type=%s query_len=%d payload=%s", taskID, strings.TrimSpace(req.NodeID), req.NodeType, len(query), snapshotAnyForLog(req.Payload, 2000))

	var (
		result orchestrator.ExecutionResult
		err    error
	)

	switch req.NodeType {
	case orchestrator.NodeTypeChatModel:
		result, err = w.executeChatModel(ctx, agent, req)
	case orchestrator.NodeTypeTool:
		result, err = w.executeSaveTool(ctx, req)
	default:
		err = fmt.Errorf("unknown node type: %s", req.NodeType)
	}

	if err != nil {
		logger.Infof("[TRACE] memoreminder.node_error task=%s node=%s type=%s err=%v", taskID, strings.TrimSpace(req.NodeID), req.NodeType, err)
		return orchestrator.ExecutionResult{}, err
	}
	logger.Infof("[TRACE] memoreminder.node_output task=%s node=%s type=%s output=%s", taskID, strings.TrimSpace(req.NodeID), req.NodeType, snapshotAnyForLog(result.Output, 2000))
	return result, nil
}

func (w *workflowNodeWorker) executeChatModel(ctx context.Context, agent *Agent, req orchestrator.ExecutionRequest) (orchestrator.ExecutionResult, error) {
	intent := "parse_reminder"
	if req.NodeConfig != nil {
		if v, ok := req.NodeConfig["intent"].(string); ok && strings.TrimSpace(v) != "" {
			intent = strings.TrimSpace(v)
		}
	}

	if intent == "ack_user" {
		ackPrompt, fallback := buildAckPrompt(req.Payload)
		logger.Infof("[TRACE] memoreminder.chatmodel ack_start task=%s query_len=%d", req.TaskID, len(ackPrompt))
		resp, err := agent.llmClient.ChatCompletion(ctx, agent.chatModel, []llm.Message{{Role: "user", Content: ackPrompt}}, nil, nil)
		if err != nil {
			logger.Warnf("[MemoReminder] ack llm failed, use fallback: %v", err)
			resp = fallback
		}
		resp = strings.TrimSpace(resp)
		if resp == "" {
			resp = fallback
		}
		logger.Infof("[TRACE] memoreminder.chatmodel ack_done task=%s resp=%s", req.TaskID, truncateForLog(resp, 300))
		return orchestrator.ExecutionResult{
			Output: map[string]any{
				"response": resp,
			},
			RawText:    resp,
			Duration:   time.Since(req.TriggeredAt),
			FinishedAt: time.Now(),
		}, nil
	}

	query := ""
	if v, ok := req.Payload["text"].(string); ok {
		query = strings.TrimSpace(v)
	}
	if query == "" {
		if v, ok := req.Payload["input"].(string); ok {
			query = strings.TrimSpace(v)
		}
	}
	if query == "" {
		if v, ok := req.Payload["query"].(string); ok {
			query = strings.TrimSpace(v)
		}
	}

	if query == "" {
		return orchestrator.ExecutionResult{}, fmt.Errorf("no text input")
	}

	prompt := fmt.Sprintf(
		"你是备忘录提醒助手。请从用户输入中提取提醒信息，输出纯 JSON 格式（不要包含其他文本）：\n"+
			"用户输入: %s\n\n"+
			"请输出格式（严格按此输出，只输出 JSON）：\n"+
			`{"id":"uuid","content":"提醒内容","remind_at":"2000-01-01T00:00:00Z","script":"PowerShell弹窗脚本内容","reminded":false}`,
		query,
	)

	logger.Infof("[TRACE] memoreminder.chatmodel start task=%s query_len=%d", req.TaskID, len(query))
	resp, err := agent.llmClient.ChatCompletion(ctx, agent.chatModel, []llm.Message{{Role: "user", Content: prompt}}, nil, nil)
	if err != nil {
		return orchestrator.ExecutionResult{}, err
	}

	resp = strings.TrimSpace(resp)
	if resp == "" {
		resp = `{"id":"","content":"","remind_at":"","script":"","reminded":false}`
	}
	logger.Infof("[TRACE] memoreminder.chatmodel extracted_raw task=%s raw=%s", req.TaskID, truncateForLog(resp, 1200))

	reminder, parseErr := parseReminderFromLLMResponse(resp)
	if parseErr != nil {
		logger.Warnf("[MemoReminder] Failed to parse JSON response: %v raw=%s", err, truncateForLog(resp, 1200))
	}
	if strings.TrimSpace(reminder.Content) == "" {
		reminder.Content = query
	}
	if reminder.RemindAt.IsZero() {
		reminder.RemindAt = time.Now().Add(30 * time.Minute)
	}
	if strings.TrimSpace(reminder.ID) == "" {
		reminder.ID = fmt.Sprintf("memo-%d", time.Now().UnixNano())
	}
	if strings.TrimSpace(reminder.Script) == "" {
		reminder.Script = reminder.Content
	}
	logger.Infof("[TRACE] memoreminder.chatmodel extracted_fields task=%s id=%s content=%s remind_at=%s reminded=%t script_len=%d",
		req.TaskID,
		strings.TrimSpace(reminder.ID),
		truncateForLog(strings.TrimSpace(reminder.Content), 200),
		reminder.RemindAt.Format(time.RFC3339),
		reminder.Reminded,
		len(strings.TrimSpace(reminder.Script)),
	)

	logger.Infof("[TRACE] memoreminder.chatmodel done task=%s resp_len=%d", req.TaskID, len(resp))

	normalizedJSON := fmt.Sprintf(`{"id":%q,"content":%q,"remind_at":%q,"script":%q,"reminded":%t}`,
		reminder.ID,
		reminder.Content,
		reminder.RemindAt.UTC().Format(time.RFC3339),
		reminder.Script,
		reminder.Reminded,
	)

	return orchestrator.ExecutionResult{
		Output: map[string]any{
			"response": normalizedJSON,
			"query":    fmt.Sprintf("提醒内容：%s；提醒时间：%s", reminder.Content, reminder.RemindAt.Format("2006-01-02 15:04")),
			"extracted": map[string]any{
				"id":        reminder.ID,
				"content":   reminder.Content,
				"remind_at": reminder.RemindAt.UTC().Format(time.RFC3339),
				"script":    reminder.Script,
				"reminded":  reminder.Reminded,
			},
		},
		RawText:    normalizedJSON,
		Duration:   time.Since(req.TriggeredAt),
		FinishedAt: time.Now(),
	}, nil
}

func (w *workflowNodeWorker) executeSaveTool(_ context.Context, req orchestrator.ExecutionRequest) (orchestrator.ExecutionResult, error) {
	reminder, err := reminderFromPayload(req.Payload)
	if err != nil {
		return orchestrator.ExecutionResult{}, err
	}

	if err := w.agent.saveReminderWithJSONTool(reminder); err != nil {
		return orchestrator.ExecutionResult{}, fmt.Errorf("save reminder failed: %w", err)
	}
	logger.Infof("[TRACE] memoreminder.save_tool done task=%s path=%s id=%s content=%s remind_at=%s",
		req.TaskID,
		remindersFilePath(),
		reminder.ID,
		truncateForLog(reminder.Content, 120),
		reminder.RemindAt.Format(time.RFC3339),
	)

	resp := fmt.Sprintf("提醒已写入，将在%s提醒你：%s", reminder.RemindAt.Format("2006-01-02 15:04"), reminder.Content)
	return orchestrator.ExecutionResult{
		Output: map[string]any{
			"response": resp,
			"saved": map[string]any{
				"id":        reminder.ID,
				"content":   reminder.Content,
				"remind_at": reminder.RemindAt.UTC().Format(time.RFC3339),
				"script":    reminder.Script,
				"reminded":  reminder.Reminded,
			},
		},
		RawText:    resp,
		Duration:   time.Since(req.TriggeredAt),
		FinishedAt: time.Now(),
	}, nil
}

func (w *workflowNodeWorker) ProcessNode(ctx context.Context, node *orchestrator.Node, input map[string]any) (map[string]any, error) {
	agent := w.agent
	if node.Type != "chat_model" {
		return nil, fmt.Errorf("unknown node type: %s", node.Type)
	}

	text, _ := input["text"].(string)
	if text == "" {
		text, _ = input["input"].(string)
	}

	if text == "" {
		return nil, fmt.Errorf("no text input")
	}

	prompt := fmt.Sprintf(
		"你是备忘录提醒助手。请从用户输入中提取提醒信息，输出 JSON格式。\n"+
			"用户输入: %s\n"+
			"请严格按照此 JSON 格式输出（不要包含其他文本）:\n"+
			`{"id":"","content":"","remind_at":"2000-01-01T00:00:00Z","script":"PowerShell 弹窗脚本","reminded":false}`,
		text,
	)

	resp, err := agent.llmClient.ChatCompletion(context.Background(), agent.chatModel, []llm.Message{{Role: "user", Content: prompt}}, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("llm call failed: %w", err)
	}

	return map[string]any{
		"response": resp,
	}, nil
}

func withTaskManager(ctx context.Context, manager internaltm.Manager) context.Context {
	return context.WithValue(ctx, ctxKeyTaskManager{}, manager)
}

func (a *Agent) emitStepEvent(ctx context.Context, manager internaltm.Manager, taskID string, nodeID string, state internalproto.StepState) {
	if manager == nil {
		return
	}
	messageZh := memoReminderNodeProgressText[nodeID]
	if messageZh == "" {
		messageZh = fmt.Sprintf("执行节点 %s", nodeID)
	}
	if state == internalproto.StepStateEnd {
		messageZh = "完成：" + messageZh
	}
	if state == internalproto.StepStateError {
		messageZh = "失败：" + messageZh
	}
	ev := internalproto.NewStepEvent("memoreminder", "workflow", nodeID, state, messageZh)
	text := messageZh
	if token, err := internalproto.EncodeStepToken(ev); err == nil {
		text = messageZh + "\n" + token
	}
	_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateWorking, &internalproto.Message{
		Role:  internalproto.MessageRoleAgent,
		Parts: []internalproto.Part{internalproto.NewTextPart(text)},
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

func truncateForLog(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if maxLen <= 0 || len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "...(truncated)"
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

func parseReminderFromLLMResponse(raw string) (Reminder, error) {
	clean := strings.TrimSpace(raw)
	if strings.HasPrefix(clean, "```") {
		clean = strings.TrimPrefix(clean, "```")
		clean = strings.TrimPrefix(clean, "json")
		clean = strings.TrimPrefix(clean, "JSON")
		clean = strings.TrimSuffix(clean, "```")
		clean = strings.TrimSpace(clean)
	}
	if s, ok := extractJSONObject(clean); ok {
		clean = s
	}

	var parsed struct {
		ID       string `json:"id"`
		Content  string `json:"content"`
		RemindAt string `json:"remind_at"`
		Script   string `json:"script"`
		Reminded bool   `json:"reminded"`
	}
	if err := json.Unmarshal([]byte(clean), &parsed); err != nil {
		return Reminder{}, err
	}

	remindAt, _ := parseRemindAt(parsed.RemindAt)
	return Reminder{
		ID:       strings.TrimSpace(parsed.ID),
		Content:  strings.TrimSpace(parsed.Content),
		RemindAt: remindAt,
		Script:   strings.TrimSpace(parsed.Script),
		Reminded: parsed.Reminded,
	}, nil
}

func parseRemindAt(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty remind_at")
	}
	layouts := []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006/01/02 15:04:05",
		"2006/01/02 15:04",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("unsupported remind_at format: %s", s)
}

func extractJSONObject(s string) (string, bool) {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end < 0 || end <= start {
		return "", false
	}
	return strings.TrimSpace(s[start : end+1]), true
}

func reminderFromPayload(payload map[string]any) (Reminder, error) {
	if payload == nil {
		return Reminder{}, fmt.Errorf("empty payload")
	}

	if parseOut, ok := payload["N_parse"].(map[string]any); ok {
		if extracted, ok := parseOut["extracted"].(map[string]any); ok {
			reminder := Reminder{
				ID:       strings.TrimSpace(fmt.Sprint(extracted["id"])),
				Content:  strings.TrimSpace(fmt.Sprint(extracted["content"])),
				Script:   strings.TrimSpace(fmt.Sprint(extracted["script"])),
				Reminded: strings.EqualFold(strings.TrimSpace(fmt.Sprint(extracted["reminded"])), "true"),
			}
			if remindAtRaw := strings.TrimSpace(fmt.Sprint(extracted["remind_at"])); remindAtRaw != "" {
				if t, err := parseRemindAt(remindAtRaw); err == nil {
					reminder.RemindAt = t
				}
			}
			if strings.TrimSpace(reminder.Content) != "" {
				if reminder.RemindAt.IsZero() {
					reminder.RemindAt = time.Now().Add(30 * time.Minute)
				}
				if reminder.ID == "" {
					reminder.ID = fmt.Sprintf("memo-%d", time.Now().UnixNano())
				}
				if reminder.Script == "" {
					reminder.Script = reminder.Content
				}
				return reminder, nil
			}
		}
	}

	return Reminder{}, fmt.Errorf("missing parsed reminder from payload")
}

func buildAckPrompt(payload map[string]any) (string, string) {
	fallback := "已收到你的提醒，我会按时提醒你。"
	if payload == nil {
		return "请用一句中文回复用户：已收到提醒，并说明会按时提醒。", fallback
	}
	savedContent := ""
	savedTime := ""
	if saveOut, ok := payload["N_write_json"].(map[string]any); ok {
		if saved, ok := saveOut["saved"].(map[string]any); ok {
			savedContent = strings.TrimSpace(fmt.Sprint(saved["content"]))
			savedTime = strings.TrimSpace(fmt.Sprint(saved["remind_at"]))
		}
	}
	if savedContent == "" && savedTime == "" {
		if saveOut, ok := payload["N_save"].(map[string]any); ok {
			if saved, ok := saveOut["saved"].(map[string]any); ok {
				savedContent = strings.TrimSpace(fmt.Sprint(saved["content"]))
				savedTime = strings.TrimSpace(fmt.Sprint(saved["remind_at"]))
			}
		}
	}
	if savedContent == "" {
		savedContent = strings.TrimSpace(fmt.Sprint(payload["text"]))
	}
	if t, err := parseRemindAt(savedTime); err == nil {
		savedTime = t.Format("2006-01-02 15:04")
	}
	if savedContent != "" && savedTime != "" {
		fallback = fmt.Sprintf("已收到，我会在%s提醒你：%s。", savedTime, savedContent)
	} else if savedContent != "" {
		fallback = fmt.Sprintf("已收到，我会按时提醒你：%s。", savedContent)
	}
	prompt := fmt.Sprintf("你是备忘录提醒助手。请用一句简短中文确认：已收到提醒，并明确提醒时间和内容。\n提醒时间：%s\n提醒内容：%s\n只输出给用户的话，不要JSON。", savedTime, savedContent)
	return prompt, fallback
}

func remindersFilePath() string {
	absPath, err := filepath.Abs(remindersFile)
	if err != nil {
		return remindersFile
	}
	return absPath
}

func loadReminders() ([]Reminder, error) {
	path := remindersFilePath()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Reminder{}, nil
		}
		return nil, err
	}
	defer f.Close()
	var reminders []Reminder
	if err := json.NewDecoder(f).Decode(&reminders); err != nil {
		return nil, err
	}
	return reminders, nil
}

func saveReminders(reminders []Reminder) error {
	path := remindersFilePath()
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(reminders)
}

func saveReminderToFile(reminder Reminder) error {
	reminders, err := loadReminders()
	if err != nil {
		logger.Warnf("[MemoReminder] load reminders failed, will overwrite file: %v", err)
		reminders = []Reminder{}
	}
	reminders = append(reminders, reminder)
	return saveReminders(reminders)
}

func (a *Agent) saveReminderWithJSONTool(reminder Reminder) error {
	if a == nil || a.JsonFileTool == nil {
		logger.Warnf("[TRACE] memoreminder.json_file fallback reason=nil_tool path=%s", remindersFilePath())
		return saveReminderToFile(reminder)
	}

	reminders, err := loadReminders()
	if err != nil {
		logger.Warnf("[MemoReminder] load reminders failed before json_file write: %v", err)
		reminders = []Reminder{}
	}
	reminders = append(reminders, reminder)

	payload := make([]map[string]any, 0, len(reminders))
	for _, item := range reminders {
		payload = append(payload, map[string]any{
			"id":        item.ID,
			"content":   item.Content,
			"remind_at": item.RemindAt.UTC().Format(time.RFC3339),
			"script":    item.Script,
			"reminded":  item.Reminded,
		})
	}

	logger.Infof("[TRACE] memoreminder.json_file start path=%s count=%d", remindersFile, len(payload))
	_, execErr := a.JsonFileTool.Execute(context.Background(), map[string]any{
		"action": "write",
		"path":   remindersFile,
		"json":   payload,
	})
	if execErr != nil {
		logger.Warnf("[TRACE] memoreminder.json_file error path=%s err=%v", remindersFile, execErr)
		return execErr
	}
	logger.Infof("[TRACE] memoreminder.json_file done path=%s count=%d", remindersFile, len(payload))
	return nil
}

func buildMemoReminderWorkflow() (*orchestrator.Workflow, error) {
	wf, err := orchestrator.NewWorkflow(MemoReminderWorkflowID, "memoreminder default workflow")
	if err != nil {
		return nil, err
	}

	if err = wf.AddNode(orchestrator.Node{ID: "N_start", Type: orchestrator.NodeTypeStart}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{
		ID:       "N_parse",
		Type:     orchestrator.NodeTypeChatModel,
		AgentID:  MemoReminderWorkflowWorkerID,
		TaskType: "chat_model",
		Config: map[string]any{
			"intent": "parse_reminder",
		},
		PreInput: "提取并结构化备忘录提醒信息。",
	}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{
		ID:       "N_write_json",
		Type:     orchestrator.NodeTypeTool,
		AgentID:  MemoReminderWorkflowWorkerID,
		TaskType: "tool",
		Config: map[string]any{
			"tool_name": "write_json",
		},
		PreInput: "将解析结果写入 reminders.json。",
	}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{
		ID:       "N_ack",
		Type:     orchestrator.NodeTypeChatModel,
		AgentID:  MemoReminderWorkflowWorkerID,
		TaskType: "chat_model",
		Config: map[string]any{
			"intent": "ack_user",
		},
		PreInput: "根据已写入提醒，向用户确认提醒时间和内容。",
	}); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{ID: "N_end", Type: orchestrator.NodeTypeEnd}); err != nil {
		return nil, err
	}

	if err = wf.AddEdge("N_start", "N_parse"); err != nil {
		return nil, err
	}
	if err = wf.AddEdge("N_parse", "N_write_json"); err != nil {
		return nil, err
	}
	if err = wf.AddEdge("N_write_json", "N_ack"); err != nil {
		return nil, err
	}
	if err = wf.AddEdge("N_ack", "N_end"); err != nil {
		return nil, err
	}

	return wf, nil
}
