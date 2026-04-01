package orchestrator

import (
	"ai/pkg/logger"
	"ai/pkg/storage"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	APIVersionV1 = "v1"
)

type AgentWorkflowAPI struct {
	rateLimiter  *RateLimiter
	authProvider AuthProvider
	cache        *AgentWorkflowCache
}

type RateLimiter struct {
	mu         sync.Mutex
	requests   map[string][]time.Time
	maxReqs    int
	windowSize time.Duration
}

type AuthProvider interface {
	ValidateToken(token string) (bool, error)
}

type AgentWorkflowCache struct {
	mu      sync.RWMutex
	data    *AgentWorkflowsResponse
	expires time.Time
	ttl     time.Duration
}

func NewAgentWorkflowAPI(authProvider AuthProvider) *AgentWorkflowAPI {
	return &AgentWorkflowAPI{
		rateLimiter:  NewRateLimiter(100, time.Minute),
		authProvider: authProvider,
		cache: &AgentWorkflowCache{
			ttl: 5 * time.Minute,
		},
	}
}

func NewRateLimiter(maxReqs int, windowSize time.Duration) *RateLimiter {
	return &RateLimiter{
		requests:   make(map[string][]time.Time),
		maxReqs:    maxReqs,
		windowSize: windowSize,
	}
}

func (rl *RateLimiter) Allow(clientID string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	windowStart := now.Add(-rl.windowSize)

	requests := rl.requests[clientID]
	validRequests := make([]time.Time, 0, len(requests))
	for _, t := range requests {
		if t.After(windowStart) {
			validRequests = append(validRequests, t)
		}
	}

	if len(validRequests) >= rl.maxReqs {
		rl.requests[clientID] = validRequests
		return false
	}

	validRequests = append(validRequests, now)
	rl.requests[clientID] = validRequests
	return true
}

func (c *AgentWorkflowCache) Get() *AgentWorkflowsResponse {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.data != nil && time.Now().Before(c.expires) {
		return c.data
	}
	return nil
}

func (c *AgentWorkflowCache) Set(data *AgentWorkflowsResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.data = data
	c.expires = time.Now().Add(c.ttl)
}

type AgentWorkflowsResponse struct {
	APIVersion string                `json:"apiVersion"`
	Timestamp  string                `json:"timestamp"`
	Agents     []AgentWorkflowDetail `json:"agents"`
}

type AgentWorkflowDetail struct {
	ID             string             `json:"id"`
	Name           string             `json:"name"`
	Type           string             `json:"type"`
	Description    string             `json:"description"`
	Version        string             `json:"version"`
	Configuration  AgentConfiguration `json:"configuration"`
	Dependencies   []AgentDependency  `json:"dependencies"`
	ExecutionOrder ExecutionOrder     `json:"executionOrder"`
	Nodes          []NodeDefinition   `json:"nodes"`
	Edges          []EdgeDefinition   `json:"edges"`
	Metadata       AgentMetadata      `json:"metadata"`
}

type AgentConfiguration struct {
	Timeout         int                    `json:"timeout"`
	RetryPolicy     RetryPolicy            `json:"retryPolicy"`
	InputSchema     map[string]interface{} `json:"inputSchema"`
	OutputSchema    map[string]interface{} `json:"outputSchema"`
	EnvironmentVars []EnvironmentVar       `json:"environmentVars"`
}

type RetryPolicy struct {
	MaxAttempts       int     `json:"maxAttempts"`
	InitialDelayMs    int     `json:"initialDelayMs"`
	MaxDelayMs        int     `json:"maxDelayMs"`
	BackoffMultiplier float64 `json:"backoffMultiplier"`
}

type EnvironmentVar struct {
	Name        string `json:"name"`
	Required    bool   `json:"required"`
	Default     string `json:"default,omitempty"`
	Description string `json:"description,omitempty"`
}

type AgentDependency struct {
	AgentID      string            `json:"agentId"`
	Type         string            `json:"type"`
	Required     bool              `json:"required"`
	Description  string            `json:"description"`
	InputMapping map[string]string `json:"inputMapping,omitempty"`
}

type ExecutionOrder struct {
	StartNodeID string     `json:"startNodeId"`
	Sequence    []string   `json:"sequence"`
	Parallel    [][]string `json:"parallel,omitempty"`
}

type AgentMetadata struct {
	CreatedAt string            `json:"createdAt"`
	UpdatedAt string            `json:"updatedAt"`
	Author    string            `json:"author"`
	Tags      []string          `json:"tags"`
	Labels    map[string]string `json:"labels"`
}

type ErrorResponse struct {
	APIVersion string      `json:"apiVersion"`
	Error      ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

const (
	ErrCodeUnauthorized      = "UNAUTHORIZED"
	ErrCodeRateLimitExceeded = "RATE_LIMIT_EXCEEDED"
	ErrCodeAgentNotFound     = "AGENT_NOT_FOUND"
	ErrCodeInternalError     = "INTERNAL_ERROR"
	ErrCodeInvalidRequest    = "INVALID_REQUEST"
)

func (api *AgentWorkflowAPI) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/agent-workflows", api.handleAgentWorkflows)
	mux.HandleFunc("/v1/agent-workflows/", api.handleAgentWorkflowByID)
	return mux
}

