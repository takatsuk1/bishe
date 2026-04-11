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
	"encoding/json"
	"fmt"
	"regexp"
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
	agent.akshareToolCatalog, agent.akshareToolSchema = discoverAkshareToolCatalogAndSchema()

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
		resp, err := a.streamLLMResponse(ctx, taskID, nodeID, baseURL, apiKey, model, prompt)
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
		resp, err := a.streamLLMResponse(ctx, taskID, nodeID, baseURL, apiKey, model, prompt)
		if err != nil {
			logger.Warnf("[financehelper] response llm failed task=%s node=%s err=%v, using fallback", taskID, nodeID, err)
			return map[string]any{"response": a.fallbackResponse(originalQuery, payload)}, nil
		}
		resp = strings.TrimSpace(resp)
		if resp == "" {
			resp = a.fallbackResponse(originalQuery, payload)
		}
		return map[string]any{"response": agentfmt.Clean(resp)}, nil
	default:
		resp, err := a.streamLLMResponse(ctx, taskID, nodeID, baseURL, apiKey, model, query)
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

func (a *Agent) streamLLMResponse(ctx context.Context, taskID string, nodeID string, baseURL string, apiKey string, model string, prompt string) (string, error) {
	a.emitSemanticStep(ctx, taskID, "financehelper.llm.start", internalproto.StepStateInfo, "正在调用大模型："+strings.TrimSpace(nodeID))
	client := llm.NewClient(baseURL, apiKey)
	var streamBuf strings.Builder
	lastEmitAt := time.Time{}
	resp, err := client.ChatCompletionStream(ctx, model, []llm.Message{{Role: "user", Content: prompt}}, nil, nil, func(delta string) error {
		if strings.TrimSpace(delta) == "" {
			return nil
		}
		streamBuf.WriteString(delta)
		if !lastEmitAt.IsZero() && time.Since(lastEmitAt) < 150*time.Millisecond {
			return nil
		}
		lastEmitAt = time.Now()
		a.emitSemanticStep(ctx, taskID, "financehelper.llm.delta", internalproto.StepStateInfo, "正在调用大模型："+truncateText(streamBuf.String(), 140))
		return nil
	})
	if err != nil {
		return "", err
	}
	a.emitSemanticStep(ctx, taskID, "financehelper.llm.end", internalproto.StepStateEnd, "完成：大模型处理")
	return strings.TrimSpace(resp), nil
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

func (a *Agent) buildUserTableMetadataHint(ctx context.Context, userID string) string {
	userID = normalizeUserID(userID)
	tableName := financeTableName(userID)
	fallback := fmt.Sprintf("table=%s; column_meta=[]", tableName)

	tool, err := a.findToolByName("mysql_exec")
	if err != nil || tool == nil {
		return fallback
	}
	_, _ = tool.Execute(ctx, map[string]any{"sql": ensureSchemaRegistryTableSQL()})

	rec, recErr := loadSchemaRecord(ctx, tool, userID)
	if recErr != nil || strings.TrimSpace(rec.TableName) == "" {
		cols, colErr := fetchTableColumns(ctx, tool, tableName)
		if colErr == nil && len(cols) > 0 {
			sem := inferSemanticMapping(cols)
			rec = financeSchemaRecord{
				UserID:        userID,
				TableName:     tableName,
				Columns:       cols,
				SemanticToCol: sem,
			}
			if colTypes, typeErr := fetchTableColumnTypes(ctx, tool, tableName); typeErr == nil {
				rec.ColumnTypes = colTypes
			}
			a.enrichSchemaRecordSemantics(ctx, &rec)
			_ = saveSchemaRecord(ctx, tool, rec)
		}
	}
	if strings.TrimSpace(rec.TableName) == "" {
		return fallback
	}
	a.enrichSchemaRecordSemantics(ctx, &rec)
	metaJSON, _ := json.Marshal(rec.ColumnMeta)
	return fmt.Sprintf("table=%s; column_meta=%s", rec.TableName, string(metaJSON))
}

func (a *Agent) executeMySQLPurpose(ctx context.Context, taskID string, nodeID string, purpose string, userQuery string, userID string, plan financePlan) (map[string]any, error) {
	tool, err := a.findToolByName("mysql_exec")
	if err != nil {
		return nil, err
	}

	execWithLog := func(step string, sqlText string, records *[]toolExecutionRecord, parsedRows *[]any) (any, string, error) {
		sqlText = strings.TrimSpace(sqlText)
		if sqlText == "" {
			return nil, "", nil
		}
		logger.Infof("[financehelper] mysql_exec task=%s node=%s purpose=%s step=%s sql=%s", taskID, nodeID, purpose, step, truncateText(strings.ReplaceAll(sqlText, "\n", " "), 600))
		out, runErr := tool.Execute(ctx, map[string]any{"sql": sqlText})
		if runErr != nil {
			logger.Warnf("[financehelper] mysql_exec failed task=%s node=%s purpose=%s step=%s err=%v", taskID, nodeID, purpose, step, runErr)
			return nil, "", runErr
		}
		text := extractToolText(out)
		parsed := parseJSONLikeText(text)
		*records = append(*records, toolExecutionRecord{
			Step:       step,
			ToolName:   "mysql_exec",
			Request:    map[string]any{"sql": sqlText},
			Response:   out,
			ParsedJSON: parsed,
			Text:       truncateText(text, 600),
		})
		if parsed != nil {
			*parsedRows = append(*parsedRows, parsed)
		}
		return parsed, text, nil
	}

	if purpose == "ledger" {
		logger.Infof("[financehelper] ledger_path start task=%s node=%s user_id=%s", taskID, nodeID, userID)
		tableName := strings.TrimSpace(plan.TableName)
		if tableName == "" {
			tableName = financeTableName(userID)
		}
		logger.Infof("[financehelper] ledger_path table_resolved task=%s table=%s", taskID, tableName)
		records := make([]toolExecutionRecord, 0, 8)
		parsedRows := make([]any, 0, 4)
		summaryParts := make([]string, 0, 4)

		if _, _, runErr := execWithLog("schema_registry_ensure", ensureSchemaRegistryTableSQL(), &records, &parsedRows); runErr != nil {
			return nil, fmt.Errorf("mysql_exec schema registry ensure failed: %w", runErr)
		}
		if _, _, runErr := execWithLog("ledger_ensure_table", defaultEnsureTableSQL(tableName), &records, &parsedRows); runErr != nil {
			return nil, fmt.Errorf("mysql_exec ensure ledger table failed: %w", runErr)
		}

		cols, colErr := fetchTableColumns(ctx, tool, tableName)
		if colErr != nil {
			return nil, fmt.Errorf("load ledger schema failed: %w", colErr)
		}
		if len(cols) == 0 {
			logger.Infof("[financehelper] ledger_path schema_source=fallback_create_sql task=%s table=%s", taskID, tableName)
			cols = inferColumnsFromCreateSQL(defaultEnsureTableSQL(tableName))
		} else {
			logger.Infof("[financehelper] ledger_path schema_source=table_columns task=%s table=%s columns=%d", taskID, tableName, len(cols))
		}
		rec := financeSchemaRecord{
			UserID:        userID,
			TableName:     tableName,
			Columns:       uniqueStrings(cols),
			SemanticToCol: inferSemanticMapping(cols),
		}
		if colTypes, typeErr := fetchTableColumnTypes(ctx, tool, tableName); typeErr == nil {
			rec.ColumnTypes = colTypes
		}
		a.enrichSchemaRecordSemantics(ctx, &rec)
		if saveErr := saveSchemaRecord(ctx, tool, rec); saveErr != nil {
			return nil, fmt.Errorf("save ledger schema metadata failed: %w", saveErr)
		}

		ledgerSQLs, genErr := a.generateLedgerSQLBySchema(ctx, userQuery, rec)
		logger.Infof("[financehelper] ledger_path llm_sql_generated task=%s sql_count=%d llm_err=%v", taskID, len(ledgerSQLs), genErr)
		if genErr != nil || len(ledgerSQLs) == 0 {
			if fallback := strings.TrimSpace(buildLedgerInsertSQL(rec, tableName, userQuery)); fallback != "" {
				logger.Infof("[financehelper] ledger_path use_fallback_insert_sql task=%s", taskID)
				ledgerSQLs = []string{fallback}
			} else {
				logger.Warnf("[financehelper] ledger_path fallback_insert_empty task=%s", taskID)
			}
		} else {
			logger.Infof("[financehelper] ledger_path use_llm_sql task=%s", taskID)
		}
		if len(ledgerSQLs) == 0 {
			return nil, fmt.Errorf("unable to generate ledger SQL")
		}

		for idx, sqlText := range ledgerSQLs {
			sqlText = normalizeSQLForMySQL(sqlText)
			parsed, text, runErr := execWithLog(fmt.Sprintf("ledger_%d", idx+1), sqlText, &records, &parsedRows)
			if runErr != nil {
				return nil, fmt.Errorf("mysql_exec step %d failed: %w", idx+1, runErr)
			}
			if summary := summarizeSQLResult(sqlText, parsed, text); summary != "" {
				summaryParts = append(summaryParts, summary)
			}
		}

		return map[string]any{
			"response": strings.Join(summaryParts, "\n"),
			"records":  records,
			"result":   parsedRows,
		}, nil
	}

	sqlList := make([]string, 0, len(plan.SQLStatements)+1)
	if strings.TrimSpace(plan.EnsureTableSQL) != "" {
		sqlList = append(sqlList, plan.EnsureTableSQL)
	}
	tableName := strings.TrimSpace(plan.TableName)
	if tableName == "" {
		tableName = extractTableNameFromSQL(plan.EnsureTableSQL)
	}
	sqlList = append(sqlList, plan.SQLStatements...)
	if purpose == "advice" && len(plan.SQLStatements) == 0 {
		sqlList = append(sqlList, fmt.Sprintf("SELECT * FROM %s ORDER BY bill_date DESC, id DESC LIMIT 20", financeTableName(financeGuestUserID)))
	}
	if len(sqlList) == 0 {
		return map[string]any{"response": "无需执行数据库操作", "records": []any{}}, nil
	}

	records := make([]toolExecutionRecord, 0, len(sqlList)+8)
	parsedRows := make([]any, 0, len(sqlList))
	summaryParts := make([]string, 0, len(sqlList))
	schemaRec := financeSchemaRecord{}
	if tableName != "" && purpose != "news" {
		extraRecords, extraSummary, ensureErr := a.ensureSchemaColumns(ctx, tool, tableName)
		if ensureErr != nil {
			logger.Warnf("[financehelper] mysql_exec schema_ensure failed task=%s node=%s purpose=%s table=%s err=%v", taskID, nodeID, purpose, tableName, ensureErr)
			return nil, ensureErr
		}
		records = append(records, extraRecords...)
		summaryParts = append(summaryParts, extraSummary...)
		if rec, recErr := a.loadOrBuildSchemaRecord(ctx, tool, userID, tableName, plan.EnsureTableSQL); recErr == nil {
			schemaRec = rec
			_ = a.refreshAndSaveSchemaRecord(ctx, tool, userID, tableName, &schemaRec)
		}
	}
	for idx, sqlText := range sqlList {
		sqlText = strings.TrimSpace(sqlText)
		if sqlText == "" {
			continue
		}
		if tableName != "" && purpose != "news" {
			sqlText = rewriteSQLBySchema(sqlText, schemaRec)
		}
		logger.Infof("[financehelper] mysql_exec task=%s node=%s purpose=%s step=%s sql=%s", taskID, nodeID, purpose, fmt.Sprintf("%s_%d", purpose, idx+1), truncateText(strings.ReplaceAll(sqlText, "\n", " "), 600))
		out, err := tool.Execute(ctx, map[string]any{"sql": sqlText})
		if err != nil {
			toolText := truncateText(extractToolText(out), 800)
			logger.Warnf("[financehelper] mysql_exec failed task=%s node=%s purpose=%s step=%s err=%v tool_text=%s", taskID, nodeID, purpose, fmt.Sprintf("%s_%d", purpose, idx+1), err, toolText)
			if strings.TrimSpace(toolText) != "" {
				return nil, fmt.Errorf("mysql_exec step %d failed: %w; tool_text=%s", idx+1, err, toolText)
			}
			return nil, fmt.Errorf("mysql_exec step %d failed: %w", idx+1, err)
		}
		text := extractToolText(out)
		parsed := parseJSONLikeText(text)
		records = append(records, toolExecutionRecord{
			Step:       fmt.Sprintf("%s_%d", purpose, idx+1),
			ToolName:   "mysql_exec",
			Request:    map[string]any{"sql": sqlText},
			Response:   out,
			ParsedJSON: parsed,
			Text:       truncateText(text, 600),
		})
		if parsed != nil {
			parsedRows = append(parsedRows, parsed)
		}
		if summary := summarizeSQLResult(sqlText, parsed, text); summary != "" {
			summaryParts = append(summaryParts, summary)
		}
	}

	return map[string]any{
		"response": strings.Join(summaryParts, "\n"),
		"records":  records,
		"result":   parsedRows,
	}, nil
}

func (a *Agent) executeAksharePurpose(ctx context.Context, taskID string, nodeID string, purpose string, plan financePlan) (map[string]any, error) {
	tool, err := a.findToolByName("akshare-one-mcp")
	if err != nil {
		return nil, err
	}

	toolName := normalizeAkshareToolName(plan.AkshareToolName, purpose)
	args := cloneAnyMap(plan.AkshareArgs)
	if toolName == "" {
		toolName, args = fallbackAkshareRequestV2(purpose)
	}
	if len(args) == 0 {
		args = map[string]any{}
	}
	plannedCalls := buildAkshareCallPlan(args)
	candidates := a.akshareToolCandidates(toolName, purpose)

	records := make([]toolExecutionRecord, 0, len(plannedCalls)+len(candidates))
	parsedResults := make([]any, 0, len(plannedCalls)+len(candidates))
	summaryParts := make([]string, 0, len(plannedCalls)+len(candidates))
	successCount := 0
	var lastErr error
	attempt := 0

	runOne := func(usedToolName string, candidateArgs map[string]any, totalHint int) {
		attempt++
		logger.Infof("[financehelper] akshare_exec task=%s node=%s purpose=%s attempt=%d/%d tool_name=%s", taskID, nodeID, purpose, attempt, totalHint, usedToolName)
		out, runErr := tool.Execute(ctx, map[string]any{
			"tool_name": usedToolName,
			"arguments": candidateArgs,
		})
		if runErr != nil {
			lastErr = runErr
			logger.Warnf("[financehelper] akshare_exec failed task=%s node=%s purpose=%s tool_name=%s err=%v", taskID, nodeID, purpose, usedToolName, runErr)
			return
		}

		text := extractToolText(out)
		parsed := parseJSONLikeText(text)
		records = append(records, toolExecutionRecord{
			Step:       fmt.Sprintf("%s_%d", purpose, attempt),
			ToolName:   usedToolName,
			Request:    map[string]any{"tool_name": usedToolName, "arguments": candidateArgs},
			Response:   out,
			ParsedJSON: parsed,
			Text:       truncateText(text, 1200),
		})
		parsedResults = append(parsedResults, parsed)
		if summary := summarizeAkshareResult(usedToolName, candidateArgs, parsed, text); strings.TrimSpace(summary) != "" {
			summaryParts = append(summaryParts, summary)
		}
		successCount++
	}

	totalHint := len(plannedCalls) + len(candidates)
	for _, call := range plannedCalls {
		usedToolName := strings.TrimSpace(call.ToolName)
		if usedToolName == "" {
			continue
		}
		candidateArgs := buildAkshareArgumentsForTool(usedToolName, call.Arguments, purpose)
		runOne(usedToolName, candidateArgs, totalHint)
		if successCount >= akshareEnoughCount(purpose) {
			break
		}
	}
	if successCount < akshareEnoughCount(purpose) {
		for _, candidate := range candidates {
			usedToolName := strings.TrimSpace(candidate)
			candidateArgs := buildAkshareArgumentsForTool(usedToolName, args, purpose)
			runOne(usedToolName, candidateArgs, totalHint)
			if successCount >= akshareEnoughCount(purpose) {
				break
			}
		}
	}
	if successCount == 0 {
		if lastErr == nil {
			lastErr = fmt.Errorf("no akshare candidate succeeded")
		}
		return nil, fmt.Errorf("akshare tool failed: %w", lastErr)
	}
	response := strings.Join(summaryParts, "\n")
	if strings.TrimSpace(response) == "" {
		response = fmt.Sprintf("AkShare 调用成功 %d 次。", successCount)
	}
	return map[string]any{
		"response": response,
		"records":  records,
		"result":   parsedResults,
	}, nil
}

type aksharePlannedCall struct {
	ToolName  string
	Arguments map[string]any
}

func buildAkshareCallPlan(args map[string]any) []aksharePlannedCall {
	if len(args) == 0 {
		return nil
	}
	raw, ok := args["calls"]
	if !ok {
		return nil
	}
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil
	}
	out := make([]aksharePlannedCall, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(fmt.Sprint(m["tool_name"]))
		if name == "" {
			continue
		}
		callArgs := map[string]any{}
		if mm, ok := m["arguments"].(map[string]any); ok {
			callArgs = cloneAnyMap(mm)
		}
		out = append(out, aksharePlannedCall{
			ToolName:  name,
			Arguments: callArgs,
		})
	}
	return out
}

