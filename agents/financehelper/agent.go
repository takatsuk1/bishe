package financehelper

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
	FinanceHelperWorkflowID       = "financehelper-default"
	FinanceHelperWorkflowWorkerID = "financehelper_worker"
	financeGuestUserID            = "guest"
	financeTablePlaceholder       = "__USER_BILL_TABLE__"
)

type ctxKeyTaskManager struct{}

type Agent struct {
	orchestratorEngine orchestrator.Engine
	llmClient          *llm.Client
	chatModel          string
	MySQLTool          tools.Tool
	AkshareTool        tools.Tool
	akshareToolCatalog string
	akshareToolSchema  string
	akshareToolInfos   []tools.ToolInfo
}

type workflowNodeWorker struct {
	agent *Agent
}

type financePlan struct {
	Action          string         `json:"action"`
	Summary         string         `json:"summary"`
	TableName       string         `json:"table_name,omitempty"`
	EnsureTableSQL  string         `json:"ensure_table_sql"`
	SQLStatements   []string       `json:"sql_statements"`
	AkshareToolName string         `json:"akshare_tool_name"`
	AkshareArgs     map[string]any `json:"akshare_arguments"`
}

type toolExecutionRecord struct {
	Step       string         `json:"step"`
	ToolName   string         `json:"tool_name"`
	Request    map[string]any `json:"request,omitempty"`
	Response   map[string]any `json:"response,omitempty"`
	ParsedJSON any            `json:"parsed_json,omitempty"`
	Text       string         `json:"text,omitempty"`
}

type financeSchemaRecord struct {
	UserID        string              `json:"user_id"`
	TableName     string              `json:"table_name"`
	Columns       []string            `json:"columns"`
	ColumnTypes   map[string]string   `json:"column_types,omitempty"`
	ColumnSpecs   []string            `json:"column_specs,omitempty"`
	ColumnMeta    []financeColumnMeta `json:"column_meta,omitempty"`
	SemanticToCol map[string]string   `json:"semantic_to_col"`
	SemanticDesc  map[string]string   `json:"semantic_desc,omitempty"`
}

type financeColumnMeta struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Meaning string `json:"meaning"`
}