func (api *AgentWorkflowAPI) handleAgentWorkflows(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()

	if r.Method != http.MethodGet {
		api.writeError(w, http.StatusMethodNotAllowed, ErrCodeInvalidRequest, "Method not allowed", "")
		return
	}

	clientID := api.getClientID(r)
	if !api.rateLimiter.Allow(clientID) {
		api.writeError(w, http.StatusTooManyRequests, ErrCodeRateLimitExceeded,
			"Rate limit exceeded. Please try again later.", "")
		return
	}

	if api.authProvider != nil {
		token := r.Header.Get("Authorization")
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		if token != "" {
			if strings.HasPrefix(token, "Bearer ") {
				token = strings.TrimPrefix(token, "Bearer ")
			}
			valid, err := api.authProvider.ValidateToken(token)
			if err != nil {
				api.writeError(w, http.StatusInternalServerError, ErrCodeInternalError,
					"Authentication service error", err.Error())
				return
			}
			if !valid {
				api.writeError(w, http.StatusUnauthorized, ErrCodeUnauthorized,
					"Invalid or expired token", "")
				return
			}
		}
	}

	cached := api.cache.Get()
	if cached != nil {
		api.writeJSON(w, http.StatusOK, cached)
		return
	}

	response := api.buildAgentWorkflowsResponse()
	api.cache.Set(response)

	elapsed := time.Since(startTime)
	if elapsed > 200*time.Millisecond {
	}

	api.writeJSON(w, http.StatusOK, response)
}

func (api *AgentWorkflowAPI) handleAgentWorkflowByID(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()

	if r.Method != http.MethodGet {
		api.writeError(w, http.StatusMethodNotAllowed, ErrCodeInvalidRequest, "Method not allowed", "")
		return
	}

	clientID := api.getClientID(r)
	if !api.rateLimiter.Allow(clientID) {
		api.writeError(w, http.StatusTooManyRequests, ErrCodeRateLimitExceeded,
			"Rate limit exceeded. Please try again later.", "")
		return
	}

	agentID := strings.TrimPrefix(r.URL.Path, "/v1/agent-workflows/")
	if agentID == "" {
		api.writeError(w, http.StatusBadRequest, ErrCodeInvalidRequest, "Agent ID is required", "")
		return
	}

	response := api.buildAgentWorkflowsResponse()

	var found *AgentWorkflowDetail
	for i := range response.Agents {
		if response.Agents[i].ID == agentID {
			found = &response.Agents[i]
			break
		}
	}

	if found == nil {
		api.writeError(w, http.StatusNotFound, ErrCodeAgentNotFound,
			"Agent not found", fmt.Sprintf("Agent with ID '%s' does not exist", agentID))
		return
	}

	singleResponse := struct {
		APIVersion string              `json:"apiVersion"`
		Timestamp  string              `json:"timestamp"`
		Agent      AgentWorkflowDetail `json:"agent"`
	}{
		APIVersion: response.APIVersion,
		Timestamp:  response.Timestamp,
		Agent:      *found,
	}

	elapsed := time.Since(startTime)
	_ = elapsed

	api.writeJSON(w, http.StatusOK, singleResponse)
}

func (api *AgentWorkflowAPI) getClientID(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		return strings.Split(forwarded, ",")[0]
	}
	return r.RemoteAddr
}

func (api *AgentWorkflowAPI) writeJSON(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-API-Version", APIVersionV1)
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(data)
}

func (api *AgentWorkflowAPI) writeError(w http.ResponseWriter, statusCode int, code, message, details string) {
	errResp := ErrorResponse{
		APIVersion: APIVersionV1,
		Error: ErrorDetail{
			Code:    code,
			Message: message,
			Details: details,
		},
	}
	api.writeJSON(w, statusCode, errResp)
}