func akshareEnoughCount(purpose string) int {
	switch strings.TrimSpace(strings.ToLower(purpose)) {
	case "advice":
		return 2
	default:
		return 1
	}
}

func isUnknownToolErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "unknown tool")
}

func isAkshareValidationErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "validation error") ||
		strings.Contains(msg, "missing required argument") ||
		strings.Contains(msg, "unexpected keyword argument") ||
		strings.Contains(msg, "missing_argument") ||
		strings.Contains(msg, "unexpected_keyword_argument")
}

func buildAkshareArgumentsForTool(toolName string, base map[string]any, purpose string) map[string]any {
	out := cloneAnyMap(base)
	if out == nil {
		out = map[string]any{}
	}
	query := strings.TrimSpace(fmt.Sprint(out["query"]))
	if query == "" {
		query = purpose
	}

	switch strings.TrimSpace(strings.ToLower(toolName)) {
	case "get_news_data":
		symbol := strings.TrimSpace(fmt.Sprint(out["symbol"]))
		if symbol == "" {
			symbol = normalizeSymbol(extractSymbolFromQuery(query))
		}
		if symbol == "" {
			symbol = "sh000001"
		}
		return map[string]any{
			"symbol": symbol,
		}
	case "get_realtime_data", "get_hist_data":
		symbol := strings.TrimSpace(fmt.Sprint(out["symbol"]))
		if symbol == "" {
			symbol = normalizeSymbol(extractSymbolFromQuery(query))
		}
		if symbol == "" {
			symbol = "sh000001"
		}
		return map[string]any{
			"symbol": symbol,
		}
	case "get_time_info":
		return map[string]any{}
	default:
		delete(out, "query")
		return out
	}
}

func extractSymbolFromQuery(query string) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return ""
	}
	reDigits := regexp.MustCompile(`\b\d{6}\b`)
	if m := reDigits.FindString(query); m != "" {
		return m
	}
	reTicker := regexp.MustCompile(`\b[A-Z]{2,6}\b`)
	if m := reTicker.FindString(strings.ToUpper(query)); m != "" {
		return m
	}
	return ""
}

func normalizeSymbol(symbol string) string {
	symbol = strings.TrimSpace(strings.ToLower(symbol))
	if symbol == "" {
		return ""
	}
	reDigits := regexp.MustCompile(`^\d{6}$`)
	if reDigits.MatchString(symbol) {
		if strings.HasPrefix(symbol, "6") {
			return "sh" + symbol
		}
		return "sz" + symbol
	}
	return symbol
}