var financeHelperNodeProgressText = map[string]string{
	"N_start":          "初始化财务助理任务",
	"N_plan":           "识别意图并规划执行步骤",
	"N_is_ledger":      "判断是否为记账请求",
	"N_route_report":   "同步意图用于报告判断",
	"N_mysql_ledger":   "写入或更新用户账单数据",
	"N_is_report":      "判断是否为财务报告请求",
	"N_route_news":     "同步意图用于资讯判断",
	"N_mysql_report":   "查询指定时间范围账单",
	"N_is_news":        "判断是否为财经资讯请求",
	"N_akshare_news":   "读取金融资讯并整理",
	"N_mysql_advice":   "查询当前财务情况",
	"N_akshare_advice": "读取市场信息用于理财建议",
	"N_respond":        "汇总结果并生成回复",
	"N_end":            "输出最终财务结果",
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
	logger.Infof("[TRACE] financehelper llm_config url=%s model=%s api_key_set=%t", strings.TrimSpace(cfg.LLM.URL), agent.chatModel, strings.TrimSpace(cfg.LLM.APIKey) != "")

	mysqlDSN := strings.TrimSpace(cfg.MySQL.DSN)
	agent.MySQLTool = tools.NewMCPTool(
		"mysql_exec",
		"本地 MySQL MCP 服务，执行账单相关 SQL",
		[]tools.ToolParameter{
			{Name: "sql", Type: tools.ParamTypeString, Required: true, Description: "要执行的 SQL 语句"},
		},
		tools.MCPToolConfig{
			Mode:     "stdio",
			Command:  "go",
			Args:     []string{"run", "./tools/mysqlmcp", "--dsn", mysqlDSN},
			ToolName: "mysql_exec",
		},
	)
	agent.AkshareTool = tools.NewMCPTool(
		"akshare-one-mcp",
		"本地 AkShare MCP 服务，由 Agent 选择具体金融子工具",
		[]tools.ToolParameter{
			{Name: "tool_name", Type: tools.ParamTypeString, Required: true, Description: "AkShare MCP 子工具名"},
			{Name: "arguments", Type: tools.ParamTypeObject, Required: false, Description: "AkShare MCP 调用参数"},
			{Name: "query", Type: tools.ParamTypeString, Required: false, Description: "兼容字段"},
		},
		tools.MCPToolConfig{
			Mode:     "stdio",
			Command:  "uvx",
			Args:     []string{"akshare-one-mcp"},
			ToolName: "auto",
		},
	)
	agent.akshareToolCatalog = "Available AkShare MCP tools: (discovery unavailable)"
	agent.akshareToolSchema = "AkShare tool parameter schema: (discovery unavailable)"
	infos, cat, schema := discoverAkshareToolCatalogAndSchema()
	agent.akshareToolCatalog = cat
	agent.akshareToolSchema = schema
	agent.akshareToolInfos = infos
	logger.Infof("[TRACE] financehelper startup akshare catalog=%s", truncateText(agent.akshareToolCatalog, 800))
	logger.Infof("[TRACE] financehelper startup akshare schema=%s", truncateText(agent.akshareToolSchema, 1200))
	logger.Infof("[TRACE] financehelper startup akshare catalog=%s", truncateText(agent.akshareToolCatalog, 800))
	logger.Infof("[TRACE] financehelper startup akshare schema=%s", truncateText(agent.akshareToolSchema, 1200))

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
		logger.Infof("[TRACE] financehelper monitor enabled")
	} else {
		logger.Infof("[TRACE] financehelper monitor disabled: mysql unavailable")
	}

	agent.orchestratorEngine = orchestrator.NewEngine(engineCfg, orchestrator.NewInMemoryAgentRegistry())
	if err := agent.orchestratorEngine.RegisterWorker(orchestrator.AgentDescriptor{
		ID:           FinanceHelperWorkflowWorkerID,
		Name:         "financehelper workflow worker",
		Capabilities: []orchestrator.AgentCapability{"chat_model", "tool", "financehelper"},
	}, &workflowNodeWorker{agent: agent}); err != nil {
		return nil, err
	}

	wf, err := buildFinanceHelperWorkflow()
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
	userID := financeGuestUserID
	if initialMsg.Metadata != nil {
		userID = normalizeUserID(initialMsg.Metadata["user_id"])
		if userID == financeGuestUserID {
			userID = normalizeUserID(initialMsg.Metadata["userId"])
		}
		if userID == financeGuestUserID {
			userID = normalizeUserID(initialMsg.Metadata["UserID"])
		}
	}

	runID, err := a.orchestratorEngine.StartWorkflow(ctx, FinanceHelperWorkflowID, map[string]any{
		"task_id": taskID,
		"query":   query,
		"text":    query,
		"input":   query,
		"user_id": userID,
	})
	if err != nil {
		return fmt.Errorf("failed to start financehelper workflow: %w", err)
	}
	stopProgress := a.startProgressReporter(ctx, taskID, runID, manager)
	defer stopProgress()

	runResult, err := a.orchestratorEngine.WaitRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("failed to wait financehelper workflow: %w", err)
	}
	if runResult.State != orchestrator.RunStateSucceeded {
		if runResult.ErrorMessage != "" {
			return fmt.Errorf("financehelper workflow failed: %s", runResult.ErrorMessage)
		}
		return fmt.Errorf("financehelper workflow failed")
	}

	out := strings.TrimSpace(fmt.Sprint(runResult.FinalOutput["response"]))
	if out == "" {
		out = agentfmt.Clean(a.fallbackResponse(query, runResult.FinalOutput))
	}
	if strings.TrimSpace(out) == "" {
		out = "Workflow executed successfully"
	}
	streamedFinal := financeStreamedToUser(runResult)
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
	logger.Infof("[TRACE] financehelper.node_input task=%s node=%s type=%s query_len=%d payload=%s", taskID, strings.TrimSpace(req.NodeID), req.NodeType, len(strings.TrimSpace(query)), snapshotAnyForLog(req.Payload, 2000))

	var (
		output map[string]any
		err    error
	)

	switch req.NodeType {
	case orchestrator.NodeTypeChatModel:
		output, err = w.agent.callChatModel(ctx, taskID, query, req.NodeID, req.NodeConfig, req.Payload)
	case orchestrator.NodeTypeTool:
		output, err = w.agent.callTool(ctx, taskID, query, req.NodeID, req.NodeConfig, req.Payload)
	default:
		response := strings.TrimSpace(query)
		if response == "" {
			response = "ok"
		}
		output = map[string]any{"response": response}
	}
	if err != nil {
		logger.Infof("[TRACE] financehelper.node_error task=%s node=%s type=%s err=%v", taskID, strings.TrimSpace(req.NodeID), req.NodeType, err)
		return orchestrator.ExecutionResult{}, err
	}
	logger.Infof("[TRACE] financehelper.node_output task=%s node=%s type=%s output=%s", taskID, strings.TrimSpace(req.NodeID), req.NodeType, snapshotAnyForLog(output, 2000))
	return orchestrator.ExecutionResult{Output: output}, nil
}