func (api *AgentWorkflowAPI) buildAgentWorkflowsResponse() *AgentWorkflowsResponse {
	db, err := storage.GetMySQLStorage()
	if err != nil {
		logger.Errorf("[AgentWorkflow] MySQL storage not available: %v", err)
		return &AgentWorkflowsResponse{
			APIVersion: APIVersionV1,
			Timestamp:  time.Now().UTC().Format(time.RFC3339),
			Agents:     []AgentWorkflowDetail{},
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	workflows, err := db.GetAgentWorkflows(ctx)
	if err != nil {
		logger.Errorf("[AgentWorkflow] Failed to get agent workflows from DB: %v", err)
		return &AgentWorkflowsResponse{
			APIVersion: APIVersionV1,
			Timestamp:  time.Now().UTC().Format(time.RFC3339),
			Agents:     []AgentWorkflowDetail{},
		}
	}

	agents := make([]AgentWorkflowDetail, 0, len(workflows))
	for _, wf := range workflows {
		agent := convertWorkflowToAgentDetail(wf)
		agents = append(agents, agent)
	}

	return &AgentWorkflowsResponse{
		APIVersion: APIVersionV1,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Agents:     agents,
	}
}

func convertWorkflowToAgentDetail(wf storage.WorkflowDefinition) AgentWorkflowDetail {
	nodes := make([]NodeDefinition, 0, len(wf.Nodes))
	for _, n := range wf.Nodes {
		nodes = append(nodes, NodeDefinition{
			ID:         n.ID,
			Type:       n.Type,
			Config:     n.Config,
			AgentID:    n.AgentID,
			TaskType:   n.TaskType,
			Condition:  n.Condition,
			PreInput:   n.PreInput, // Mapping preInput from workflow API detail conversion
			LoopConfig: convertLoopConfig(n.LoopConfig),
			Metadata:   n.Metadata,
		})
	}

	edges := make([]EdgeDefinition, 0, len(wf.Edges))
	for _, e := range wf.Edges {
		edges = append(edges, EdgeDefinition{
			From:    e.From,
			To:      e.To,
			Label:   e.Label,
			Mapping: convertMapping(e.Mapping),
		})
	}

	return AgentWorkflowDetail{
		ID:          wf.WorkflowID,
		Name:        wf.Name,
		Type:        "agent",
		Description: wf.Description,
		Version:     "1.0.0",
		ExecutionOrder: ExecutionOrder{
			StartNodeID: wf.StartNodeID,
		},
		Nodes:    nodes,
		Edges:    edges,
		Metadata: AgentMetadata{},
	}
}

func convertLoopConfig(m map[string]interface{}) *LoopConfig {
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

func convertMapping(m map[string]interface{}) map[string]string {
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

func InitAgentWorkflows() error {
	db, err := storage.GetMySQLStorage()
	if err != nil {
		return fmt.Errorf("mysql storage not initialized: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	agentWorkflows := []struct {
		id          string
		name        string
		description string
		buildFunc   func() storage.WorkflowDefinition
	}{
		{"host", "Host Agent", "主控 Agent，负责路由决策和调用下游 Agent", buildHostWorkflowDef},
		{"deepresearch", "Deep Research Agent", "深度研究 Agent，使用 Tavily 进行深度检索并整理答案", buildDeepResearchWorkflowDef},
		{"urlreader", "URL Reader Agent", "URL 读取 Agent，使用本地 Fetch MCP 读取网页内容并生成回答", buildURLReaderWorkflowDef},
		{"lbshelper", "LBS Helper Agent", "位置服务 Agent，仅使用 AMap 规划行程", buildLBSHelperWorkflowDef},
		{"schedulehelper", "Schedule Helper Agent", "日程规划 Agent，输出任务优先级和时间安排建议", buildScheduleHelperWorkflowDef},
		{"financehelper", "Finance Helper Agent", "财务助理 Agent，支持记账、财务报告、财经资讯整理与理财建议", buildFinanceHelperWorkflowDef},
		{"memoreminder", "备忘录提醒 Agent", "结构化记录并定时弹窗提醒的备忘录 Agent", buildMemoReminderWorkflowDef},
	}

	for _, aw := range agentWorkflows {
		def := aw.buildFunc()
		def.WorkflowID = aw.id
		def.UserID = "system"
		def.Name = aw.name
		def.Description = aw.description

		logger.Infof("[InitAgentWorkflows] Upserting workflow: %s", aw.id)
		if err := db.SaveWorkflow(ctx, &def); err != nil {
			logger.Errorf("[InitAgentWorkflows] Failed to upsert workflow %s: %v", aw.id, err)
		} else {
			logger.Infof("[InitAgentWorkflows] Successfully upserted workflow: %s", aw.id)
		}
	}

	return nil
}

func buildHostAgentWorkflow() AgentWorkflowDetail {
	return AgentWorkflowDetail{
		ID:          "host",
		Name:        "Host Agent",
		Type:        "orchestrator",
		Description: "主控 Agent，负责路由决策和调用下游 Agent",
		Version:     "1.0.0",
		Configuration: AgentConfiguration{
			Timeout: 600,
			RetryPolicy: RetryPolicy{
				MaxAttempts:       3,
				InitialDelayMs:    200,
				MaxDelayMs:        5000,
				BackoffMultiplier: 2.0,
			},
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"text": map[string]interface{}{
						"type":        "string",
						"description": "用户输入文本",
					},
					"user_id": map[string]interface{}{
						"type":        "string",
						"description": "用户 ID",
					},
				},
				"required": []string{"text"},
			},
			OutputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"response": map[string]interface{}{
						"type":        "string",
						"description": "最终响应",
					},
					"agent_name": map[string]interface{}{
						"type":        "string",
						"description": "调用的下游 Agent 名称",
					},
				},
			},
			EnvironmentVars: []EnvironmentVar{
				{Name: "LLM_URL", Required: true, Description: "大模型 API URL"},
				{Name: "LLM_API_KEY", Required: true, Description: "大模型 API Key"},
			},
		},
		Dependencies: []AgentDependency{
			{AgentID: "deepresearch", Type: "downstream", Required: false, Description: "深度研究 Agent"},
			{AgentID: "urlreader", Type: "downstream", Required: false, Description: "URL 读取 Agent"},
			{AgentID: "lbshelper", Type: "downstream", Required: false, Description: "位置服务 Agent"},
		},
		ExecutionOrder: ExecutionOrder{
			StartNodeID: "start",
			Sequence:    []string{"start", "agent_info", "chat_model", "condition", "call_agent", "direct_answer", "end"},
			Parallel:    nil,
		},
		Nodes: []NodeDefinition{
			{ID: "start", Type: "start", Metadata: map[string]string{"ui.label": "开始", "ui.x": "120", "ui.y": "120"}},
			{ID: "agent_info", Type: "tool", Config: map[string]interface{}{"tool_name": "agent_info"}, Metadata: map[string]string{"ui.label": "获取可调用 Agent", "ui.x": "300", "ui.y": "120", "ui.agent": "host"}},
			{ID: "chat_model", Type: "chat_model", PreInput: "你是路由决策器。可调用 agent 列表如下：\n{{agent_info.response}}\n若无需调用下游 agent，只输出 false；若需要调用，只输出 agent 名称本身。用户问题: {{text}}", Config: map[string]interface{}{"output_type": "string"}, Metadata: map[string]string{"ui.label": "路由决策", "ui.x": "520", "ui.y": "120", "ui.agent": "host"}},
			{ID: "condition", Type: "condition", Config: map[string]interface{}{"left_type": "path", "left_value": "chat_model.response", "operator": "eq", "right_type": "const", "right_value": "false"}, Metadata: map[string]string{"ui.label": "是否直接回答", "ui.x": "700", "ui.y": "120", "ui.agent": "host"}},
			{ID: "call_agent", Type: "tool", Config: map[string]interface{}{"tool_name": "call_agent", "input_mapping": map[string]interface{}{"agent_name": "chat_model.response", "text": "text", "task_id": "task_id", "user_id": "user_id"}, "params": map[string]interface{}{"allowed_agents": []interface{}{"deepresearch", "urlreader", "lbshelper", "schedulehelper", "financehelper", "wellnesscoach"}}}, Metadata: map[string]string{"ui.label": "调用下游 Agent", "ui.x": "760", "ui.y": "60", "ui.agent": "host"}},
			{ID: "direct_answer", Type: "chat_model", PreInput: "请基于可调用 agent 列表直接回答用户问题。可调用 agent 列表：\n{{agent_info.response}}\n用户问题: {{text}}", Config: map[string]interface{}{"output_type": "string"}, Metadata: map[string]string{"ui.label": "直接回答", "ui.x": "760", "ui.y": "180", "ui.agent": "host"}},
			{ID: "end", Type: "end", Metadata: map[string]string{"ui.label": "结束", "ui.x": "980", "ui.y": "120"}},
		},
		Edges: []EdgeDefinition{
			{From: "start", To: "agent_info"},
			{From: "agent_info", To: "chat_model"},
			{From: "chat_model", To: "condition"},
			{From: "condition", To: "direct_answer", Label: "true"},
			{From: "condition", To: "call_agent", Label: "false"},
			{From: "call_agent", To: "end"},
			{From: "direct_answer", To: "end"},
		},
		Metadata: AgentMetadata{
			CreatedAt: "2024-01-01T00:00:00Z",
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			Author:    "system",
			Tags:      []string{"orchestrator", "router", "main"},
			Labels:    map[string]string{"tier": "primary", "visibility": "public"},
		},
	}
}

func buildDeepResearchAgentWorkflow() AgentWorkflowDetail {
	return AgentWorkflowDetail{
		ID:          "deepresearch",
		Name:        "Deep Research Agent",
		Type:        "worker",
		Description: "深度研究 Agent，使用 Tavily 进行深度检索并整理答案",
		Version:     "1.0.0",
		Configuration: AgentConfiguration{
			Timeout: 600,
			RetryPolicy: RetryPolicy{
				MaxAttempts:       3,
				InitialDelayMs:    200,
				MaxDelayMs:        5000,
				BackoffMultiplier: 2.0,
			},
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "研究查询",
					},
				},
				"required": []string{"query"},
			},
			OutputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"response": map[string]interface{}{
						"type":        "string",
						"description": "研究结果",
					},
					"evidence": map[string]interface{}{
						"type":        "array",
						"description": "证据列表",
					},
				},
			},
			EnvironmentVars: []EnvironmentVar{
				{Name: "TAVILY_API_KEY", Required: true, Description: "Tavily API Key"},
				{Name: "LLM_URL", Required: true, Description: "大模型 API URL"},
				{Name: "LLM_API_KEY", Required: true, Description: "大模型 API Key"},
			},
		},
		Dependencies: []AgentDependency{
			{AgentID: "tavily", Type: "external_service", Required: true, Description: "Tavily 搜索服务"},
		},
		ExecutionOrder: ExecutionOrder{
			StartNodeID: "start",
			Sequence:    []string{"start", "loop", "judge_satisfied", "condition", "extract_query", "tavily_search", "end"},
		},
		Nodes: []NodeDefinition{
			{ID: "start", Type: "start", Metadata: map[string]string{"ui.label": "开始", "ui.x": "120", "ui.y": "120"}},
			{ID: "loop", Type: "loop", Config: map[string]interface{}{"max_iterations": 5}, LoopConfig: &LoopConfig{MaxIterations: 5, ContinueTo: "judge_satisfied", ExitTo: "end"}, Metadata: map[string]string{"ui.label": "循环控制", "ui.x": "300", "ui.y": "120", "ui.agent": "deepresearch"}},
			{ID: "judge_satisfied", Type: "chat_model", PreInput: "根据当前检索结果判断是否已经足够回答用户问题。仅输出 true 或 false。问题: {{query}}；当前结果: {{tavily_search.response}}", Config: map[string]interface{}{"output_type": "bool"}, Metadata: map[string]string{"ui.label": "是否满足", "ui.x": "500", "ui.y": "60", "ui.agent": "deepresearch"}},
			{ID: "condition", Type: "condition", Config: map[string]interface{}{"left_type": "path", "left_value": "judge_satisfied.response", "operator": "eq", "right_type": "bool", "right_value": true}, Metadata: map[string]string{"ui.label": "满足判断", "ui.x": "680", "ui.y": "60", "ui.agent": "deepresearch"}},
			{ID: "extract_query", Type: "chat_model", PreInput: "请基于用户问题与当前检索结果，提取一个新的检索关键词。只输出关键词。问题: {{query}}；当前结果: {{tavily_search.response}}", Config: map[string]interface{}{"output_type": "string"}, Metadata: map[string]string{"ui.label": "提取新关键词", "ui.x": "860", "ui.y": "150", "ui.agent": "deepresearch"}},
			{ID: "tavily_search", Type: "tool", AgentID: "deepresearch", TaskType: "tavily_search", Config: map[string]interface{}{"input_mapping": map[string]interface{}{"query": "extract_query.response"}}, Metadata: map[string]string{"ui.label": "Tavily 检索", "ui.x": "1040", "ui.y": "150", "ui.agent": "deepresearch"}},
			{ID: "end", Type: "end", Metadata: map[string]string{"ui.label": "结束", "ui.x": "1240", "ui.y": "120"}},
		},
		Edges: []EdgeDefinition{
			{From: "start", To: "loop"},
			{From: "loop", To: "judge_satisfied", Label: "body"},
			{From: "judge_satisfied", To: "condition"},
			{From: "condition", To: "loop", Label: "true"},
			{From: "condition", To: "extract_query", Label: "false"},
			{From: "extract_query", To: "tavily_search"},
			{From: "tavily_search", To: "loop", Label: "loop"},
			{From: "loop", To: "end", Label: "exit"},
		},
		Metadata: AgentMetadata{
			CreatedAt: "2024-01-01T00:00:00Z",
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			Author:    "system",
			Tags:      []string{"research", "search", "tavily"},
			Labels:    map[string]string{"tier": "worker", "visibility": "public"},
		},
	}
}