func discoverAkshareToolCatalogAndSchema() (string, string) {
	catalogFallback := "Available AkShare MCP tools: (discovery unavailable)"
	schemaFallback := "AkShare tool parameter schema: (discovery unavailable)"
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	command, args, err := tools.EnsureUvToolInstalled(ctx, "uvx", []string{"akshare-one-mcp"})
	if err != nil {
		return catalogFallback, schemaFallback
	}
	client, err := tools.ConnectMCPStdio(ctx, command, args)
	if err != nil {
		return catalogFallback, schemaFallback
	}
	defer func() { _ = client.Close() }()

	infos, err := client.ListTools(ctx)
	if err != nil || len(infos) == 0 {
		return catalogFallback, schemaFallback
	}
	names := make([]string, 0, len(infos))
	schemaLines := make([]string, 0, len(infos))
	for _, info := range infos {
		name := strings.TrimSpace(info.Name)
		if name != "" {
			names = append(names, name)
		}
		schemaLines = append(schemaLines, formatAkshareToolSchemaLine(info))
	}
	if len(names) == 0 {
		return catalogFallback, schemaFallback
	}
	if len(schemaLines) == 0 {
		schemaLines = append(schemaLines, "(schema unavailable)")
	}
	return "Available AkShare MCP tools: " + strings.Join(uniqueStrings(names), ", "),
		"AkShare tool parameter schema:\n" + strings.Join(schemaLines, "\n")
}

func formatAkshareToolSchemaLine(info tools.RemoteToolInfo) string {
	name := strings.TrimSpace(info.Name)
	if name == "" {
		name = "(unknown_tool)"
	}
	if len(info.InputSchema) == 0 {
		return "- " + name + " params: (schema unavailable)"
	}
	requiredSet := map[string]bool{}
	if rawReq, ok := info.InputSchema["required"]; ok {
		switch arr := rawReq.(type) {
		case []any:
			for _, item := range arr {
				val := strings.TrimSpace(fmt.Sprint(item))
				if val != "" {
					requiredSet[val] = true
				}
			}
		case []string:
			for _, item := range arr {
				val := strings.TrimSpace(item)
				if val != "" {
					requiredSet[val] = true
				}
			}
		}
	}
	required := make([]string, 0, len(requiredSet))
	optional := make([]string, 0, 8)
	paramLines := make([]string, 0, 8)
	if props, ok := info.InputSchema["properties"].(map[string]any); ok {
		for k, raw := range props {
			key := strings.TrimSpace(k)
			if key == "" {
				continue
			}
			if requiredSet[key] {
				required = append(required, key)
			} else {
				optional = append(optional, key)
			}
			prop, _ := raw.(map[string]any)
			paramLines = append(paramLines, formatAkshareParamLine(key, requiredSet[key], prop))
		}
	}
	required = uniqueStrings(required)
	optional = uniqueStrings(optional)
	paramLines = uniqueStrings(paramLines)
	if len(paramLines) == 0 {
		return fmt.Sprintf("- %s required=[%s] optional=[%s]", name, strings.Join(required, ", "), strings.Join(optional, ", "))
	}
	return fmt.Sprintf("- %s required=[%s] optional=[%s] params={%s}", name, strings.Join(required, ", "), strings.Join(optional, ", "), strings.Join(paramLines, " ; "))
}

func formatAkshareParamLine(name string, required bool, prop map[string]any) string {
	typeName := strings.TrimSpace(fmt.Sprint(prop["type"]))
	if typeName == "" || typeName == "<nil>" {
		typeName = "any"
	}
	desc := sanitizeSingleLine(strings.TrimSpace(fmt.Sprint(prop["description"])))
	enumVals := formatEnumValues(prop["enum"])
	req := "optional"
	if required {
		req = "required"
	}
	if desc == "" && enumVals == "" {
		return fmt.Sprintf("%s(%s,%s)", name, typeName, req)
	}
	if enumVals == "" {
		return fmt.Sprintf("%s(%s,%s,%s)", name, typeName, req, desc)
	}
	if desc == "" {
		return fmt.Sprintf("%s(%s,%s,enum=%s)", name, typeName, req, enumVals)
	}
	return fmt.Sprintf("%s(%s,%s,%s,enum=%s)", name, typeName, req, desc, enumVals)
}

func sanitizeSingleLine(text string) string {
	if text == "" || text == "<nil>" {
		return ""
	}
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "\r", " ")
	return strings.TrimSpace(text)
}

func formatEnumValues(raw any) string {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return ""
	}
	vals := make([]string, 0, len(items))
	for _, item := range items {
		v := strings.TrimSpace(fmt.Sprint(item))
		if v != "" && v != "<nil>" {
			vals = append(vals, v)
		}
	}
	vals = uniqueStrings(vals)
	if len(vals) == 0 {
		return ""
	}
	return "[" + strings.Join(vals, ",") + "]"
}

func parseAkshareToolNames(catalog string) []string {
	catalog = strings.TrimSpace(catalog)
	if catalog == "" {
		return nil
	}
	const prefix = "Available AkShare MCP tools:"
	if strings.HasPrefix(catalog, prefix) {
		catalog = strings.TrimSpace(strings.TrimPrefix(catalog, prefix))
	}
	if strings.Contains(catalog, "unavailable") {
		return nil
	}
	parts := strings.Split(catalog, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name != "" {
			out = append(out, name)
		}
	}
	return uniqueStrings(out)
}

func (a *Agent) akshareToolCandidates(primary string, purpose string) []string {
	out := make([]string, 0, 6)
	purpose = strings.TrimSpace(strings.ToLower(purpose))
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if purpose == "news" && !isNewsPreferredAkshareTool(name) {
			return
		}
		for _, existing := range out {
			if strings.EqualFold(existing, name) {
				return
			}
		}
		out = append(out, name)
	}
	add(strings.TrimSpace(primary))
	fallbackName, _ := fallbackAkshareRequestV2(purpose)
	add(fallbackName)
	for _, name := range parseAkshareToolNames(a.akshareToolCatalog) {
		add(name)
	}
	return out
}

func isNewsPreferredAkshareTool(name string) bool {
	switch strings.TrimSpace(strings.ToLower(name)) {
	case "get_news_data", "get_realtime_data", "get_hist_data", "get_time_info":
		return true
	default:
		return false
	}
}

func normalizeAkshareToolName(raw string, purpose string) string {
	name := strings.TrimSpace(raw)
	if name != "" {
		return name
	}
	fallbackName, _ := fallbackAkshareRequestV2(purpose)
	return fallbackName
}

func fallbackAkshareRequestV2(purpose string) (string, map[string]any) {
	switch strings.TrimSpace(purpose) {
	case "news":
		return "get_news_data", map[string]any{"symbol": "sh000001"}
	default:
		return "get_realtime_data", map[string]any{"symbol": "sh000001"}
	}
}