func (a *Agent) callChatModel(ctx context.Context, taskID string, query string, nodeID string, nodeCfg map[string]any, payload map[string]any) (map[string]any, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query is empty")
	}
	originalQuery := extractFinanceUserQuery(payload, query)

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
	if baseURL == "" || model == "" {
		return nil, fmt.Errorf("chat_model config missing url/model")
	}

	switch intent {
	case "route_action":
		action := normalizeAction(strings.TrimSpace(fmt.Sprint(payload["action"])))
		if action == "" {
			plan := planFromPayload(payload)
			action = normalizeAction(plan.Action)
		}
		if action == "" {
			action = "advice"
		}
		return map[string]any{"response": action, "action": action}, nil
	case "plan_request":
		userID := normalizeUserID(payload["user_id"])
		tableMetaHint := a.buildUserTableMetadataHint(ctx, userID)
		prompt := buildPlanPrompt(originalQuery, userID, a.akshareToolCatalog, a.akshareToolSchema, tableMetaHint)
		logger.Infof("[financehelper] planner_prompt task=%s node=%s user=%s prompt=\n%s", taskID, nodeID, userID, prompt)
		resp, _, err := a.streamLLMResponse(ctx, taskID, nodeID, baseURL, apiKey, model, prompt, false)
		if err != nil {
			logger.Warnf("[financehelper] plan llm failed task=%s node=%s err=%v, using fallback", taskID, nodeID, err)
			plan := finalizePlan(buildFallbackPlan(originalQuery), userID)
			return planToOutput(plan), nil
		}
		plan, err := decodeFinancePlan(resp)
		if err != nil {
			logger.Warnf("[financehelper] invalid plan json task=%s node=%s err=%v, using fallback", taskID, nodeID, err)
			plan = buildFallbackPlan(originalQuery)
		}
		plan = finalizePlan(plan, userID)
		return planToOutput(plan), nil
	case "final_response":
		prompt := buildResponsePrompt(originalQuery, payload)
		resp, streamedToUser, err := a.streamLLMResponse(ctx, taskID, nodeID, baseURL, apiKey, model, prompt, true)
		if err != nil {
			logger.Warnf("[financehelper] response llm failed task=%s node=%s err=%v, using fallback", taskID, nodeID, err)
			return map[string]any{"response": a.fallbackResponse(originalQuery, payload)}, nil
		}
		resp = strings.TrimSpace(resp)
		if resp == "" {
			resp = a.fallbackResponse(originalQuery, payload)
		}
		output := map[string]any{"response": agentfmt.Clean(resp)}
		if streamedToUser {
			output["streamed_to_user"] = true
		}
		return output, nil
	default:
		resp, _, err := a.streamLLMResponse(ctx, taskID, nodeID, baseURL, apiKey, model, query, false)
		if err != nil {
			return nil, err
		}
		resp = strings.TrimSpace(resp)
		if resp == "" {
			resp = "(empty LLM response)"
		}
		return map[string]any{"response": resp}, nil
	}
}