func buildURLReaderAgentWorkflow() AgentWorkflowDetail {
	return AgentWorkflowDetail{
		ID:          "urlreader",
		Name:        "URL Reader Agent",
		Type:        "worker",
		Description: "URL 读取 Agent，使用本地 Fetch MCP 读取网页内容并生成回答",
		Version:     "1.0.0",
		Configuration: AgentConfiguration{
			Timeout: 600,
			RetryPolicy: RetryPolicy{
				MaxAttempts:       3,
				InitialDelayMs:    200,
				MaxDelayMs:        5000,
				BackoffMultiplier: 2.0,
			},
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"text": map[string]interface{}{
						"type":        "string",
						"description": "包含 URL 的文本",
					},
				},
				"required": []string{"text"},
			},
			OutputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"response": map[string]interface{}{
						"type":        "string",
						"description": "网页内容摘要",
					},
				},
			},
			EnvironmentVars: []EnvironmentVar{
				{Name: "LLM_URL", Required: true, Description: "大模型 API URL"},
				{Name: "LLM_API_KEY", Required: true, Description: "大模型 API Key"},
			},
		},
		Dependencies: []AgentDependency{
			{AgentID: "fetch", Type: "external_service", Required: true, Description: "本地 Fetch MCP"},
		},
		ExecutionOrder: ExecutionOrder{
			StartNodeID: "start",
			Sequence:    []string{"start", "extract_url", "fetch_read", "organize", "end"},
		},
		Nodes: []NodeDefinition{
			{ID: "start", Type: "start", Metadata: map[string]string{"ui.label": "开始", "ui.x": "120", "ui.y": "120"}},
			{ID: "extract_url", Type: "chat_model", PreInput: "从用户问题中提取 URL，仅输出 URL 本身。用户问题: {{text}}", Config: map[string]interface{}{"output_type": "string"}, Metadata: map[string]string{"ui.label": "提取URL", "ui.x": "300", "ui.y": "120", "ui.agent": "urlreader"}},
			{ID: "fetch_read", Type: "tool", AgentID: "urlreader", TaskType: "fetch_read", Config: map[string]interface{}{"tool_name": "fetch", "input_mapping": map[string]interface{}{"url": "extract_url.response"}}, Metadata: map[string]string{"ui.label": "Fetch 读取网页", "ui.x": "500", "ui.y": "120", "ui.agent": "urlreader"}},
			{ID: "organize", Type: "chat_model", PreInput: "请整理并总结网页内容，回答用户问题。用户问题: {{text}}；网页内容: {{fetch_read.response}}", Config: map[string]interface{}{"output_type": "string"}, Metadata: map[string]string{"ui.label": "整理回答", "ui.x": "700", "ui.y": "120", "ui.agent": "urlreader"}},
			{ID: "end", Type: "end", Metadata: map[string]string{"ui.label": "结束", "ui.x": "920", "ui.y": "120"}},
		},
		Edges: []EdgeDefinition{
			{From: "start", To: "extract_url"},
			{From: "extract_url", To: "fetch_read"},
			{From: "fetch_read", To: "organize"},
			{From: "organize", To: "end"},
		},
		Metadata: AgentMetadata{
			CreatedAt: "2024-01-01T00:00:00Z",
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			Author:    "system",
			Tags:      []string{"url", "reader", "fetch"},
			Labels:    map[string]string{"tier": "worker", "visibility": "public"},
		},
	}
}

