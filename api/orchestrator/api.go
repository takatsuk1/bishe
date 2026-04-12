package orchestrator

import (
	"ai/config"
	"ai/pkg/agentmanager"
	"ai/pkg/authz"
	"ai/pkg/executor"
	"ai/pkg/logger"
	"ai/pkg/monitor"
	"ai/pkg/storage"
	"ai/pkg/tools"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func extractIDFromPath(path string, prefix string) string {
	if len(path) <= len(prefix) {
		return ""
	}
	remaining := path[len(prefix):]
	parts := strings.Split(remaining, "/")
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}

// OrchestratorAPI 处理 orchestrator 相关的 API 请求
type OrchestratorAPI struct {
	workflows        map[string]WorkflowDefinition
	runs             map[string]RunResult
	workflowUpdates  map[string]time.Time
	agentWorkflowAPI *AgentWorkflowAPI
	userToolAPI      *UserToolAPI
	userAgentAPI     *UserAgentAPI
}

// NewOrchestratorAPI 创建一个新的 OrchestratorAPI 实例
func NewOrchestratorAPI() *OrchestratorAPI {
	return &OrchestratorAPI{
		workflows:        make(map[string]WorkflowDefinition),
		runs:             make(map[string]RunResult),
		workflowUpdates:  make(map[string]time.Time),
		agentWorkflowAPI: NewAgentWorkflowAPI(nil),
		userToolAPI:      NewUserToolAPI(nil),
		userAgentAPI:     NewUserAgentAPI(nil, nil, nil),
	}
}

// NewOrchestratorAPIWithStorage 创建一个带存储的 OrchestratorAPI 实例
func NewOrchestratorAPIWithStorage(mysqlStorage *storage.MySQLStorage) *OrchestratorAPI {
	projectRoot, _ := os.Getwd()
	processMgr := agentmanager.NewAgentProcessManager(agentmanager.ManagerConfig{
		ProjectRoot: projectRoot,
		AgentsDir:   filepath.Join(projectRoot, "agents", "user_agents"),
	}, mysqlStorage)

	if mysqlStorage != nil {
		callTool, err := tools.NewCallAgentTool(context.Background(), config.GetMainConfig(), func(ctx context.Context, userID string) ([]string, error) {
			if userID == "" {
				return nil, nil
			}
			rows, listErr := mysqlStorage.ListUserAgents(ctx, userID)
			if listErr != nil {
				return nil, listErr
			}
			out := make([]string, 0, len(rows))
			for _, a := range rows {
				if a.Status == storage.AgentStatusPublished {
					out = append(out, a.AgentID)
				}
			}
			return out, nil
		})
		if err != nil {
			logger.Warnf("[OrchestratorAPI] init call_agent tool failed: %v", err)
		} else {
			reg := tools.GetGlobalRegistry()
			if reg.Exists(callTool.Info().Name) {
				_ = reg.Unregister(callTool.Info().Name)
			}
			if regErr := reg.Register(callTool); regErr != nil {
				logger.Warnf("[OrchestratorAPI] register call_agent tool failed: %v", regErr)
			}
		}
	}

	exec := executor.NewInterpretiveExecutor(executor.ExecutionConfig{}, tools.GetGlobalRegistry())
	if mysqlStorage != nil {
		exec.SetMonitorService(monitor.NewService(mysqlStorage, nil))
	}

	return &OrchestratorAPI{
		workflows:        make(map[string]WorkflowDefinition),
		runs:             make(map[string]RunResult),
		workflowUpdates:  make(map[string]time.Time),
		agentWorkflowAPI: NewAgentWorkflowAPI(nil),
		userToolAPI:      NewUserToolAPI(mysqlStorage),
		userAgentAPI:     NewUserAgentAPI(mysqlStorage, exec, processMgr),
	}
}

// Handler 返回一个 HTTP handler，用于处理 orchestrator API 请求
func (api *OrchestratorAPI) Handler() http.Handler {
	mux := http.NewServeMux()

	// 注册路由 - 注意：更具体的路由要放在前面
	mux.HandleFunc("/v1/orchestrator/agents", api.handleListAgents)
	mux.HandleFunc("/v1/orchestrator/agent-workflows", api.handleAgentWorkflows)
	mux.HandleFunc("/v1/orchestrator/agent-workflows/", api.handleAgentWorkflowByID)
	mux.HandleFunc("/v1/orchestrator/tools", api.handleListTools)
	mux.HandleFunc("/v1/orchestrator/user-workflows", api.handleListUserWorkflows)
	mux.HandleFunc("/v1/orchestrator/user-workflows/save", api.handleSaveUserWorkflow)
	mux.HandleFunc("/v1/orchestrator/user-workflows/", api.handleUserWorkflowByID)
	mux.HandleFunc("/v1/orchestrator/workflows", api.handleWorkflows)
	mux.HandleFunc("/v1/orchestrator/workflows/", api.handleWorkflow)
	mux.HandleFunc("/v1/orchestrator/runs/", api.handleRun)

	// 用户工具和Agent路由
	if api.userToolAPI != nil {
		api.userToolAPI.RegisterRoutes(mux)
	}
	if api.userAgentAPI != nil {
		api.userAgentAPI.RegisterRoutes(mux)
	}

	// 根路径路由放在最后
	mux.HandleFunc("/v1/orchestrator/", api.handleRoot)

	return mux
}

// handleRoot 处理根路径请求
func (api *OrchestratorAPI) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/orchestrator/" {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("orchestrator API"))
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

// handleListAgents 处理列出所有 agent 的请求
func (api *OrchestratorAPI) handleListAgents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	userID, ok := authenticatedUserID(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}

	// 获取内置 agent 信息
	agents := []AgentInfo{
		{ID: "host", Name: "主控编排助手", Description: "主控助手，负责路由决策和调用其他助手"},
		{ID: "deepresearch", Name: "深度检索助手", Description: "深度检索助手，使用 Tavily 进行深度检索并整理答案"},
		{ID: "urlreader", Name: "网页阅读助手", Description: "网页阅读助手，使用本地 Fetch MCP 读取网页内容并生成回答"},
		{ID: "lbshelper", Name: "出行助手", Description: "出行助手，使用 AMap 和 Tavily 规划行程"},
		{ID: "schedulehelper", Name: "日程规划助手", Description: "日程规划助手，输出任务优先级和时间安排建议"},
		{ID: "financehelper", Name: "财务助手", Description: "财务助手，支持记账、财务报告、财经资讯整理与理财建议"},
		{ID: "bazihelper", Name: "八字助手", Description: "八字助手，调用 Bazi MCP 工具生成命盘并输出结构化解读"},
		{ID: "resumecustomizer", Name: "简历优化助手", Description: "简历优化助手，结合目标岗位与上传简历生成定制版简历"},
		{ID: "interviewsimulator", Name: "面试模拟助手", Description: "面试模拟助手，基于上传简历生成结构化模拟面试内容"},
		{ID: "careerradar", Name: "职场雷达助手", Description: "职场雷达助手，调用 deepresearch 推荐匹配岗位并识别高风险岗位描述"},
	}

	// 追加已发布用户 agent，保证 chat/workflow 页面可选择。
	if api.userAgentAPI != nil && api.userAgentAPI.storage != nil {
		allowAllRead := hasAllScopeAccess(r, "orchestrator.agent.read")
		existing := make(map[string]struct{}, len(agents))
		for _, a := range agents {
			existing[a.ID] = struct{}{}
		}

		appendPublished := func(source []storage.UserAgentDefinition) {
			for _, ua := range source {
				if ua.Status != storage.AgentStatusPublished {
					continue
				}
				if _, found := existing[ua.AgentID]; found {
					continue
				}
				agents = append(agents, AgentInfo{
					ID:          ua.AgentID,
					Name:        ua.Name,
					Description: ua.Description,
				})
				existing[ua.AgentID] = struct{}{}
			}
		}

		if allowAllRead {
			if allAgents, err := api.userAgentAPI.storage.ListUserAgents(r.Context(), ""); err != nil {
				logger.Warnf("[OrchestratorAPI] list all user agents failed: %v", err)
			} else {
				appendPublished(allAgents)
			}
		} else {
			if userAgents, err := api.userAgentAPI.storage.ListUserAgents(r.Context(), userID); err != nil {
				logger.Warnf("[OrchestratorAPI] list user agents failed: %v", err)
			} else {
				appendPublished(userAgents)
			}
			if systemAgents, err := api.userAgentAPI.storage.ListUserAgents(r.Context(), "system"); err != nil {
				logger.Warnf("[OrchestratorAPI] list system agents failed: %v", err)
			} else {
				appendPublished(systemAgents)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(agents); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

// handleListTools 处理列出所有已注册工具的请求
func (api *OrchestratorAPI) handleListTools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	userID, ok := authenticatedUserID(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}

	ctx := r.Context()

	// 1. 获取全局已注册的工具
	toolInfos := tools.ListTools()

	// 转换为 API 响应格式
	apiTools := make([]ToolInfo, 0, len(toolInfos))
	for _, ti := range toolInfos {
		params := make([]ToolParameterInfo, 0, len(ti.Parameters))
		for _, p := range ti.Parameters {
			params = append(params, ToolParameterInfo{
				Name:        p.Name,
				Type:        string(p.Type),
				Required:    p.Required,
				Description: p.Description,
			})
		}
		apiTools = append(apiTools, ToolInfo{
			Name:        ti.Name,
			Type:        string(ti.Type),
			Description: ti.Description,
			Parameters:  params,
		})
	}
	existingNames := make(map[string]struct{}, len(apiTools))
	for _, t := range apiTools {
		existingNames[t.Name] = struct{}{}
	}

	// 2. 获取用户定义的工具（从数据库）
	if api.userToolAPI != nil && api.userToolAPI.storage != nil {
		appendTools := func(dbTools []storage.UserToolDefinition) {
			for _, t := range dbTools {
				if _, found := existingNames[t.Name]; found {
					continue
				}
				params := make([]ToolParameterInfo, 0, len(t.Parameters))
				for _, p := range t.Parameters {
					params = append(params, ToolParameterInfo{
						Name:        p.Name,
						Type:        p.Type,
						Required:    p.Required,
						Description: p.Description,
					})
				}
				outputParams := make([]ToolParameterInfo, 0, len(t.OutputParameters))
				for _, p := range t.OutputParameters {
					outputParams = append(outputParams, ToolParameterInfo{
						Name:        p.Name,
						Type:        p.Type,
						Required:    p.Required,
						Description: p.Description,
					})
				}
				apiTools = append(apiTools, ToolInfo{
					Name:             t.Name,
					Type:             t.ToolType,
					Description:      t.Description,
					Parameters:       params,
					OutputParameters: outputParams,
				})
				existingNames[t.Name] = struct{}{}
			}
		}

		if userTools, err := api.userToolAPI.storage.ListUserTools(ctx, userID); err != nil {
			logger.Warnf("[handleListTools] failed to list user tools: %v", err)
		} else {
			appendTools(userTools)
		}
		if systemTools, err := api.userToolAPI.storage.ListUserTools(ctx, "system"); err != nil {
			logger.Warnf("[handleListTools] failed to list system tools: %v", err)
		} else {
			appendTools(systemTools)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(apiTools); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

// ToolInfo 表示工具信息
type ToolInfo struct {
	Name             string              `json:"name"`
	Type             string              `json:"type"`
	Description      string              `json:"description"`
	Parameters       []ToolParameterInfo `json:"parameters"`
	OutputParameters []ToolParameterInfo `json:"outputParameters"`
}

// ToolParameterInfo 表示工具参数信息
type ToolParameterInfo struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
}

// handleAgentWorkflows 处理获取所有 agent 编排结构的请求
func (api *OrchestratorAPI) handleAgentWorkflows(w http.ResponseWriter, r *http.Request) {
	api.agentWorkflowAPI.handleAgentWorkflows(w, r)
}

// handleAgentWorkflowByID 处理获取单个 agent 编排结构的请求
func (api *OrchestratorAPI) handleAgentWorkflowByID(w http.ResponseWriter, r *http.Request) {
	api.agentWorkflowAPI.handleAgentWorkflowByID(w, r)
}

// handleWorkflows 处理工作流列表请求
func (api *OrchestratorAPI) handleWorkflows(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		api.handleListWorkflows(w, r)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleStartWorkflowRun 处理启动工作流运行的请求
func (api *OrchestratorAPI) handleStartWorkflowRun(w http.ResponseWriter, r *http.Request, workflowID string) {
	if strings.TrimSpace(workflowID) == "" {
		http.Error(w, "workflow id is required", http.StatusBadRequest)
		return
	}

	// 解析请求体
	var request struct {
		Input map[string]interface{} `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// 生成运行 ID
	runID := fmt.Sprintf("%s:run:%d", workflowID, time.Now().UnixNano())

	// 创建运行结果
	run := RunResult{
		RunID:       runID,
		WorkflowID:  workflowID,
		State:       "succeeded",
		StartedAt:   time.Now().Format(time.RFC3339),
		FinishedAt:  time.Now().Format(time.RFC3339),
		UpdatedAt:   time.Now().Format(time.RFC3339),
		NodeResults: []NodeRunResult{},
		FinalOutput: request.Input,
	}

	// 存储运行结果
	api.runs[runID] = run

	// 这里可以添加实际启动工作流运行的逻辑

	response := struct {
		RunID string `json:"runId"`
	}{
		RunID: runID,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

// handleListWorkflows 处理列出所有工作流的请求
func (api *OrchestratorAPI) handleListWorkflows(w http.ResponseWriter, r *http.Request) {
	logger.Infof("[ListWorkflows] 开始获取工作流列表")

	ctx := r.Context()
	userID, ok := authenticatedUserID(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if _, allowed := authorizeResourceAccess(r, "orchestrator.workflow.read", authz.ScopeOwn, userID, false); !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	allowReadAll := hasAllScopeAccess(r, "orchestrator.workflow.read")

	type DraftWorkflowSummary struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		TTLMinutes int    `json:"ttlMinutes"`
		IsDraft    bool   `json:"isDraft"`
	}

	type WorkflowListResponse struct {
		Saved  []WorkflowSummary      `json:"saved"`
		Drafts []DraftWorkflowSummary `json:"drafts"`
	}

	response := WorkflowListResponse{
		Saved:  []WorkflowSummary{},
		Drafts: []DraftWorkflowSummary{},
	}

	db, dbErr := storage.GetMySQLStorage()
	if dbErr == nil {
		saved := make([]storage.WorkflowSummary, 0)
		if allowReadAll {
			dbWorkflows, err := db.ListWorkflowsScoped(ctx, userID, true)
			if err == nil {
				saved = append(saved, dbWorkflows...)
			}
		} else {
			if own, err := db.ListWorkflows(ctx, userID); err == nil {
				saved = append(saved, own...)
			}
			if systemWF, err := db.ListWorkflows(ctx, "system"); err == nil {
				saved = append(saved, systemWF...)
			}
		}

		seen := make(map[string]struct{}, len(saved))
		for _, wf := range saved {
			if _, ok := seen[wf.WorkflowID]; ok {
				continue
			}
			seen[wf.WorkflowID] = struct{}{}
			response.Saved = append(response.Saved, WorkflowSummary{
				ID:        wf.WorkflowID,
				Name:      wf.Name,
				UpdatedAt: wf.UpdatedAt.Format(time.RFC3339),
			})
		}
	}

	drafts, err := storage.GetAllDraftWorkflows(ctx)
	if err == nil {
		for _, draft := range drafts {
			owner := strings.TrimSpace(draft.UserID)
			if !allowReadAll && owner != strings.TrimSpace(userID) && owner != "system" {
				continue
			}
			ttl, _ := storage.GetDraftTTL(ctx, draft.WorkflowID)
			ttlMinutes := int(ttl.Minutes())
			if ttlMinutes < 0 {
				ttlMinutes = 0
			}
			response.Drafts = append(response.Drafts, DraftWorkflowSummary{
				ID:         draft.WorkflowID,
				Name:       draft.Name,
				TTLMinutes: ttlMinutes,
				IsDraft:    true,
			})
		}
	}

	logger.Infof("[ListWorkflows] 返回 %d 个已保存工作流, %d 个草稿", len(response.Saved), len(response.Drafts))

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

// handleWorkflow 处理单个工作流的请求
func (api *OrchestratorAPI) handleWorkflow(w http.ResponseWriter, r *http.Request) {
	// 提取工作流 ID
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/orchestrator/workflows/"), "/")
	workflowID := parts[0]

	// 检查是否是 runs 子路径
	if len(parts) > 1 && parts[1] == "runs" {
		if r.Method == http.MethodPost {
			api.handleStartWorkflowRun(w, r, workflowID)
		} else {
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}

	switch r.Method {
	case http.MethodGet:
		api.handleGetWorkflow(w, r, workflowID)
	case http.MethodPut:
		api.handlePutWorkflow(w, r, workflowID)
	case http.MethodDelete:
		api.handleDeleteWorkflow(w, r, workflowID)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleGetWorkflow 处理获取单个工作流的请求
func (api *OrchestratorAPI) handleGetWorkflow(w http.ResponseWriter, r *http.Request, workflowID string) {
	logger.Infof("[GetWorkflow] 获取工作流: %s", workflowID)

	ctx := r.Context()
	userID, ok := authenticatedUserID(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if _, allowed := authorizeResourceAccess(r, "orchestrator.workflow.read", authz.ScopeOwn, userID, false); !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	allowReadAll := hasAllScopeAccess(r, "orchestrator.workflow.read")

	draft, err := storage.GetDraftWorkflow(ctx, workflowID)
	if err == nil && draft != nil {
		owner := strings.TrimSpace(draft.UserID)
		if !allowReadAll && owner != strings.TrimSpace(userID) && owner != "system" {
			http.Error(w, "workflow not found", http.StatusNotFound)
			return
		}
		logger.Infof("[GetWorkflow] 从 Redis 草稿获取: %s", workflowID)

		var nodes []NodeDefinition
		if draft.Nodes != "" {
			json.Unmarshal([]byte(draft.Nodes), &nodes)
		}
		nodes = normalizeAPINodeDefinitions(nodes)

		var edges []EdgeDefinition
		if draft.Edges != "" {
			json.Unmarshal([]byte(draft.Edges), &edges)
		}

		workflow := WorkflowDefinition{
			ID:          draft.WorkflowID,
			Name:        draft.Name,
			Description: draft.Description,
			StartNodeId: draft.StartNodeID,
			Nodes:       nodes,
			Edges:       edges,
		}

		ttl, _ := storage.GetDraftTTL(ctx, workflowID)
		response := struct {
			Definition WorkflowDefinition `json:"definition"`
			UpdatedAt  string             `json:"updatedAt"`
			IsDraft    bool               `json:"isDraft"`
			TTLMinutes int                `json:"ttlMinutes"`
		}{
			Definition: workflow,
			UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
			IsDraft:    true,
			TTLMinutes: int(ttl.Minutes()),
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
		return
	}

	db, err := storage.GetMySQLStorage()
	if err != nil {
		logger.Errorf("[GetWorkflow] MySQL 存储不可用: %v", err)
		http.Error(w, "workflow not found", http.StatusNotFound)
		return
	}

	dbWorkflow, err := db.GetWorkflow(ctx, workflowID)
	if err != nil {
		logger.Errorf("[GetWorkflow] 从数据库获取失败: %v", err)
		http.Error(w, "workflow not found", http.StatusNotFound)
		return
	}
	owner := strings.TrimSpace(dbWorkflow.UserID)
	if !allowReadAll && owner != strings.TrimSpace(userID) && owner != "system" {
		http.Error(w, "workflow not found", http.StatusNotFound)
		return
	}

	nodes := make([]NodeDefinition, 0, len(dbWorkflow.Nodes))
	for _, n := range dbWorkflow.Nodes {
		nodes = append(nodes, NodeDefinition{
			ID:         n.ID,
			Type:       n.Type,
			Config:     n.Config,
			AgentID:    n.AgentID,
			TaskType:   n.TaskType,
			InputType:  n.InputType,
			OutputType: n.OutputType,
			Condition:  n.Condition,
			PreInput:   n.PreInput,
			LoopConfig: convertLoopConfigFromStorage(n.LoopConfig),
			Metadata:   n.Metadata,
		})
	}
	nodes = normalizeAPINodeDefinitions(nodes)

	edges := make([]EdgeDefinition, 0, len(dbWorkflow.Edges))
	for _, e := range dbWorkflow.Edges {
		edges = append(edges, EdgeDefinition{
			From:    e.From,
			To:      e.To,
			Label:   e.Label,
			Mapping: convertMappingFromStorage(e.Mapping),
		})
	}

	workflow := WorkflowDefinition{
		ID:          dbWorkflow.WorkflowID,
		Name:        dbWorkflow.Name,
		Description: dbWorkflow.Description,
		StartNodeId: dbWorkflow.StartNodeID,
		Nodes:       nodes,
		Edges:       edges,
	}

	response := struct {
		Definition WorkflowDefinition `json:"definition"`
		UpdatedAt  string             `json:"updatedAt"`
		IsDraft    bool               `json:"isDraft"`
		TTLMinutes int                `json:"ttlMinutes"`
	}{
		Definition: workflow,
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
		IsDraft:    false,
		TTLMinutes: 0,
	}

	logger.Infof("[GetWorkflow] 返回工作流: %s, nodes=%d, edges=%d", workflowID, len(nodes), len(edges))

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func convertLoopConfigFromStorage(m map[string]interface{}) *LoopConfig {
	if m == nil {
		return nil
	}
	lc := &LoopConfig{}
	if v, ok := m["maxIterations"]; ok {
		if f, ok := v.(float64); ok {
			lc.MaxIterations = int(f)
		}
	}
	if v, ok := m["continueTo"]; ok {
		if s, ok := v.(string); ok {
			lc.ContinueTo = s
		}
	}
	if v, ok := m["exitTo"]; ok {
		if s, ok := v.(string); ok {
			lc.ExitTo = s
		}
	}
	return lc
}

func convertMappingFromStorage(m map[string]interface{}) map[string]string {
	if m == nil {
		return nil
	}
	result := make(map[string]string)
	for k, v := range m {
		if s, ok := v.(string); ok {
			result[k] = s
		}
	}
	return result
}

// handlePutWorkflow 处理创建或更新工作流的请求（保存到 Redis 草稿）
func (api *OrchestratorAPI) handlePutWorkflow(w http.ResponseWriter, r *http.Request, workflowID string) {
	logger.Infof("[PutWorkflow] ========== 开始处理工作流请求（Redis 草稿）==========")
	logger.Infof("[PutWorkflow] workflowID: %s", workflowID)
	userID, ok := authenticatedUserID(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if _, allowed := authorizeResourceAccess(r, "orchestrator.workflow.manage", authz.ScopeOwn, userID, false); !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	var workflow WorkflowDefinition
	if err := json.NewDecoder(r.Body).Decode(&workflow); err != nil {
		logger.Errorf("[PutWorkflow] 解析请求体失败: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	logger.Infof("[PutWorkflow] 解析成功: ID=%s, Name=%s, StartNodeId=%s, Nodes=%d, Edges=%d",
		workflow.ID, workflow.Name, workflow.StartNodeId, len(workflow.Nodes), len(workflow.Edges))

	workflow.ID = workflowID
	workflow.Nodes = normalizeAPINodeDefinitions(workflow.Nodes)

	nodesJSON, _ := json.Marshal(workflow.Nodes)
	edgesJSON, _ := json.Marshal(workflow.Edges)

	draft := &storage.DraftWorkflow{
		WorkflowID:  workflowID,
		UserID:      userID,
		Name:        workflow.Name,
		Description: workflow.Description,
		StartNodeID: workflow.StartNodeId,
		Nodes:       string(nodesJSON),
		Edges:       string(edgesJSON),
	}

	ctx := r.Context()
	allowManageAll := hasAllScopeAccess(r, "orchestrator.workflow.manage")
	if db, dbErr := storage.GetMySQLStorage(); dbErr == nil {
		if existing, getErr := db.GetWorkflowScoped(ctx, workflowID, userID, allowManageAll); getErr == nil && existing != nil {
			if _, allowed := authorizeResourceAccess(r, "orchestrator.workflow.manage", authz.ScopeOwn, existing.UserID, false); !allowed {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
	}
	if existingDraft, draftErr := storage.GetDraftWorkflow(ctx, workflowID); draftErr == nil && existingDraft != nil {
		owner := strings.TrimSpace(existingDraft.UserID)
		if owner != "" && !allowManageAll && owner != strings.TrimSpace(userID) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}
	if err := storage.SaveDraftWorkflow(ctx, workflowID, draft); err != nil {
		logger.Errorf("[PutWorkflow] 保存到 Redis 失败: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	logger.Infof("[PutWorkflow] 已保存到 Redis 草稿（5分钟后过期，点击保存按钮才会写入数据库）")

	response := struct {
		ID        string `json:"id"`
		UpdatedAt string `json:"updatedAt"`
		IsDraft   bool   `json:"isDraft"`
	}{
		ID:        workflowID,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		IsDraft:   true,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		logger.Errorf("[PutWorkflow] 响应编码失败: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	logger.Infof("[PutWorkflow] ========== 工作流请求处理完成 ==========")
}

// handleDeleteWorkflow 处理删除工作流的请求
func (api *OrchestratorAPI) handleDeleteWorkflow(w http.ResponseWriter, r *http.Request, workflowID string) {
	logger.Infof("[DeleteWorkflow] ========== 开始处理删除请求 ==========")
	logger.Infof("[DeleteWorkflow] workflowID: %s", workflowID)
	userID, ok := authenticatedUserID(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if _, allowed := authorizeResourceAccess(r, "orchestrator.workflow.manage", authz.ScopeOwn, userID, false); !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	allowManageAll := hasAllScopeAccess(r, "orchestrator.workflow.manage")

	ctx := r.Context()
	if draft, err := storage.GetDraftWorkflow(ctx, workflowID); err == nil && draft != nil {
		owner := strings.TrimSpace(draft.UserID)
		if owner != "" && !allowManageAll && owner != strings.TrimSpace(userID) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}

	storage.DeleteDraftWorkflow(ctx, workflowID)
	logger.Infof("[DeleteWorkflow] 已从 Redis 草稿中删除")

	db, err := storage.GetMySQLStorage()
	if err != nil {
		logger.Errorf("[DeleteWorkflow] MySQL 存储不可用: %v", err)
	} else {
		if err := db.DeleteWorkflowScoped(ctx, workflowID, userID, allowManageAll); err != nil {
			logger.Errorf("[DeleteWorkflow] 从数据库删除失败: %v", err)
		} else {
			logger.Infof("[DeleteWorkflow] 成功从数据库删除: workflowId=%s", workflowID)
		}
	}

	response := struct {
		Deleted bool `json:"deleted"`
	}{
		Deleted: true,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		logger.Errorf("[DeleteWorkflow] 响应编码失败: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	logger.Infof("[DeleteWorkflow] ========== 删除请求处理完成 ==========")
}

// handleRun 处理运行相关的请求
func (api *OrchestratorAPI) handleRun(w http.ResponseWriter, r *http.Request) {
	// 提取运行 ID
	runID := strings.TrimPrefix(r.URL.Path, "/v1/orchestrator/runs/")

	if r.Method == http.MethodGet {
		api.handleGetRun(w, r, runID)
	} else {
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleGetRun 处理获取运行状态的请求
func (api *OrchestratorAPI) handleGetRun(w http.ResponseWriter, r *http.Request, runID string) {
	// 从 runs 映射中获取运行状态
	run, exists := api.runs[runID]
	if !exists {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(run); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

// 前端数据结构

// AgentInfo 表示 agent 信息
type AgentInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// WorkflowSummary 表示工作流摘要
type WorkflowSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	UpdatedAt string `json:"updatedAt"`
}

// NodeDefinition 表示节点定义
type NodeDefinition struct {
	ID            string                 `json:"id"`
	Type          string                 `json:"type"`
	Config        map[string]interface{} `json:"config,omitempty"`
	AgentID       string                 `json:"agentId,omitempty"`
	TaskType      string                 `json:"taskType,omitempty"`
	InputType     string                 `json:"inputType,omitempty"`
	OutputType    string                 `json:"outputType,omitempty"`
	InputPorts    []PortDefinition       `json:"inputPorts,omitempty"`
	OutputPorts   []PortDefinition       `json:"outputPorts,omitempty"`
	InputMapping  map[string]string      `json:"inputMapping,omitempty"`
	OutputMapping map[string]string      `json:"outputMapping,omitempty"`
	SchemaVersion int                    `json:"schemaVersion,omitempty"`
	Condition     string                 `json:"condition,omitempty"`
	PreInput      string                 `json:"preInput,omitempty"`
	LoopConfig    *LoopConfig            `json:"loopConfig,omitempty"`
	Metadata      map[string]string      `json:"metadata,omitempty"`
}

// PortDefinition 表示端口定义
type PortDefinition struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

// LoopConfig 表示循环配置
type LoopConfig struct {
	MaxIterations int    `json:"maxIterations"`
	ContinueTo    string `json:"continueTo"`
	ExitTo        string `json:"exitTo"`
}

// EdgeDefinition 表示边定义
type EdgeDefinition struct {
	From    string            `json:"from"`
	To      string            `json:"to"`
	Label   string            `json:"label,omitempty"`
	Mapping map[string]string `json:"mapping,omitempty"`
}

// WorkflowDefinition 表示工作流定义
type WorkflowDefinition struct {
	ID            string           `json:"id"`
	Name          string           `json:"name"`
	Description   string           `json:"description,omitempty"`
	SchemaVersion int              `json:"schemaVersion,omitempty"`
	StartNodeId   string           `json:"startNodeId"`
	Nodes         []NodeDefinition `json:"nodes"`
	Edges         []EdgeDefinition `json:"edges"`
}

// WorkflowGetResponse 表示获取工作流的响应
type WorkflowGetResponse struct {
	Definition WorkflowDefinition `json:"definition"`
	UpdatedAt  string             `json:"updatedAt"`
}

// NodeRunResult 表示节点运行结果
type NodeRunResult struct {
	NodeID   string                 `json:"nodeId"`
	TaskID   string                 `json:"taskId"`
	State    string                 `json:"state"`
	Output   map[string]interface{} `json:"output,omitempty"`
	ErrorMsg string                 `json:"errorMsg,omitempty"`
}

// RunResult 表示运行结果
type RunResult struct {
	RunID         string                 `json:"runId"`
	WorkflowID    string                 `json:"workflowId"`
	State         string                 `json:"state"`
	StartedAt     string                 `json:"startedAt"`
	FinishedAt    string                 `json:"finishedAt"`
	UpdatedAt     string                 `json:"updatedAt"`
	CurrentNodeID string                 `json:"currentNodeId,omitempty"`
	CurrentTaskID string                 `json:"currentTaskId,omitempty"`
	NodeResults   []NodeRunResult        `json:"nodeResults"`
	FinalOutput   map[string]interface{} `json:"finalOutput,omitempty"`
	ErrorMessage  string                 `json:"errorMessage,omitempty"`
}