func (a *Agent) findToolByName(name string) (tools.Tool, error) {
	switch strings.TrimSpace(name) {
	case "mysql_exec":
		if a.MySQLTool == nil {
			return nil, fmt.Errorf("tool mysql_exec is not initialized")
		}
		return a.MySQLTool, nil
	case "akshare-one-mcp":
		if a.AkshareTool == nil {
			return nil, fmt.Errorf("tool akshare-one-mcp is not initialized")
		}
		return a.AkshareTool, nil
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

func buildPlanPrompt(userQuery string, userID string, akshareCatalog string, akshareSchema string, tableMetaHint string) string {
	if strings.TrimSpace(userID) == "" {
		userID = financeGuestUserID
	}
	var sb strings.Builder
	sb.WriteString("You are financehelper planner.\n")
	sb.WriteString("Classify the user's finance request into one action: ledger, report, news, advice.\n")
	sb.WriteString("All SQL MUST be valid MySQL 8 syntax. Never use SQLite syntax.\n")
	sb.WriteString("Return JSON only. Do not output markdown.\n")
	sb.WriteString("Schema:\n")
	sb.WriteString("{\"action\":\"ledger|report|news|advice\",\"summary\":\"...\",\"ensure_table_sql\":\"...\",\"sql_statements\":[\"...\"],\"akshare_tool_name\":\"...\",\"akshare_arguments\":{\"calls\":[{\"tool_name\":\"...\",\"arguments\":{}}]}}\n")
	sb.WriteString("Rules:\n")
	sb.WriteString("1. If action is ledger/report/advice and database access is needed, write SQL using table placeholder ")
	sb.WriteString(financeTablePlaceholder)
	sb.WriteString(" instead of a real table name.\n")
	sb.WriteString("2. For ledger, include CREATE TABLE IF NOT EXISTS in ensure_table_sql. The table structure should be designed by you, suitable for bookkeeping. Include the actual INSERT/UPDATE/DELETE/SELECT statements in sql_statements.\n")
	sb.WriteString("3. For report, include ensure_table_sql and one or more SELECT statements in sql_statements for the requested time range.\n")
	sb.WriteString("4. For advice, include ensure_table_sql and one or more SELECT statements in sql_statements to read the current financial state, and also provide akshare_tool_name plus akshare_arguments.\n")
	sb.WriteString("5. For news, leave SQL fields empty and provide akshare_tool_name plus akshare_arguments.\n")
	sb.WriteString("6. Choose akshare_tool_name from runtime tool catalog only.\n")
	sb.WriteString("7. For akshare_arguments, follow runtime parameter schema and include required arguments.\n")
	sb.WriteString("8. For news/advice, prefer providing akshare_arguments.calls as an ordered multi-call plan; each item is {tool_name, arguments}.\n")
	sb.WriteString("9. calls can contain fallback tools; execution should continue when one call fails.\n")
	sb.WriteString("10. For ledger/report/advice SQL, table/column names MUST strictly follow the user's table metadata (column_meta). Do not invent columns.\n")
	sb.WriteString("11. Prefer concise SQL. Escape single quotes if needed.\n")
	sb.WriteString("12. Current user id: ")
	sb.WriteString(userID)
	sb.WriteString("\n")
	sb.WriteString("User request:\n")
	sb.WriteString(userQuery)
	sb.WriteString("\nRuntime AkShare tool catalog:\n")
	sb.WriteString(strings.TrimSpace(akshareCatalog))
	sb.WriteString("\nRuntime AkShare parameter schema:\n")
	sb.WriteString(strings.TrimSpace(akshareSchema))
	sb.WriteString("\nUser billing table metadata (STRICT):\n")
	sb.WriteString(strings.TrimSpace(tableMetaHint))
	return sb.String()
}

func buildResponsePrompt(userQuery string, payload map[string]any) string {
	plan := planFromPayload(payload)
	mysqlInfo := stringifyPayloadBlock(payload["N_mysql_ledger"])
	if mysqlInfo == "" {
		mysqlInfo = stringifyPayloadBlock(payload["N_mysql_report"])
	}
	if mysqlInfo == "" {
		mysqlInfo = stringifyPayloadBlock(payload["N_mysql_advice"])
	}
	akshareInfo := stringifyPayloadBlock(payload["N_akshare_news"])
	if akshareInfo == "" {
		akshareInfo = stringifyPayloadBlock(payload["N_akshare_advice"])
	}

	var sb strings.Builder
	sb.WriteString("You are financehelper responder.\n")
	sb.WriteString("Answer in Simplified Chinese with a clean structured result.\n")
	sb.WriteString("If action=ledger: confirm what was recorded and mention if the user can continue adding bills.\n")
	sb.WriteString("If action=report: provide overview, income/expense structure, key findings, and suggestions.\n")
	sb.WriteString("If action=news: provide a finance news digest with bullet-like paragraphs and a brief takeaway.\n")
	sb.WriteString("If action=advice: combine the user's current finances and market information, then give practical risk-aware suggestions.\n")
	sb.WriteString("Keep it concise but complete.\n")
	sb.WriteString("User request:\n")
	sb.WriteString(userQuery)
	sb.WriteString("\n\nPlanned action:\n")
	sb.WriteString(plan.Action)
	sb.WriteString("\n\nPlan summary:\n")
	sb.WriteString(plan.Summary)
	sb.WriteString("\n\nMySQL result:\n")
	sb.WriteString(truncateText(mysqlInfo, 3000))
	sb.WriteString("\n\nAkShare result:\n")
	sb.WriteString(truncateText(akshareInfo, 3000))
	return sb.String()
}

func (a *Agent) generateLedgerSQLBySchema(ctx context.Context, userQuery string, rec financeSchemaRecord) ([]string, error) {
	if strings.TrimSpace(rec.TableName) == "" {
		return nil, fmt.Errorf("empty ledger table name")
	}
	schemaJSON, _ := json.Marshal(rec)
	var sb strings.Builder
	sb.WriteString("You are a MySQL SQL generator for bookkeeping.\n")
	sb.WriteString("Return JSON only. No markdown.\n")
	sb.WriteString("Output schema: {\"sql_statements\":[\"...\"]}\n")
	sb.WriteString("Rules:\n")
	sb.WriteString("1. Use ONLY MySQL 8 SQL.\n")
	sb.WriteString("2. Use table name exactly: ")
	sb.WriteString(rec.TableName)
	sb.WriteString("\n")
	sb.WriteString("3. Use ONLY columns from the provided schema metadata.\n")
	sb.WriteString("4. For bookkeeping request, prefer INSERT statement.\n")
	sb.WriteString("5. Never use placeholder table names.\n")
	sb.WriteString("User request:\n")
	sb.WriteString(userQuery)
	sb.WriteString("\nSchema metadata JSON:\n")
	sb.WriteString(string(schemaJSON))

	resp, err := llm.NewClient(strings.TrimSpace(a.llmClient.BaseURL), strings.TrimSpace(a.llmClient.APIKey)).ChatCompletion(
		ctx,
		strings.TrimSpace(a.chatModel),
		[]llm.Message{{Role: "user", Content: sb.String()}},
		nil,
		nil,
	)
	if err != nil {
		return nil, err
	}
	return parseSQLStatements(resp, rec.TableName), nil
}

func parseSQLStatements(raw string, tableName string) []string {
	raw = stripMarkdownCodeFence(strings.TrimSpace(raw))
	if raw == "" {
		return nil
	}

	type sqlEnvelope struct {
		SQLStatements []string `json:"sql_statements"`
	}
	var env sqlEnvelope
	if json.Unmarshal([]byte(raw), &env) == nil && len(env.SQLStatements) > 0 {
		return finalizeSQLStatements(env.SQLStatements, tableName)
	}

	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start >= 0 && end > start {
		obj := raw[start : end+1]
		if json.Unmarshal([]byte(obj), &env) == nil && len(env.SQLStatements) > 0 {
			return finalizeSQLStatements(env.SQLStatements, tableName)
		}
	}

	lines := strings.Split(raw, ";")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		s := strings.TrimSpace(line)
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	return finalizeSQLStatements(out, tableName)
}

func finalizeSQLStatements(in []string, tableName string) []string {
	out := make([]string, 0, len(in))
	for _, sqlText := range in {
		sqlText = replaceTablePlaceholder(sqlText, tableName)
		sqlText = normalizeSQLForMySQL(sqlText)
		sqlText = strings.TrimSpace(sqlText)
		if sqlText == "" {
			continue
		}
		out = append(out, sqlText)
	}
	return out
}

func decodeFinancePlan(raw string) (financePlan, error) {
	raw = stripMarkdownCodeFence(raw)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return financePlan{}, fmt.Errorf("empty plan")
	}
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start >= 0 && end >= start {
		raw = raw[start : end+1]
	}

	var plan financePlan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		return financePlan{}, err
	}
	return plan, nil
}

func finalizePlan(plan financePlan, userID string) financePlan {
	userID = normalizeUserID(userID)
	plan.Action = normalizeAction(plan.Action)
	if plan.Action == "" {
		plan.Action = "advice"
	}
	tableName := financeTableName(userID)
	plan.TableName = tableName
	plan.EnsureTableSQL = replaceTablePlaceholder(plan.EnsureTableSQL, tableName)
	plan.EnsureTableSQL = normalizeSQLForMySQL(plan.EnsureTableSQL)
	if strings.TrimSpace(plan.EnsureTableSQL) == "" && plan.Action != "news" {
		plan.EnsureTableSQL = defaultEnsureTableSQL(tableName)
	}
	if plan.Action != "news" && looksLikeNonMySQLDDL(plan.EnsureTableSQL) {
		plan.EnsureTableSQL = defaultEnsureTableSQL(tableName)
	}

	outSQL := make([]string, 0, len(plan.SQLStatements))
	for _, item := range plan.SQLStatements {
		item = replaceTablePlaceholder(item, tableName)
		item = normalizeSQLForMySQL(item)
		item = strings.TrimSpace(item)
		if item != "" {
			outSQL = append(outSQL, item)
		}
	}
	plan.SQLStatements = outSQL

	if strings.TrimSpace(plan.Summary) == "" {
		switch plan.Action {
		case "ledger":
			plan.Summary = "记录并维护用户账单"
		case "report":
			plan.Summary = "查询时间范围账单并生成报告"
		case "news":
			plan.Summary = "读取金融资讯并整理重点"
		default:
			plan.Summary = "结合账单与市场信息生成理财建议"
		}
	}
	if len(plan.AkshareArgs) == 0 {
		plan.AkshareArgs = map[string]any{}
	}
	return plan
}

func planToOutput(plan financePlan) map[string]any {
	return map[string]any{
		"action":            plan.Action,
		"summary":           plan.Summary,
		"table_name":        plan.TableName,
		"ensure_table_sql":  plan.EnsureTableSQL,
		"sql_statements":    plan.SQLStatements,
		"akshare_tool_name": plan.AkshareToolName,
		"akshare_arguments": plan.AkshareArgs,
		// Condition nodes now read latest_output.response, so expose action for routing.
		"response": plan.Action,
	}
}

func planFromPayload(payload map[string]any) financePlan {
	out := financePlan{}
	node, _ := payload["N_plan"].(map[string]any)
	out.Action = strings.TrimSpace(fmt.Sprint(node["action"]))
	out.Summary = strings.TrimSpace(fmt.Sprint(node["summary"]))
	out.TableName = strings.TrimSpace(fmt.Sprint(node["table_name"]))
	out.EnsureTableSQL = strings.TrimSpace(fmt.Sprint(node["ensure_table_sql"]))
	out.AkshareToolName = strings.TrimSpace(fmt.Sprint(node["akshare_tool_name"]))
	switch arr := node["sql_statements"].(type) {
	case []any:
		out.SQLStatements = make([]string, 0, len(arr))
		for _, item := range arr {
			text := strings.TrimSpace(fmt.Sprint(item))
			if text != "" {
				out.SQLStatements = append(out.SQLStatements, text)
			}
		}
	case []string:
		out.SQLStatements = make([]string, 0, len(arr))
		for _, item := range arr {
			item = strings.TrimSpace(item)
			if item != "" {
				out.SQLStatements = append(out.SQLStatements, item)
			}
		}
	}
	if m, ok := node["akshare_arguments"].(map[string]any); ok {
		out.AkshareArgs = cloneAnyMap(m)
	}
	return out
}