func buildLBSHelperAgentWorkflow() AgentWorkflowDetail {
	return AgentWorkflowDetail{
		ID:          "lbshelper",
		Name:        "LBS Helper Agent",
		Type:        "worker",
		Description: "位置服务 Agent，仅使用 AMap 规划行程",
		Version:     "1.0.0",
		Configuration: AgentConfiguration{
			Timeout: 600,
			RetryPolicy: RetryPolicy{
				MaxAttempts:       3,
				InitialDelayMs:    200,
				MaxDelayMs:        5000,
				BackoffMultiplier: 2.0,
			},
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "位置相关查询",
					},
				},
				"required": []string{"query"},
			},
			OutputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"response": map[string]interface{}{
						"type":        "string",
						"description": "行程规划结果",
					},
				},
			},
			EnvironmentVars: []EnvironmentVar{
				{Name: "AMAP_SERVER_URL", Required: true, Description: "AMap MCP 服务 URL"},
				{Name: "LLM_URL", Required: true, Description: "大模型 API URL"},
				{Name: "LLM_API_KEY", Required: true, Description: "大模型 API Key"},
			},
		},
		Dependencies: []AgentDependency{
			{AgentID: "amap", Type: "external_service", Required: true, Description: "AMap MCP 服务"},
		},
		ExecutionOrder: ExecutionOrder{
			StartNodeID: "start",
			Sequence:    []string{"start", "extract_route", "amap_query", "organize", "end"},
			Parallel:    nil,
		},
		Nodes: []NodeDefinition{
			{ID: "start", Type: "start", Metadata: map[string]string{"ui.label": "开始", "ui.x": "120", "ui.y": "120"}},
			{ID: "extract_route", Type: "chat_model", PreInput: "从用户问题中提取路径规划文本（起点、终点、途经点、出行方式）。仅输出可用于地图查询的文本。用户问题: {{query}}", Config: map[string]interface{}{"output_type": "string"}, Metadata: map[string]string{"ui.label": "提取路径规划文本", "ui.x": "300", "ui.y": "120", "ui.agent": "lbshelper"}},
			{ID: "amap_query", Type: "tool", AgentID: "lbshelper", TaskType: "amap_query", Config: map[string]interface{}{"tool_name": "amap", "input_mapping": map[string]interface{}{"query": "extract_route.response"}, "params": map[string]interface{}{"tool_name": "route_plan"}}, Metadata: map[string]string{"ui.label": "AMap 查询行程", "ui.x": "520", "ui.y": "120", "ui.agent": "lbshelper"}},
			{ID: "organize", Type: "chat_model", PreInput: "请基于地图结果整理并回答用户问题。用户问题: {{query}}；地图结果: {{amap_query.response}}", Config: map[string]interface{}{"output_type": "string"}, Metadata: map[string]string{"ui.label": "整理回答", "ui.x": "740", "ui.y": "120", "ui.agent": "lbshelper"}},
			{ID: "end", Type: "end", Metadata: map[string]string{"ui.label": "结束", "ui.x": "760", "ui.y": "120"}},
		},
		Edges: []EdgeDefinition{
			{From: "start", To: "extract_route"},
			{From: "extract_route", To: "amap_query"},
			{From: "amap_query", To: "organize"},
			{From: "organize", To: "end"},
		},
		Metadata: AgentMetadata{
			CreatedAt: "2024-01-01T00:00:00Z",
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			Author:    "system",
			Tags:      []string{"lbs", "location", "amap"},
			Labels:    map[string]string{"tier": "worker", "visibility": "public"},
		},
	}
}