func (a *Agent) callTool(ctx context.Context, taskID string, query string, nodeID string, nodeCfg map[string]any, payload map[string]any) (map[string]any, error) {
	toolName := ""
	purpose := ""
	if nodeCfg != nil {
		if v, ok := nodeCfg["tool_name"].(string); ok {
			toolName = strings.TrimSpace(v)
		}
		if v, ok := nodeCfg["purpose"].(string); ok {
			purpose = strings.TrimSpace(v)
		}
	}
	if toolName == "" {
		return nil, fmt.Errorf("tool node missing config.tool_name")
	}
	a.emitSemanticStep(ctx, taskID, "financehelper.tool.start", internalproto.StepStateInfo, "正在调用工具："+toolName)

	originalQuery := extractFinanceUserQuery(payload, query)
	plan := planFromPayload(payload)
	if plan.Action == "" {
		plan = finalizePlan(buildFallbackPlan(originalQuery), normalizeUserID(payload["user_id"]))
	}
	effectivePurpose := strings.ToLower(strings.TrimSpace(purpose))
	plannedAction := strings.ToLower(strings.TrimSpace(plan.Action))

	switch toolName {
	case "mysql_exec":
		userID := normalizeUserID(payload["user_id"])
		// New engine condition semantics use latest node output. In this workflow,
		// non-ledger branches may converge on advice nodes, so dispatch by planned action.
		if effectivePurpose == "advice" {
			switch plannedAction {
			case "ledger", "report", "advice":
				effectivePurpose = plannedAction
			case "news":
				return map[string]any{"response": "news action does not require mysql", "records": []any{}, "result": []any{}}, nil
			}
		}
		return a.executeMySQLPurpose(ctx, taskID, nodeID, effectivePurpose, originalQuery, userID, plan)
	case "akshare-one-mcp":
		if effectivePurpose == "advice" {
			switch plannedAction {
			case "news", "advice":
				effectivePurpose = plannedAction
			case "ledger", "report":
				return map[string]any{"response": "no market tool call required", "records": []any{}, "result": []any{}}, nil
			}
		}
		return a.executeAksharePurpose(ctx, taskID, nodeID, effectivePurpose, plan)
	default:
		return nil, fmt.Errorf("tool %s not found", toolName)
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

func (a *Agent) emitSemanticStep(ctx context.Context, taskID string, name string, state internalproto.StepState, message string) {
	manager := taskManagerFromContext(ctx)
	if manager == nil {
		return
	}
	ev := internalproto.NewStepEvent("financehelper", "semantic", strings.TrimSpace(name), state, strings.TrimSpace(message))
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

func financeStreamedToUser(runResult orchestrator.RunResult) bool {
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
					a.emitStepEvent(ctx, manager, taskID, nodeID, internalproto.StepStateStart)
				}
				for _, nr := range run.NodeResults {
					id := strings.TrimSpace(nr.NodeID)
					if id == "" || finished[id] {
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
	messageZh := financeHelperNodeProgressText[nodeID]
	if messageZh == "" {
		messageZh = fmt.Sprintf("执行节点 %s", nodeID)
	}
	if state == internalproto.StepStateEnd {
		messageZh = "完成：" + messageZh
	}
	if state == internalproto.StepStateError {
		messageZh = "失败：" + messageZh
	}
	ev := internalproto.NewStepEvent("financehelper", "workflow", nodeID, state, messageZh)
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