func buildFallbackPlan(query string) financePlan {
	lower := strings.ToLower(strings.TrimSpace(query))
	if isLikelyReportQuery(lower) {
		return financePlan{
			Action:         "report",
			Summary:        "查询近期账单并生成财务报告",
			EnsureTableSQL: defaultEnsureTableSQL(financeTablePlaceholder),
			SQLStatements: []string{
				fmt.Sprintf("SELECT * FROM %s WHERE bill_date >= DATE_SUB(NOW(), INTERVAL 1 MONTH) ORDER BY bill_date DESC, id DESC LIMIT 200", financeTablePlaceholder),
			},
		}
	}
	switch {
	case containsAny(lower, []string{"资讯", "新闻", "快讯", "行情"}):
		toolName, args := fallbackAkshareRequestV2("news")
		return financePlan{Action: "news", Summary: "整理财经资讯", AkshareToolName: toolName, AkshareArgs: args}
	case containsAny(lower, []string{"报告", "报表", "汇总", "统计"}):
		return financePlan{
			Action:         "report",
			Summary:        "生成财务报告",
			EnsureTableSQL: defaultEnsureTableSQL(financeTablePlaceholder),
			SQLStatements: []string{
				fmt.Sprintf("SELECT bill_type, category, SUM(amount) AS total_amount, COUNT(*) AS record_count FROM %s GROUP BY bill_type, category ORDER BY total_amount DESC", financeTablePlaceholder),
			},
		}
	case containsAny(lower, []string{"建议", "理财", "配置", "投资"}):
		toolName, args := fallbackAkshareRequestV2("advice")
		return financePlan{
			Action:          "advice",
			Summary:         "查询账单并给出理财建议",
			EnsureTableSQL:  defaultEnsureTableSQL(financeTablePlaceholder),
			SQLStatements:   []string{fmt.Sprintf("SELECT * FROM %s ORDER BY bill_date DESC, id DESC LIMIT 30", financeTablePlaceholder)},
			AkshareToolName: toolName,
			AkshareArgs:     args,
		}
	default:
		return financePlan{
			Action:         "ledger",
			Summary:        "记录用户账单",
			EnsureTableSQL: defaultEnsureTableSQL(financeTablePlaceholder),
			SQLStatements: []string{
				fmt.Sprintf("INSERT INTO %s (bill_date, amount, category, bill_type, note, raw_text) VALUES (CURDATE(), 0, '未分类', 'expense', '%s', '%s')", financeTablePlaceholder, escapeSQLString(query), escapeSQLString(query)),
			},
		}
	}
}

func isLikelyReportQuery(lower string) bool {
	if lower == "" {
		return false
	}
	querySignals := []string{"查询", "查", "看看", "统计", "报表", "报告", "汇总", "明细", "账单", "收支", "最近", "近", "本月", "上月", "月"}
	writeSignals := []string{"记一笔", "记账", "写入", "新增", "添加", "入账", "录入", "报销", "花了", "消费了", "支出", "收入", "转账"}
	hasQuery := containsAny(lower, querySignals)
	hasWrite := containsAny(lower, writeSignals)
	return hasQuery && !hasWrite
}

func (a *Agent) fallbackResponse(query string, payload map[string]any) string {
	plan := planFromPayload(payload)
	switch plan.Action {
	case "ledger":
		return "已为你处理账单记录请求。若需要，我还可以继续帮你补充分类、时间或金额信息。"
	case "report":
		return "已读取指定范围内的账单数据，并整理出财务报告。你可以继续指定更精确的时间范围或分类。"
	case "news":
		return "已整理财经资讯重点。若你想聚焦某个市场、板块或标的，可以继续告诉我。"
	default:
		return "已结合你的账单情况与市场信息整理出理财建议。建议你继续补充风险偏好和投资期限，以便我给出更细的方案。"
	}
}

func buildFinanceHelperWorkflow() (*orchestrator.Workflow, error) {
	wf, err := orchestrator.NewWorkflow(FinanceHelperWorkflowID, "financehelper finance orchestration workflow")
	if err != nil {
		return nil, err
	}
	nodes := []orchestrator.Node{
		{ID: "N_start", Type: orchestrator.NodeTypeStart},
		{ID: "N_plan", Type: orchestrator.NodeTypeChatModel, AgentID: FinanceHelperWorkflowWorkerID, TaskType: "chat_model", Config: map[string]any{"intent": "plan_request"}, PreInput: "规划财务请求处理步骤"},
		{ID: "N_is_ledger", Type: orchestrator.NodeTypeCondition, Config: map[string]any{"left_type": "path", "left_value": "response", "operator": "eq", "right_type": "value", "right_value": "ledger"}, Metadata: map[string]string{"true_to": "N_mysql_ledger", "false_to": "N_route_report"}},
		{ID: "N_mysql_ledger", Type: orchestrator.NodeTypeTool, AgentID: FinanceHelperWorkflowWorkerID, TaskType: "tool", Config: map[string]any{"tool_name": "mysql_exec", "purpose": "ledger"}},
		{ID: "N_route_report", Type: orchestrator.NodeTypeChatModel, AgentID: FinanceHelperWorkflowWorkerID, TaskType: "chat_model", Config: map[string]any{"intent": "route_action"}, PreInput: "内部路由：同步当前 action 用于报告分支判断"},
		{ID: "N_is_report", Type: orchestrator.NodeTypeCondition, Config: map[string]any{"left_type": "path", "left_value": "response", "operator": "eq", "right_type": "value", "right_value": "report"}, Metadata: map[string]string{"true_to": "N_mysql_report", "false_to": "N_route_news"}},
		{ID: "N_mysql_report", Type: orchestrator.NodeTypeTool, AgentID: FinanceHelperWorkflowWorkerID, TaskType: "tool", Config: map[string]any{"tool_name": "mysql_exec", "purpose": "report"}},
		{ID: "N_route_news", Type: orchestrator.NodeTypeChatModel, AgentID: FinanceHelperWorkflowWorkerID, TaskType: "chat_model", Config: map[string]any{"intent": "route_action"}, PreInput: "内部路由：同步当前 action 用于资讯分支判断"},
		{ID: "N_is_news", Type: orchestrator.NodeTypeCondition, Config: map[string]any{"left_type": "path", "left_value": "response", "operator": "eq", "right_type": "value", "right_value": "news"}, Metadata: map[string]string{"true_to": "N_akshare_news", "false_to": "N_mysql_advice"}},
		{ID: "N_akshare_news", Type: orchestrator.NodeTypeTool, AgentID: FinanceHelperWorkflowWorkerID, TaskType: "tool", Config: map[string]any{"tool_name": "akshare-one-mcp", "purpose": "news"}},
		{ID: "N_mysql_advice", Type: orchestrator.NodeTypeTool, AgentID: FinanceHelperWorkflowWorkerID, TaskType: "tool", Config: map[string]any{"tool_name": "mysql_exec", "purpose": "advice"}},
		{ID: "N_akshare_advice", Type: orchestrator.NodeTypeTool, AgentID: FinanceHelperWorkflowWorkerID, TaskType: "tool", Config: map[string]any{"tool_name": "akshare-one-mcp", "purpose": "advice"}},
		{ID: "N_respond", Type: orchestrator.NodeTypeChatModel, AgentID: FinanceHelperWorkflowWorkerID, TaskType: "chat_model", Config: map[string]any{"intent": "final_response"}, PreInput: "整理财务执行结果并回复用户"},
		{ID: "N_end", Type: orchestrator.NodeTypeEnd},
	}
	for _, node := range nodes {
		if err = wf.AddNode(node); err != nil {
			return nil, err
		}
	}

	edges := []struct {
		from  string
		to    string
		label string
	}{
		{"N_start", "N_plan", ""},
		{"N_plan", "N_is_ledger", ""},
		{"N_is_ledger", "N_mysql_ledger", "true"},
		{"N_is_ledger", "N_route_report", "false"},
		{"N_route_report", "N_is_report", ""},
		{"N_mysql_ledger", "N_respond", ""},
		{"N_is_report", "N_mysql_report", "true"},
		{"N_is_report", "N_route_news", "false"},
		{"N_route_news", "N_is_news", ""},
		{"N_mysql_report", "N_respond", ""},
		{"N_is_news", "N_akshare_news", "true"},
		{"N_is_news", "N_mysql_advice", "false"},
		{"N_akshare_news", "N_respond", ""},
		{"N_mysql_advice", "N_akshare_advice", ""},
		{"N_akshare_advice", "N_respond", ""},
		{"N_respond", "N_end", ""},
	}
	for _, edge := range edges {
		if edge.label == "" {
			if err = wf.AddEdge(edge.from, edge.to); err != nil {
				return nil, err
			}
			continue
		}
		if err = wf.AddEdgeWithLabel(edge.from, edge.to, edge.label, nil); err != nil {
			return nil, err
		}
	}
	return wf, nil
}

func normalizeAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "ledger", "bookkeeping", "record":
		return "ledger"
	case "report", "statement":
		return "report"
	case "news", "market_news":
		return "news"
	case "advice", "suggestion", "portfolio":
		return "advice"
	default:
		return ""
	}
}

func financeTableName(userID string) string {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		userID = financeGuestUserID
	}
	re := regexp.MustCompile(`[^a-zA-Z0-9]+`)
	cleaned := strings.ToLower(re.ReplaceAllString(userID, "_"))
	cleaned = strings.Trim(cleaned, "_")
	if cleaned == "" {
		cleaned = financeGuestUserID
	}
	if len(cleaned) > 40 {
		cleaned = cleaned[:40]
	}
	return "finance_bill_" + cleaned
}

func normalizeUserID(raw any) string {
	if raw == nil {
		return financeGuestUserID
	}
	userID := strings.TrimSpace(fmt.Sprint(raw))
	if userID == "" || userID == "<nil>" {
		return financeGuestUserID
	}
	return userID
}

func extractFinanceUserQuery(payload map[string]any, fallback string) string {
	if payload != nil {
		for _, key := range []string{"input", "text", "query"} {
			raw := strings.TrimSpace(fmt.Sprint(payload[key]))
			if raw == "" || raw == "<nil>" {
				continue
			}
			if q := extractFinanceCurrentQuestion(raw); q != "" {
				return q
			}
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
				if q := extractFinanceCurrentQuestion(raw); q != "" {
					return q
				}
			}
		}
	}
	return extractFinanceCurrentQuestion(strings.TrimSpace(fallback))
}