func buildHostWorkflowDef() storage.WorkflowDefinition {
	return storage.WorkflowDefinition{
		StartNodeID: "start",
		Nodes: []storage.NodeDef{
			{ID: "start", Type: "start", Metadata: map[string]string{"ui.label": "开始", "ui.x": "120", "ui.y": "120"}},
			{ID: "agent_info", Type: "tool", Config: map[string]interface{}{"tool_name": "agent_info"}, Metadata: map[string]string{"ui.label": "获取可调用 Agent", "ui.x": "300", "ui.y": "120", "ui.agent": "host"}},
			{ID: "chat_model", Type: "chat_model", PreInput: "你是路由决策器。可调用 agent 列表如下：\n{{agent_info.response}}\n若无需调用下游 agent，只输出 false；若需要调用，只输出 agent 名称本身。用户问题: {{text}}", Config: map[string]interface{}{"output_type": "string"}, Metadata: map[string]string{"ui.label": "路由决策", "ui.x": "520", "ui.y": "120", "ui.agent": "host"}},
			{ID: "condition", Type: "condition", Config: map[string]interface{}{"left_type": "path", "left_value": "chat_model.response", "operator": "eq", "right_type": "const", "right_value": "false"}, Metadata: map[string]string{"ui.label": "是否直接回答", "ui.x": "700", "ui.y": "120", "ui.agent": "host"}},
			{ID: "call_agent", Type: "tool", Config: map[string]interface{}{"tool_name": "call_agent", "input_mapping": map[string]interface{}{"agent_name": "chat_model.response", "text": "text", "task_id": "task_id", "user_id": "user_id"}, "params": map[string]interface{}{"allowed_agents": []interface{}{"deepresearch", "urlreader", "lbshelper", "schedulehelper", "financehelper", "wellnesscoach"}}}, Metadata: map[string]string{"ui.label": "调用下游 Agent", "ui.x": "760", "ui.y": "60", "ui.agent": "host"}},
			{ID: "direct_answer", Type: "chat_model", PreInput: "请基于可调用 agent 列表直接回答用户问题。可调用 agent 列表：\n{{agent_info.response}}\n用户问题: {{text}}", Config: map[string]interface{}{"output_type": "string"}, Metadata: map[string]string{"ui.label": "直接回答", "ui.x": "760", "ui.y": "180", "ui.agent": "host"}},
			{ID: "end", Type: "end", Metadata: map[string]string{"ui.label": "结束", "ui.x": "980", "ui.y": "120"}},
		},
		Edges: []storage.EdgeDef{
			{From: "start", To: "agent_info"},
			{From: "agent_info", To: "chat_model"},
			{From: "chat_model", To: "condition"},
			{From: "condition", To: "direct_answer", Label: "true"},
			{From: "condition", To: "call_agent", Label: "false"},
			{From: "call_agent", To: "end"},
			{From: "direct_answer", To: "end"},
		},
	}
}

func buildDeepResearchWorkflowDef() storage.WorkflowDefinition {
	return storage.WorkflowDefinition{
		StartNodeID: "start",
		Nodes: []storage.NodeDef{
			{ID: "start", Type: "start", Metadata: map[string]string{"ui.label": "开始", "ui.x": "120", "ui.y": "120"}},
			{ID: "loop", Type: "loop", Config: map[string]interface{}{"max_iterations": 5}, LoopConfig: map[string]interface{}{"max_iterations": 5, "continue_to": "judge_satisfied", "exit_to": "end"}, Metadata: map[string]string{"ui.label": "循环控制", "ui.x": "300", "ui.y": "120", "ui.agent": "deepresearch"}},
			{ID: "judge_satisfied", Type: "chat_model", PreInput: "根据当前检索结果判断是否已经足够回答用户问题。仅输出 true 或 false。问题: {{query}}；当前结果: {{tavily_search.response}}", Config: map[string]interface{}{"output_type": "bool"}, Metadata: map[string]string{"ui.label": "是否满足", "ui.x": "500", "ui.y": "60", "ui.agent": "deepresearch"}},
			{ID: "condition", Type: "condition", Config: map[string]interface{}{"left_type": "path", "left_value": "judge_satisfied.response", "operator": "eq", "right_type": "bool", "right_value": true}, Metadata: map[string]string{"ui.label": "满足判断", "ui.x": "680", "ui.y": "60", "ui.agent": "deepresearch"}},
			{ID: "extract_query", Type: "chat_model", PreInput: "请基于用户问题与当前检索结果，提取一个新的检索关键词。只输出关键词。问题: {{query}}；当前结果: {{tavily_search.response}}", Config: map[string]interface{}{"output_type": "string"}, Metadata: map[string]string{"ui.label": "提取新关键词", "ui.x": "860", "ui.y": "150", "ui.agent": "deepresearch"}},
			{ID: "tavily_search", Type: "tool", AgentID: "deepresearch", TaskType: "tavily_search", Config: map[string]interface{}{"input_mapping": map[string]interface{}{"query": "extract_query.response"}}, Metadata: map[string]string{"ui.label": "Tavily 检索", "ui.x": "1040", "ui.y": "150", "ui.agent": "deepresearch"}},
			{ID: "end", Type: "end", Metadata: map[string]string{"ui.label": "结束", "ui.x": "1240", "ui.y": "120"}},
		},
		Edges: []storage.EdgeDef{
			{From: "start", To: "loop"},
			{From: "loop", To: "judge_satisfied", Label: "body"},
			{From: "judge_satisfied", To: "condition"},
			{From: "condition", To: "loop", Label: "true"},
			{From: "condition", To: "extract_query", Label: "false"},
			{From: "extract_query", To: "tavily_search"},
			{From: "tavily_search", To: "loop", Label: "loop"},
			{From: "loop", To: "end", Label: "exit"},
		},
	}
}

func buildURLReaderWorkflowDef() storage.WorkflowDefinition {
	return storage.WorkflowDefinition{
		StartNodeID: "start",
		Nodes: []storage.NodeDef{
			{ID: "start", Type: "start", Metadata: map[string]string{"ui.label": "开始", "ui.x": "120", "ui.y": "120"}},
			{ID: "extract_url", Type: "chat_model", PreInput: "从用户问题中提取 URL，仅输出 URL 本身。用户问题: {{text}}", Config: map[string]interface{}{"output_type": "string"}, Metadata: map[string]string{"ui.label": "提取URL", "ui.x": "300", "ui.y": "120", "ui.agent": "urlreader"}},
			{ID: "fetch_read", Type: "tool", AgentID: "urlreader", TaskType: "fetch_read", Config: map[string]interface{}{"tool_name": "fetch", "input_mapping": map[string]interface{}{"url": "extract_url.response"}}, Metadata: map[string]string{"ui.label": "Fetch 读取网页", "ui.x": "500", "ui.y": "120", "ui.agent": "urlreader"}},
			{ID: "organize", Type: "chat_model", PreInput: "请整理并总结网页内容，回答用户问题。用户问题: {{text}}；网页内容: {{fetch_read.response}}", Config: map[string]interface{}{"output_type": "string"}, Metadata: map[string]string{"ui.label": "整理回答", "ui.x": "700", "ui.y": "120", "ui.agent": "urlreader"}},
			{ID: "end", Type: "end", Metadata: map[string]string{"ui.label": "结束", "ui.x": "920", "ui.y": "120"}},
		},
		Edges: []storage.EdgeDef{
			{From: "start", To: "extract_url"},
			{From: "extract_url", To: "fetch_read"},
			{From: "fetch_read", To: "organize"},
			{From: "organize", To: "end"},
		},
	}
}

func buildLBSHelperWorkflowDef() storage.WorkflowDefinition {
	return storage.WorkflowDefinition{
		StartNodeID: "start",
		Nodes: []storage.NodeDef{
			{ID: "start", Type: "start", Metadata: map[string]string{"ui.label": "开始", "ui.x": "120", "ui.y": "120"}},
			{ID: "extract_route", Type: "chat_model", PreInput: "从用户问题中提取路径规划文本（起点、终点、途经点、出行方式）。仅输出可用于地图查询的文本。用户问题: {{query}}", Config: map[string]interface{}{"output_type": "string"}, Metadata: map[string]string{"ui.label": "提取路径规划文本", "ui.x": "300", "ui.y": "120", "ui.agent": "lbshelper"}},
			{ID: "amap_query", Type: "tool", AgentID: "lbshelper", TaskType: "amap_query", Config: map[string]interface{}{"tool_name": "amap", "input_mapping": map[string]interface{}{"query": "extract_route.response"}, "params": map[string]interface{}{"tool_name": "route_plan"}}, Metadata: map[string]string{"ui.label": "AMap 查询行程", "ui.x": "520", "ui.y": "120", "ui.agent": "lbshelper"}},
			{ID: "organize", Type: "chat_model", PreInput: "请基于地图结果整理并回答用户问题。用户问题: {{query}}；地图结果: {{amap_query.response}}", Config: map[string]interface{}{"output_type": "string"}, Metadata: map[string]string{"ui.label": "整理回答", "ui.x": "740", "ui.y": "120", "ui.agent": "lbshelper"}},
			{ID: "end", Type: "end", Metadata: map[string]string{"ui.label": "结束", "ui.x": "760", "ui.y": "120"}},
		},
		Edges: []storage.EdgeDef{
			{From: "start", To: "extract_route"},
			{From: "extract_route", To: "amap_query"},
			{From: "amap_query", To: "organize"},
			{From: "organize", To: "end"},
		},
	}
}

func buildScheduleHelperWorkflowDef() storage.WorkflowDefinition {
	return storage.WorkflowDefinition{
		StartNodeID: "start",
		Nodes: []storage.NodeDef{
			{ID: "start", Type: "start", Metadata: map[string]string{"ui.label": "开始", "ui.x": "120", "ui.y": "120"}},
			{ID: "plan", Type: "chat_model", PreInput: "你是日程规划助手。请根据用户输入生成清晰可执行的日程安排（优先级、时间块、提醒点）。用户输入: {{text}}", Config: map[string]interface{}{"output_type": "string"}, Metadata: map[string]string{"ui.label": "生成计划", "ui.x": "320", "ui.y": "120", "ui.agent": "schedulehelper"}},
			{ID: "refine", Type: "chat_model", PreInput: "请优化当前日程方案，补充风险提醒、备用计划和可量化目标。当前方案: {{plan.response}}", Config: map[string]interface{}{"output_type": "string"}, Metadata: map[string]string{"ui.label": "计划优化", "ui.x": "540", "ui.y": "120", "ui.agent": "schedulehelper"}},
			{ID: "end", Type: "end", Metadata: map[string]string{"ui.label": "结束", "ui.x": "760", "ui.y": "120"}},
		},
		Edges: []storage.EdgeDef{
			{From: "start", To: "plan"},
			{From: "plan", To: "refine"},
			{From: "refine", To: "end"},
		},
	}
}