func extractFinanceCurrentQuestion(in string) string {
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
	if strings.Contains(s, "map[") {
		return ""
	}
	return s
}

func replaceTablePlaceholder(sqlText string, tableName string) string {
	sqlText = strings.TrimSpace(sqlText)
	if sqlText == "" {
		return ""
	}
	return strings.ReplaceAll(sqlText, financeTablePlaceholder, tableName)
}

func extractTableNameFromSQL(sqlText string) string {
	s := strings.TrimSpace(sqlText)
	if s == "" {
		return ""
	}
	re := regexp.MustCompile(`(?i)create\s+table\s+if\s+not\s+exists\s+([a-zA-Z0-9_]+)`)
	m := re.FindStringSubmatch(s)
	if len(m) >= 2 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func ensureSchemaRegistryTableSQL() string {
	return "CREATE TABLE IF NOT EXISTS finance_schema_registry (" +
		"user_id VARCHAR(128) PRIMARY KEY," +
		"table_name VARCHAR(128) NOT NULL," +
		"schema_json LONGTEXT NULL," +
		"created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP," +
		"updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP" +
		")"
}

func (a *Agent) loadOrBuildSchemaRecord(ctx context.Context, tool tools.Tool, userID string, tableName string, ensureSQL string) (financeSchemaRecord, error) {
	rec, err := loadSchemaRecord(ctx, tool, userID)
	if err == nil && strings.TrimSpace(rec.TableName) != "" {
		if len(rec.SemanticToCol) == 0 && len(rec.Columns) > 0 {
			rec.SemanticToCol = inferSemanticMapping(rec.Columns)
		}
		if len(rec.ColumnTypes) == 0 {
			if colTypes, typeErr := fetchTableColumnTypes(ctx, tool, rec.TableName); typeErr == nil {
				rec.ColumnTypes = colTypes
			}
		}
		a.enrichSchemaRecordSemantics(ctx, &rec)
		return rec, nil
	}

	cols, colErr := fetchTableColumns(ctx, tool, tableName)
	if colErr != nil || len(cols) == 0 {
		fallback := inferColumnsFromCreateSQL(ensureSQL)
		cols = uniqueStrings(fallback)
	}
	rec = financeSchemaRecord{
		UserID:        userID,
		TableName:     tableName,
		Columns:       uniqueStrings(cols),
		SemanticToCol: inferSemanticMapping(cols),
	}
	if colTypes, typeErr := fetchTableColumnTypes(ctx, tool, tableName); typeErr == nil {
		rec.ColumnTypes = colTypes
	}
	a.enrichSchemaRecordSemantics(ctx, &rec)
	return rec, nil
}

func (a *Agent) refreshAndSaveSchemaRecord(ctx context.Context, tool tools.Tool, userID string, tableName string, rec *financeSchemaRecord) error {
	cols, err := fetchTableColumns(ctx, tool, tableName)
	if err != nil {
		return err
	}
	if rec == nil {
		rec = &financeSchemaRecord{}
	}
	rec.UserID = userID
	rec.TableName = tableName
	rec.Columns = uniqueStrings(cols)
	rec.SemanticToCol = inferSemanticMapping(rec.Columns)
	if colTypes, typeErr := fetchTableColumnTypes(ctx, tool, tableName); typeErr == nil {
		rec.ColumnTypes = colTypes
	}
	a.enrichSchemaRecordSemantics(ctx, rec)
	return saveSchemaRecord(ctx, tool, *rec)
}

func loadSchemaRecord(ctx context.Context, tool tools.Tool, userID string) (financeSchemaRecord, error) {
	sqlText := fmt.Sprintf("SELECT table_name, schema_json FROM finance_schema_registry WHERE user_id='%s' LIMIT 1", escapeSQLString(userID))
	out, err := tool.Execute(ctx, map[string]any{"sql": sqlText})
	if err != nil {
		return financeSchemaRecord{}, err
	}
	parsed := parseJSONLikeText(extractToolText(out))
	m, ok := parsed.(map[string]any)
	if !ok {
		return financeSchemaRecord{}, fmt.Errorf("invalid schema row payload")
	}
	rows, _ := m["rows"].([]any)
	if len(rows) == 0 {
		return financeSchemaRecord{}, fmt.Errorf("schema row not found")
	}
	row, _ := rows[0].(map[string]any)
	rec := financeSchemaRecord{
		UserID:    userID,
		TableName: strings.TrimSpace(fmt.Sprint(row["table_name"])),
	}
	rawSchema := strings.TrimSpace(fmt.Sprint(row["schema_json"]))
	if rawSchema != "" && rawSchema != "<nil>" {
		_ = json.Unmarshal([]byte(rawSchema), &rec)
		if rec.UserID == "" {
			rec.UserID = userID
		}
	}
	if len(rec.Columns) == 0 && len(rec.ColumnMeta) > 0 {
		rec.Columns = make([]string, 0, len(rec.ColumnMeta))
		rec.ColumnTypes = map[string]string{}
		for _, m := range rec.ColumnMeta {
			name := strings.TrimSpace(m.Name)
			if name == "" {
				continue
			}
			rec.Columns = append(rec.Columns, name)
			if rec.ColumnTypes == nil {
				rec.ColumnTypes = map[string]string{}
			}
			rec.ColumnTypes[strings.ToLower(name)] = strings.TrimSpace(m.Type)
		}
		rec.Columns = uniqueStrings(rec.Columns)
	}
	return rec, nil
}

func saveSchemaRecord(ctx context.Context, tool tools.Tool, rec financeSchemaRecord) error {
	if strings.TrimSpace(rec.UserID) == "" || strings.TrimSpace(rec.TableName) == "" {
		return fmt.Errorf("invalid schema record")
	}
	if len(rec.ColumnMeta) == 0 {
		rec.ColumnMeta = buildColumnMeta(rec.Columns, rec.ColumnTypes, rec.SemanticToCol, rec.SemanticDesc)
	}
	stored := map[string]any{
		"user_id":     rec.UserID,
		"table_name":  rec.TableName,
		"column_meta": rec.ColumnMeta,
	}
	data, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	sqlText := fmt.Sprintf("INSERT INTO finance_schema_registry (user_id, table_name, schema_json) VALUES ('%s','%s','%s') ON DUPLICATE KEY UPDATE table_name=VALUES(table_name), schema_json=VALUES(schema_json), updated_at=CURRENT_TIMESTAMP",
		escapeSQLString(rec.UserID), escapeSQLString(rec.TableName), escapeSQLString(string(data)))
	_, err = tool.Execute(ctx, map[string]any{"sql": sqlText})
	return err
}

func fetchTableColumns(ctx context.Context, tool tools.Tool, tableName string) ([]string, error) {
	if strings.TrimSpace(tableName) == "" {
		return nil, fmt.Errorf("table name empty")
	}
	sqlText := fmt.Sprintf("SELECT COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = '%s' ORDER BY ORDINAL_POSITION", escapeSQLString(tableName))
	out, err := tool.Execute(ctx, map[string]any{"sql": sqlText})
	if err != nil {
		return nil, err
	}
	parsed := parseJSONLikeText(extractToolText(out))
	m, ok := parsed.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid column query result")
	}
	rows, _ := m["rows"].([]any)
	cols := make([]string, 0, len(rows))
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		col := strings.TrimSpace(fmt.Sprint(row["COLUMN_NAME"]))
		if col != "" {
			cols = append(cols, col)
		}
	}
	return uniqueStrings(cols), nil
}

func fetchTableColumnTypes(ctx context.Context, tool tools.Tool, tableName string) (map[string]string, error) {
	if strings.TrimSpace(tableName) == "" {
		return nil, fmt.Errorf("table name empty")
	}
	sqlText := fmt.Sprintf("SELECT COLUMN_NAME, COLUMN_TYPE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = '%s' ORDER BY ORDINAL_POSITION", escapeSQLString(tableName))
	out, err := tool.Execute(ctx, map[string]any{"sql": sqlText})
	if err != nil {
		return nil, err
	}
	parsed := parseJSONLikeText(extractToolText(out))
	m, ok := parsed.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid column type query result")
	}
	rows, _ := m["rows"].([]any)
	outMap := map[string]string{}
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		col := strings.ToLower(strings.TrimSpace(fmt.Sprint(row["COLUMN_NAME"])))
		typ := strings.TrimSpace(fmt.Sprint(row["COLUMN_TYPE"]))
		if col != "" {
			outMap[col] = typ
		}
	}
	return outMap, nil
}

func inferColumnsFromCreateSQL(sqlText string) []string {
	s := strings.TrimSpace(sqlText)
	if s == "" {
		return nil
	}
	left := strings.Index(s, "(")
	right := strings.LastIndex(s, ")")
	if left < 0 || right <= left {
		return nil
	}
	body := s[left+1 : right]
	parts := strings.Split(body, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		first := strings.Fields(p)
		if len(first) == 0 {
			continue
		}
		col := strings.Trim(first[0], "` ")
		colLower := strings.ToLower(col)
		if colLower == "primary" || colLower == "key" || colLower == "unique" || colLower == "index" {
			continue
		}
		out = append(out, col)
	}
	return uniqueStrings(out)
}

func inferSemanticMapping(columns []string) map[string]string {
	out := map[string]string{}
	for _, col := range columns {
		lc := strings.ToLower(strings.TrimSpace(col))
		switch {
		case out["amount"] == "" && containsAny(lc, []string{"amount", "money", "price", "cost", "total", "value"}):
			out["amount"] = col
		case out["bill_date"] == "" && containsAny(lc, []string{"bill_date", "date", "time", "datetime", "spend_date", "record_date"}):
			out["bill_date"] = col
		case out["category"] == "" && containsAny(lc, []string{"category", "class", "tag", "type"}):
			out["category"] = col
		case out["bill_type"] == "" && containsAny(lc, []string{"bill_type", "direction", "flow", "io", "income_expense"}):
			out["bill_type"] = col
		case out["note"] == "" && containsAny(lc, []string{"note", "remark", "memo", "desc", "description"}):
			out["note"] = col
		case out["raw_text"] == "" && containsAny(lc, []string{"raw_text", "raw", "content", "text"}):
			out["raw_text"] = col
		case out["created_at"] == "" && containsAny(lc, []string{"created_at", "create_time"}):
			out["created_at"] = col
		case out["updated_at"] == "" && containsAny(lc, []string{"updated_at", "update_time"}):
			out["updated_at"] = col
		}
	}
	return out
}

func (a *Agent) enrichSchemaRecordSemantics(ctx context.Context, rec *financeSchemaRecord) {
	if rec == nil {
		return
	}
	if len(rec.SemanticDesc) == 0 {
		rec.SemanticDesc = a.generateSemanticDescriptionsByLLM(ctx, *rec)
	}
	if len(rec.SemanticDesc) == 0 {
		rec.SemanticDesc = buildSemanticDescriptionsFallback(rec.SemanticToCol)
	}
	if len(rec.ColumnSpecs) == 0 {
		rec.ColumnSpecs = buildColumnSpecs(rec.Columns, rec.ColumnTypes, rec.SemanticToCol, rec.SemanticDesc)
	}
	if len(rec.ColumnMeta) == 0 {
		rec.ColumnMeta = buildColumnMeta(rec.Columns, rec.ColumnTypes, rec.SemanticToCol, rec.SemanticDesc)
	}
}

func (a *Agent) generateSemanticDescriptionsByLLM(ctx context.Context, rec financeSchemaRecord) map[string]string {
	if len(rec.SemanticToCol) == 0 {
		return nil
	}
	req := map[string]any{
		"table_name":      rec.TableName,
		"columns":         rec.Columns,
		"column_types":    rec.ColumnTypes,
		"semantic_to_col": rec.SemanticToCol,
	}
	reqJSON, _ := json.Marshal(req)
	prompt := strings.Join([]string{
		"You generate semantic descriptions for user billing table metadata.",
		"Return JSON only as object: {\"semantic_desc\":{\"semantic_key\":\"含义\"}}",
		"Rules:",
		"1) semantic_desc keys MUST come from semantic_to_col keys.",
		"2) Each value must be concise Chinese meaning.",
		"3) Do not invent keys.",
		"Input:",
		string(reqJSON),
	}, "\n")
	resp, err := llm.NewClient(strings.TrimSpace(a.llmClient.BaseURL), strings.TrimSpace(a.llmClient.APIKey)).
		ChatCompletion(ctx, strings.TrimSpace(a.chatModel), []llm.Message{{Role: "user", Content: prompt}}, nil, nil)
	if err != nil {
		logger.Warnf("[financehelper] semantic_desc llm failed table=%s err=%v", rec.TableName, err)
		return nil
	}
	resp = stripMarkdownCodeFence(strings.TrimSpace(resp))
	if resp == "" {
		return nil
	}
	var obj map[string]any
	if json.Unmarshal([]byte(resp), &obj) != nil {
		return nil
	}
	rawMap, _ := obj["semantic_desc"].(map[string]any)
	if len(rawMap) == 0 {
		return nil
	}
	out := map[string]string{}
	for k := range rec.SemanticToCol {
		v := strings.TrimSpace(fmt.Sprint(rawMap[k]))
		if v != "" && v != "<nil>" {
			out[k] = v
		}
	}
	return out
}

func buildSemanticDescriptionsFallback(semanticToCol map[string]string) map[string]string {
	desc := map[string]string{}
	for semantic, col := range semanticToCol {
		semantic = strings.TrimSpace(semantic)
		col = strings.TrimSpace(col)
		if semantic == "" || col == "" {
			continue
		}
		desc[semantic] = col + "字段语义"
	}
	return desc
}

func buildColumnMeta(columns []string, columnTypes map[string]string, semanticToCol map[string]string, semanticDesc map[string]string) []financeColumnMeta {
	if len(columns) == 0 {
		return nil
	}
	semanticByCol := map[string]string{}
	for semantic, col := range semanticToCol {
		c := strings.ToLower(strings.TrimSpace(col))
		if c != "" {
			semanticByCol[c] = semantic
		}
	}
	out := make([]financeColumnMeta, 0, len(columns))
	for _, col := range columns {
		col = strings.TrimSpace(col)
		if col == "" {
			continue
		}
		lc := strings.ToLower(col)
		semantic := strings.TrimSpace(semanticByCol[lc])
		meaning := strings.TrimSpace(semanticDesc[semantic])
		if meaning == "" {
			meaning = col + "字段"
		}
		typ := strings.TrimSpace(columnTypes[lc])
		if typ == "" {
			typ = "unknown"
		}
		out = append(out, financeColumnMeta{Name: col, Type: typ, Meaning: meaning})
	}
	return out
}

func buildColumnSpecs(columns []string, columnTypes map[string]string, semanticToCol map[string]string, semanticDesc map[string]string) []string {
	if len(columns) == 0 {
		return nil
	}
	semanticByCol := map[string]string{}
	for semantic, col := range semanticToCol {
		c := strings.ToLower(strings.TrimSpace(col))
		if c != "" {
			semanticByCol[c] = semantic
		}
	}
	specs := make([]string, 0, len(columns))
	for _, col := range columns {
		cleanCol := strings.TrimSpace(col)
		if cleanCol == "" {
			continue
		}
		lc := strings.ToLower(cleanCol)
		semantic := strings.TrimSpace(semanticByCol[lc])
		meaning := ""
		if semantic != "" {
			meaning = strings.TrimSpace(semanticDesc[semantic])
		}
		if meaning == "" {
			meaning = cleanCol + "字段"
		}
		colType := strings.TrimSpace(columnTypes[lc])
		if colType == "" {
			colType = "unknown"
		}
		specs = append(specs, fmt.Sprintf("%s:%s:%s", cleanCol, meaning, colType))
	}
	return uniqueStrings(specs)
}

func resolveSemanticColumn(rec financeSchemaRecord, keys ...string) string {
	for _, key := range keys {
		if c := strings.TrimSpace(rec.SemanticToCol[key]); c != "" {
			return c
		}
	}
	return ""
}

func rewriteSQLBySchema(sqlText string, rec financeSchemaRecord) string {
	sqlText = strings.TrimSpace(sqlText)
	if sqlText == "" {
		return ""
	}
	replacements := map[string]string{
		"bill_date":        resolveSemanticColumn(rec, "bill_date", "created_at"),
		"amount":           resolveSemanticColumn(rec, "amount"),
		"category":         resolveSemanticColumn(rec, "category"),
		"bill_type":        resolveSemanticColumn(rec, "bill_type", "category"),
		"type":             resolveSemanticColumn(rec, "bill_type", "category"),
		"transaction_type": resolveSemanticColumn(rec, "bill_type", "category"),
		"note":             resolveSemanticColumn(rec, "note", "raw_text"),
		"raw_text":         resolveSemanticColumn(rec, "raw_text", "note"),
		"created_at":       resolveSemanticColumn(rec, "created_at", "bill_date"),
		"updated_at":       resolveSemanticColumn(rec, "updated_at"),
	}
	for k, v := range replacements {
		if strings.TrimSpace(v) == "" || strings.EqualFold(k, v) {
			continue
		}
		re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(k) + `\b`)
		sqlText = re.ReplaceAllString(sqlText, v)
	}
	return sqlText
}

func buildLedgerInsertSQL(rec financeSchemaRecord, tableName string, userQuery string) string {
	tableName = strings.TrimSpace(tableName)
	if tableName == "" {
		return ""
	}
	amount := extractFirstAmount(userQuery)
	category := inferCategory(userQuery)
	billType := "expense"
	note := userQuery

	cols := make([]string, 0, 6)
	vals := make([]string, 0, 6)
	if col := resolveSemanticColumn(rec, "bill_date", "created_at"); col != "" {
		cols = append(cols, col)
		vals = append(vals, "CURRENT_TIMESTAMP")
	}
	if col := resolveSemanticColumn(rec, "amount"); col != "" {
		cols = append(cols, col)
		vals = append(vals, fmt.Sprintf("%.2f", amount))
	}
	if col := resolveSemanticColumn(rec, "category"); col != "" {
		cols = append(cols, col)
		vals = append(vals, fmt.Sprintf("'%s'", escapeSQLString(category)))
	}
	if col := resolveSemanticColumn(rec, "bill_type"); col != "" {
		cols = append(cols, col)
		vals = append(vals, fmt.Sprintf("'%s'", escapeSQLString(billType)))
	}
	if col := resolveSemanticColumn(rec, "note"); col != "" {
		cols = append(cols, col)
		vals = append(vals, fmt.Sprintf("'%s'", escapeSQLString(note)))
	}
	if col := resolveSemanticColumn(rec, "raw_text"); col != "" {
		cols = append(cols, col)
		vals = append(vals, fmt.Sprintf("'%s'", escapeSQLString(userQuery)))
	}
	if len(cols) == 0 {
		return ""
	}
	return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", tableName, strings.Join(cols, ", "), strings.Join(vals, ", "))
}

func extractFirstAmount(text string) float64 {
	re := regexp.MustCompile(`(\d+(?:\.\d+)?)`)
	m := re.FindStringSubmatch(text)
	if len(m) < 2 {
		return 0
	}
	var f float64
	_, _ = fmt.Sscanf(m[1], "%f", &f)
	return f
}

func inferCategory(text string) string {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "gpt"), strings.Contains(lower, "软件"), strings.Contains(lower, "会员"), strings.Contains(lower, "订阅"):
		return "软件服务"
	case strings.Contains(lower, "玩偶"), strings.Contains(lower, "玩具"):
		return "购物娱乐"
	case strings.Contains(lower, "餐"), strings.Contains(lower, "吃"), strings.Contains(lower, "外卖"):
		return "餐饮"
	default:
		return "未分类"
	}
}