func buildFinanceHelperWorkflowDef() storage.WorkflowDefinition {
	return storage.WorkflowDefinition{
		StartNodeID: "N_start",
		Nodes: []storage.NodeDef{
			{ID: "N_start", Type: "start", Metadata: map[string]string{"ui.label": "开始", "ui.x": "100", "ui.y": "180"}},
			{ID: "N_plan", Type: "chat_model", PreInput: "识别用户请求属于记账、财务报告、财经资讯还是理财建议，并规划后续 SQL 或 AkShare 调用。", Config: map[string]interface{}{"intent": "plan_request"}, Metadata: map[string]string{"ui.label": "规划请求", "ui.x": "280", "ui.y": "180", "ui.agent": "financehelper"}},
			{ID: "N_is_ledger", Type: "condition", Config: map[string]interface{}{"left_type": "path", "left_value": "N_plan.action", "operator": "eq", "right_type": "value", "right_value": "ledger"}, Metadata: map[string]string{"ui.label": "是否记账", "ui.x": "470", "ui.y": "180", "ui.agent": "financehelper"}},
			{ID: "N_mysql_ledger", Type: "tool", Config: map[string]interface{}{"tool_name": "mysql_exec", "purpose": "ledger"}, Metadata: map[string]string{"ui.label": "写入账单", "ui.x": "660", "ui.y": "80", "ui.agent": "financehelper"}},
			{ID: "N_is_report", Type: "condition", Config: map[string]interface{}{"left_type": "path", "left_value": "N_plan.action", "operator": "eq", "right_type": "value", "right_value": "report"}, Metadata: map[string]string{"ui.label": "是否报告", "ui.x": "660", "ui.y": "180", "ui.agent": "financehelper"}},
			{ID: "N_mysql_report", Type: "tool", Config: map[string]interface{}{"tool_name": "mysql_exec", "purpose": "report"}, Metadata: map[string]string{"ui.label": "查询账单", "ui.x": "860", "ui.y": "120", "ui.agent": "financehelper"}},
			{ID: "N_is_news", Type: "condition", Config: map[string]interface{}{"left_type": "path", "left_value": "N_plan.action", "operator": "eq", "right_type": "value", "right_value": "news"}, Metadata: map[string]string{"ui.label": "是否资讯", "ui.x": "860", "ui.y": "220", "ui.agent": "financehelper"}},
			{ID: "N_akshare_news", Type: "tool", Config: map[string]interface{}{"tool_name": "akshare-one-mcp", "purpose": "news"}, Metadata: map[string]string{"ui.label": "读取资讯", "ui.x": "1060", "ui.y": "180", "ui.agent": "financehelper"}},
			{ID: "N_mysql_advice", Type: "tool", Config: map[string]interface{}{"tool_name": "mysql_exec", "purpose": "advice"}, Metadata: map[string]string{"ui.label": "读取财务情况", "ui.x": "1060", "ui.y": "280", "ui.agent": "financehelper"}},
			{ID: "N_akshare_advice", Type: "tool", Config: map[string]interface{}{"tool_name": "akshare-one-mcp", "purpose": "advice"}, Metadata: map[string]string{"ui.label": "读取市场信息", "ui.x": "1260", "ui.y": "280", "ui.agent": "financehelper"}},
			{ID: "N_respond", Type: "chat_model", PreInput: "基于账单查询结果与金融资讯，输出面向用户的结构化中文回复。", Config: map[string]interface{}{"intent": "final_response"}, Metadata: map[string]string{"ui.label": "整理回复", "ui.x": "1260", "ui.y": "180", "ui.agent": "financehelper"}},
			{ID: "N_end", Type: "end", Metadata: map[string]string{"ui.label": "结束", "ui.x": "1450", "ui.y": "180"}},
		},
		Edges: []storage.EdgeDef{
			{From: "N_start", To: "N_plan"},
			{From: "N_plan", To: "N_is_ledger"},
			{From: "N_is_ledger", To: "N_mysql_ledger", Label: "true"},
			{From: "N_is_ledger", To: "N_is_report", Label: "false"},
			{From: "N_mysql_ledger", To: "N_respond"},
			{From: "N_is_report", To: "N_mysql_report", Label: "true"},
			{From: "N_is_report", To: "N_is_news", Label: "false"},
			{From: "N_mysql_report", To: "N_respond"},
			{From: "N_is_news", To: "N_akshare_news", Label: "true"},
			{From: "N_is_news", To: "N_mysql_advice", Label: "false"},
			{From: "N_akshare_news", To: "N_respond"},
			{From: "N_mysql_advice", To: "N_akshare_advice"},
			{From: "N_akshare_advice", To: "N_respond"},
			{From: "N_respond", To: "N_end"},
		},
	}
}

func buildWellnessCoachWorkflowDef() storage.WorkflowDefinition {
	return storage.WorkflowDefinition{
		StartNodeID: "start",
		Nodes: []storage.NodeDef{
			{ID: "start", Type: "start", Metadata: map[string]string{"ui.label": "开始", "ui.x": "120", "ui.y": "120"}},
			{ID: "assess", Type: "chat_model", PreInput: "你是健康评估助手。请识别用户在作息、运动、饮食上的风险点。用户输入: {{text}}", Config: map[string]interface{}{"output_type": "string"}, Metadata: map[string]string{"ui.label": "状态评估", "ui.x": "320", "ui.y": "120", "ui.agent": "wellnesscoach"}},
			{ID: "plan", Type: "chat_model", PreInput: "请基于健康评估结果输出未来 7 天可执行改进计划。评估结果: {{assess.response}}", Config: map[string]interface{}{"output_type": "string"}, Metadata: map[string]string{"ui.label": "健康计划", "ui.x": "540", "ui.y": "120", "ui.agent": "wellnesscoach"}},
			{ID: "end", Type: "end", Metadata: map[string]string{"ui.label": "结束", "ui.x": "760", "ui.y": "120"}},
		},
		Edges: []storage.EdgeDef{
			{From: "start", To: "assess"},
			{From: "assess", To: "plan"},
			{From: "plan", To: "end"},
		},
	}
}