func uniqueStrings(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		key := strings.ToLower(item)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, item)
	}
	return out
}

func (a *Agent) ensureSchemaColumns(ctx context.Context, tool tools.Tool, tableName string) ([]toolExecutionRecord, []string, error) {
	sqls := buildSchemaPatchSQLs(tableName)
	records := make([]toolExecutionRecord, 0, len(sqls))
	summaries := make([]string, 0, len(sqls))
	for _, sqlText := range sqls {
		col := extractAddedColumnName(sqlText)
		if col == "" {
			continue
		}
		exists, err := schemaColumnExists(ctx, tool, tableName, col)
		if err != nil {
			return records, summaries, err
		}
		if exists {
			continue
		}
		out, err := tool.Execute(ctx, map[string]any{"sql": sqlText})
		if err != nil {
			return records, summaries, err
		}
		text := extractToolText(out)
		records = append(records, toolExecutionRecord{
			Step:     "schema_patch_" + col,
			ToolName: "mysql_exec",
			Request:  map[string]any{"sql": sqlText},
			Response: out,
			Text:     truncateText(text, 300),
		})
		summaries = append(summaries, fmt.Sprintf("已补齐字段 %s。", col))
	}
	return records, summaries, nil
}

func schemaColumnExists(ctx context.Context, tool tools.Tool, tableName string, column string) (bool, error) {
	sqlText := fmt.Sprintf("SELECT COUNT(*) AS cnt FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = '%s' AND COLUMN_NAME = '%s'",
		escapeSQLString(tableName), escapeSQLString(column))
	out, err := tool.Execute(ctx, map[string]any{"sql": sqlText})
	if err != nil {
		return false, err
	}
	parsed := parseJSONLikeText(extractToolText(out))
	m, ok := parsed.(map[string]any)
	if !ok {
		return false, fmt.Errorf("invalid information_schema response")
	}
	rows, _ := m["rows"].([]any)
	if len(rows) == 0 {
		return false, nil
	}
	row, _ := rows[0].(map[string]any)
	cnt := toInt(row["cnt"])
	return cnt > 0, nil
}

func extractAddedColumnName(sqlText string) string {
	re := regexp.MustCompile(`(?i)add\s+column\s+([a-zA-Z0-9_]+)`)
	m := re.FindStringSubmatch(strings.TrimSpace(sqlText))
	if len(m) >= 2 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func toInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		var n int
		_, _ = fmt.Sscanf(strings.TrimSpace(t), "%d", &n)
		return n
	default:
		return 0
	}
}

func buildSchemaPatchSQLs(tableName string) []string {
	tableName = strings.TrimSpace(tableName)
	if tableName == "" {
		return nil
	}
	return []string{
		fmt.Sprintf("ALTER TABLE %s ADD COLUMN bill_date DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP", tableName),
		fmt.Sprintf("ALTER TABLE %s ADD COLUMN amount DECIMAL(18,2) NOT NULL DEFAULT 0", tableName),
		fmt.Sprintf("ALTER TABLE %s ADD COLUMN category VARCHAR(64) NOT NULL DEFAULT '未分类'", tableName),
		fmt.Sprintf("ALTER TABLE %s ADD COLUMN bill_type VARCHAR(16) NOT NULL DEFAULT 'expense'", tableName),
		fmt.Sprintf("ALTER TABLE %s ADD COLUMN note VARCHAR(255) NOT NULL DEFAULT ''", tableName),
		fmt.Sprintf("ALTER TABLE %s ADD COLUMN raw_text TEXT NULL", tableName),
		fmt.Sprintf("ALTER TABLE %s ADD COLUMN created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP", tableName),
		fmt.Sprintf("ALTER TABLE %s ADD COLUMN updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP", tableName),
	}
}

func normalizeSQLForMySQL(sqlText string) string {
	sqlText = strings.TrimSpace(sqlText)
	if sqlText == "" {
		return ""
	}

	replacements := map[string]string{
		"AUTOINCREMENT":        "AUTO_INCREMENT",
		"autoincrement":        "AUTO_INCREMENT",
		"INTEGER PRIMARY KEY":  "BIGINT PRIMARY KEY",
		"integer primary key":  "BIGINT PRIMARY KEY",
		"datetime('now')":      "CURRENT_TIMESTAMP",
		"DATE('now')":          "CURRENT_DATE",
		"date('now')":          "CURRENT_DATE",
		"IF NOT EXISTS":        "IF NOT EXISTS",
		"WITHOUT ROWID":        "",
		"STRICT":               "",
		"ON CONFLICT REPLACE":  "",
		"ON CONFLICT IGNORE":   "",
		"ON CONFLICT ABORT":    "",
		"ON CONFLICT ROLLBACK": "",
	}
	for oldVal, newVal := range replacements {
		sqlText = strings.ReplaceAll(sqlText, oldVal, newVal)
	}

	sqlText = strings.ReplaceAll(sqlText, "`", "")
	return strings.TrimSpace(sqlText)
}

func looksLikeNonMySQLDDL(sqlText string) bool {
	s := strings.ToLower(strings.TrimSpace(sqlText))
	if s == "" {
		return false
	}
	badSignals := []string{
		"autoincrement",
		"without rowid",
		"pragma ",
		"on conflict",
		"strict",
	}
	for _, signal := range badSignals {
		if strings.Contains(s, signal) {
			return true
		}
	}
	return false
}

func defaultEnsureTableSQL(tableName string) string {
	return fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s ("+
		"id BIGINT PRIMARY KEY AUTO_INCREMENT,"+
		"bill_date DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,"+
		"amount DECIMAL(18,2) NOT NULL DEFAULT 0,"+
		"category VARCHAR(64) NOT NULL DEFAULT '未分类',"+
		"bill_type VARCHAR(16) NOT NULL DEFAULT 'expense',"+
		"note VARCHAR(255) NOT NULL DEFAULT '',"+
		"raw_text TEXT NULL,"+
		"created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,"+
		"updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP"+
		")", tableName)
}

func fallbackAkshareRequest(purpose string) (string, map[string]any) {
	switch strings.TrimSpace(purpose) {
	case "news":
		return "news_all", map[string]any{"query": "最新财经资讯", "limit": 10}
	default:
		return "stock_news_main_cx", map[string]any{"query": "宏观经济 市场 利率", "limit": 10}
	}
}

func stringifyPayloadBlock(v any) string {
	if v == nil {
		return ""
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err == nil {
		return string(data)
	}
	return fmt.Sprintf("%v", v)
}

func extractToolText(out map[string]any) string {
	if out == nil {
		return ""
	}
	return strings.TrimSpace(flattenText(out["content"]))
}

func flattenText(v any) string {
	switch vv := v.(type) {
	case nil:
		return ""
	case string:
		return vv
	case []byte:
		return string(vv)
	case []any:
		parts := make([]string, 0, len(vv))
		for _, item := range vv {
			text := flattenText(item)
			if strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		for _, key := range []string{"text", "content", "body", "value"} {
			if raw, ok := vv[key]; ok {
				if text := flattenText(raw); strings.TrimSpace(text) != "" {
					return text
				}
			}
		}
		data, err := json.Marshal(vv)
		if err == nil {
			return string(data)
		}
	}
	return strings.TrimSpace(fmt.Sprintf("%v", v))
}

func parseJSONLikeText(text string) any {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	start := strings.IndexAny(text, "[{")
	endObj := strings.LastIndex(text, "}")
	endArr := strings.LastIndex(text, "]")
	end := endObj
	if endArr > end {
		end = endArr
	}
	if start < 0 || end < start {
		return nil
	}
	candidate := strings.TrimSpace(text[start : end+1])
	var out any
	if err := json.Unmarshal([]byte(candidate), &out); err != nil {
		return nil
	}
	return out
}

func summarizeSQLResult(sqlText string, parsed any, raw string) string {
	lower := strings.ToLower(strings.TrimSpace(sqlText))
	switch {
	case strings.HasPrefix(lower, "create table"):
		return "已确认用户专属账单表可用。"
	case strings.HasPrefix(lower, "insert"):
		return "账单已写入数据库。"
	case strings.HasPrefix(lower, "update"):
		return "账单已更新。"
	case strings.HasPrefix(lower, "delete"):
		return "账单已删除。"
	case strings.HasPrefix(lower, "select"):
		if m, ok := parsed.(map[string]any); ok {
			if rows, ok := m["rows"].([]any); ok {
				return fmt.Sprintf("已查询到 %d 条账单记录。", len(rows))
			}
		}
		if strings.TrimSpace(raw) != "" {
			return "已完成账单查询。"
		}
	}
	return "已完成数据库操作。"
}

func summarizeAkshareResult(toolName string, args map[string]any, parsed any, raw string) string {
	query := strings.TrimSpace(fmt.Sprint(args["query"]))
	if query == "" {
		query = "财经主题"
	}
	if arr, ok := parsed.([]any); ok {
		return fmt.Sprintf("已通过 %s 获取 %d 条与“%s”相关的金融资讯。", toolName, len(arr), query)
	}
	if m, ok := parsed.(map[string]any); ok {
		return fmt.Sprintf("已通过 %s 获取“%s”的金融数据，返回字段 %d 个。", toolName, query, len(m))
	}
	if strings.TrimSpace(raw) != "" {
		return fmt.Sprintf("已通过 %s 获取“%s”的金融资讯。", toolName, query)
	}
	return "已完成金融资讯读取。"
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

func truncateSQLForLog(sqlText string) string {
	sqlText = strings.Join(strings.Fields(strings.TrimSpace(sqlText)), " ")
	if len(sqlText) <= 240 {
		return sqlText
	}
	return sqlText[:240] + "..."
}

func escapeSQLString(in string) string {
	return strings.ReplaceAll(strings.TrimSpace(in), "'", "''")
}

func containsAny(input string, keywords []string) bool {
	for _, keyword := range keywords {
		if strings.Contains(input, keyword) {
			return true
		}
	}
	return false
}

func cloneAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
