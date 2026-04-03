package orchestrator

import (
	"ai/config"
	"ai/pkg/authz"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"ai/pkg/logger"
	"ai/pkg/storage"
	"ai/pkg/tools"
)

type UserToolAPI struct {
	storage *storage.MySQLStorage
}

func NewUserToolAPI(mysqlStorage *storage.MySQLStorage) *UserToolAPI {
	api := &UserToolAPI{
		storage: mysqlStorage,
	}
	if api.storage != nil {
		if err := api.seedBuiltInTools(context.Background()); err != nil {
			logger.Warnf("[UserToolAPI] seed built-in tools failed: %v", err)
		}
		go api.warmupStdioMCPTools()
	}
	return api
}

func (api *UserToolAPI) warmupStdioMCPTools() {
	if api.storage == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	defs, err := api.storage.ListUserTools(ctx, "")
	if err != nil {
		logger.Warnf("[UserToolAPI] warmup stdio tools list failed: %v", err)
		return
	}

	manager := tools.GetStdioMCPManager()
	for _, def := range defs {
		if strings.TrimSpace(def.ToolType) != string(tools.ToolTypeMCP) {
			continue
		}
		if getMCPMode(def.Config) != "stdio" {
			continue
		}
		serverName, serverCfg, parseErr := extractMCPStdioServer(def.Config)
		if parseErr != nil {
			logger.Warnf("[UserToolAPI] warmup parse config failed tool_id=%s err=%v", def.ToolID, parseErr)
			continue
		}
		command, args, resolveErr := tools.EnsureUvToolInstalled(ctx, serverCfg.Command, serverCfg.Args)
		if resolveErr != nil {
			logger.Warnf("[UserToolAPI] warmup resolve tool failed tool_id=%s err=%v", def.ToolID, resolveErr)
			continue
		}
		if _, startErr := manager.Start(ctx, def.ToolID, command, args); startErr != nil {
			logger.Warnf("[UserToolAPI] warmup start failed tool_id=%s server=%s err=%v", def.ToolID, serverName, startErr)
			continue
		}
		logger.Infof("[UserToolAPI] warmup started tool_id=%s server=%s", def.ToolID, serverName)
	}
}

func (api *UserToolAPI) seedBuiltInTools(ctx context.Context) error {
	amapServerURL := ""
	mysqlDSN := ""
	if cfg := config.GetMainConfig(); cfg != nil {
		amapServerURL = strings.TrimSpace(cfg.AMap.ServerURL)
		mysqlDSN = strings.TrimSpace(cfg.MySQL.DSN)
	}

	builtins := []storage.UserToolDefinition{
		{
			ToolID:      "agent_info",
			UserID:      "system",
			Name:        "agent_info",
			Description: "查询当前系统可用的 Agent 列表与能力说明",
			ToolType:    string(tools.ToolTypeHTTP),
			Config: map[string]any{
				"method":  "GET",
				"url":     "http://127.0.0.1:8080/v1/orchestrator/agents",
				"timeout": 30,
			},
			Parameters: []storage.ToolParameterDef{},
			OutputParameters: []storage.ToolParameterDef{
				{Name: "result", Type: "string", Required: true, Description: "HTTP 响应中实际需要的结果内容"},
			},
		},
		{
			ToolID:      "jina_reader",
			UserID:      "system",
			Name:        "jina_reader",
			Description: "通过 Jina Reader 拉取并清洗网页正文内容",
			ToolType:    string(tools.ToolTypeHTTP),
			Config: map[string]any{
				"method":  "GET",
				"url":     "https://r.jina.ai/{{url}}",
				"timeout": 60,
			},
			Parameters: []storage.ToolParameterDef{
				{Name: "url", Type: "string", Required: true, Description: "需要读取的网页地址（http/https）"},
			},
			OutputParameters: []storage.ToolParameterDef{
				{Name: "result", Type: "string", Required: true, Description: "HTTP 响应中实际需要的结果内容"},
			},
		},
		{
			ToolID:      "tavily",
			UserID:      "system",
			Name:        "tavily",
			Description: "调用 Tavily 搜索 API 进行实时检索",
			ToolType:    string(tools.ToolTypeHTTP),
			Config: map[string]any{
				"method": "POST",
				"url":    "https://api.tavily.com/search",
				"headers": map[string]any{
					"Content-Type":  "application/json",
					"Authorization": "Bearer {{api_key}}",
				},
				"body_template": `{"query":"{{query}}","search_depth":"{{search_depth}}","max_results":{{max_results}}}`,
				"timeout":       30,
			},
			Parameters: []storage.ToolParameterDef{
				{Name: "api_key", Type: "string", Required: true, Description: "Tavily API Key"},
				{Name: "query", Type: "string", Required: true, Description: "检索关键词"},
				{Name: "search_depth", Type: "string", Required: false, Description: "检索深度，可选 basic/advanced", Default: "basic", Enum: []any{"basic", "advanced"}},
				{Name: "max_results", Type: "number", Required: false, Description: "返回结果数量上限", Default: 5},
			},
			OutputParameters: []storage.ToolParameterDef{
				{Name: "result", Type: "string", Required: true, Description: "HTTP 响应中实际需要的结果内容"},
			},
		},
		{
			ToolID:      "amap",
			UserID:      "system",
			Name:        "amap",
			Description: "调用 AMap MCP 服务；由 Agent 通过 tool_name 决定具体子工具",
			ToolType:    string(tools.ToolTypeMCP),
			Config: map[string]any{
				"server_url": amapServerURL,
				"tool_name":  "auto",
			},
			Parameters: []storage.ToolParameterDef{
				{Name: "tool_name", Type: "string", Required: true, Description: "要调用的 MCP 子工具名（如 maps_direction_driving）"},
				{Name: "arguments", Type: "object", Required: false, Description: "MCP 子工具参数对象；优先使用此字段作为调用参数"},
				{Name: "query", Type: "string", Required: false, Description: "兼容字段；若未提供 arguments，将 query 等普通参数直接透传"},
			},
			OutputParameters: []storage.ToolParameterDef{
				{Name: "result", Type: "string", Required: true, Description: "MCP 调用返回的主要结果内容"},
			},
		},
		{
			ToolID:      "akshare-one-mcp",
			UserID:      "system",
			Name:        "akshare-one-mcp",
			Description: "本地 AkShare MCP 服务；由 Agent 通过 tool_name 决定具体子工具",
			ToolType:    string(tools.ToolTypeMCP),
			Config: map[string]any{
				"mcp_mode":    "stdio",
				"server_name": "akshare-one-mcp",
				"tool_name":   "auto",
				"mcp_servers": map[string]any{
					"akshare-one-mcp": map[string]any{
						"command": "uvx",
						"args":    []any{"akshare-one-mcp"},
					},
				},
			},
			Parameters: []storage.ToolParameterDef{
				{Name: "tool_name", Type: "string", Required: true, Description: "要调用的 MCP 子工具名"},
				{Name: "arguments", Type: "object", Required: false, Description: "MCP 子工具参数对象；优先使用此字段作为调用参数"},
				{Name: "query", Type: "string", Required: false, Description: "兼容字段；若未提供 arguments，将 query 等普通参数直接透传"},
			},
			OutputParameters: []storage.ToolParameterDef{
				{Name: "result", Type: "string", Required: true, Description: "MCP 调用返回的主要结果内容"},
			},
		},
		{
			ToolID:      "fetch",
			UserID:      "system",
			Name:        "fetch",
			Description: "本地 Fetch MCP 服务；由 Agent 通过 tool_name 决定具体子工具",
			ToolType:    string(tools.ToolTypeMCP),
			Config: map[string]any{
				"mcp_mode":    "stdio",
				"server_name": "fetch",
				"tool_name":   "auto",
				"mcp_servers": map[string]any{
					"fetch": map[string]any{
						"command": "uvx",
						"args":    []any{"mcp-server-fetch", "--ignore-robots-txt"},
					},
				},
			},
			Parameters: []storage.ToolParameterDef{
				{Name: "tool_name", Type: "string", Required: true, Description: "要调用的 MCP 子工具名"},
				{Name: "arguments", Type: "object", Required: false, Description: "MCP 子工具参数对象；优先使用此字段作为调用参数"},
				{Name: "query", Type: "string", Required: false, Description: "兼容字段；若未提供 arguments，将 query 等普通参数直接透传"},
			},
			OutputParameters: []storage.ToolParameterDef{
				{Name: "result", Type: "string", Required: true, Description: "MCP 调用返回的主要结果内容"},
			},
		},
		{
			ToolID:      "mysql_exec",
			UserID:      "system",
			Name:        "mysql_exec",
			Description: "本地 MySQL MCP 服务；执行任意 SQL",
			ToolType:    string(tools.ToolTypeMCP),
			Config: map[string]any{
				"mcp_mode":    "stdio",
				"server_name": "mysql-mcp",
				"tool_name":   "mysql_exec",
				"mcp_servers": map[string]any{
					"mysql-mcp": map[string]any{
						"command": "go",
						"args": []any{
							"run",
							"./tools/mysqlmcp",
							"--dsn",
							mysqlDSN,
						},
					},
				},
			},
			Parameters: []storage.ToolParameterDef{
				{Name: "sql", Type: "string", Required: true, Description: "要执行的 SQL 语句"},
			},
			OutputParameters: []storage.ToolParameterDef{
				{Name: "result", Type: "string", Required: true, Description: "MCP 调用返回的主要结果内容"},
			},
		},
		{
			ToolID:      "json_file",
			UserID:      "system",
			Name:        "json_file",
			Description: "本地 JSON 文件读写 MCP 服务",
			ToolType:    string(tools.ToolTypeMCP),
			Config: map[string]any{
				"mcp_mode":    "stdio",
				"server_name": "json-file-mcp",
				"tool_name":   "json_file",
				"mcp_servers": map[string]any{
					"json-file-mcp": map[string]any{
						"command": "go",
						"args": []any{
							"run",
							"./tools/jsonfilemcp",
							"--root",
							".",
						},
					},
				},
			},
			Parameters: []storage.ToolParameterDef{
				{Name: "action", Type: "string", Required: true, Description: "read 或 write"},
				{Name: "path", Type: "string", Required: true, Description: "JSON 文件路径"},
				{Name: "json", Type: "object", Required: false, Description: "写入时的 JSON 内容，可传对象或 JSON 字符串"},
			},
			OutputParameters: []storage.ToolParameterDef{
				{Name: "result", Type: "string", Required: true, Description: "MCP 调用返回的主要结果内容"},
			},
		},
		{
			ToolID:      "script_exec",
			UserID:      "system",
			Name:        "script_exec",
			Description: "本地脚本执行 MCP 服务",
			ToolType:    string(tools.ToolTypeMCP),
			Config: map[string]any{
				"mcp_mode":    "stdio",
				"server_name": "script-mcp",
				"tool_name":   "script_exec",
				"mcp_servers": map[string]any{
					"script-mcp": map[string]any{
						"command": "go",
						"args": []any{
							"run",
							"./tools/scriptmcp",
						},
					},
				},
			},
			Parameters: []storage.ToolParameterDef{
				{Name: "path", Type: "string", Required: true, Description: "脚本路径或命令"},
				{Name: "args", Type: "array", Required: false, Description: "命令参数数组"},
				{Name: "interpreter", Type: "string", Required: false, Description: "可选解释器，如 powershell/python"},
				{Name: "cwd", Type: "string", Required: false, Description: "执行工作目录"},
				{Name: "timeout_sec", Type: "number", Required: false, Description: "超时时间秒，默认 60"},
			},
			OutputParameters: []storage.ToolParameterDef{
				{Name: "result", Type: "string", Required: true, Description: "MCP 调用返回的主要结果内容"},
			},
		},
	}

	for i := range builtins {
		def := builtins[i]
		if def.ToolID == "amap" {
			api.repairAmapMCPToolConfig(ctx, &def, amapServerURL)
		}
		if err := api.storage.SaveUserTool(ctx, &def); err != nil {
			return err
		}
	}

	return nil
}

func (api *UserToolAPI) repairAmapMCPToolConfig(ctx context.Context, def *storage.UserToolDefinition, fallbackServerURL string) {
	if def.Config == nil {
		def.Config = map[string]any{}
	}

	def.Config["mcp_mode"] = "url"

	configuredToolName := strings.TrimSpace(getStringMap(def.Config, "tool_name", ""))
	if configuredToolName == "" {
		def.Config["tool_name"] = "auto"
	}

	serverURL := strings.TrimSpace(getStringMap(def.Config, "server_url", ""))
	if serverURL == "" {
		serverURL = strings.TrimSpace(fallbackServerURL)
	}

	if existing, err := api.storage.GetUserTool(ctx, def.ToolID); err == nil && existing != nil {
		existingServerURL := strings.TrimSpace(getStringMap(existing.Config, "server_url", ""))
		if serverURL == "" && existingServerURL != "" {
			serverURL = existingServerURL
		}
	}

	def.Config["server_url"] = serverURL
}

func (api *UserToolAPI) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v1/orchestrator/user-tools", api.handleUserTools)
	mux.HandleFunc("/v1/orchestrator/user-tools/", api.handleUserToolByID)
}

func (api *UserToolAPI) handleUserTools(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		api.handleListUserTools(w, r)
	case http.MethodPost:
		api.handleCreateUserTool(w, r)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (api *UserToolAPI) handleUserToolByID(w http.ResponseWriter, r *http.Request) {
	remaining := strings.TrimPrefix(r.URL.Path, "/v1/orchestrator/user-tools/")
	parts := strings.Split(strings.Trim(remaining, "/"), "/")
	if len(parts) >= 2 && parts[1] == "mcp-tools" {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		api.handleListMCPTools(w, r, parts[0])
		return
	}
	if len(parts) >= 2 && parts[1] == "mcp-start" {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		api.handleStartMCPTool(w, r, parts[0])
		return
	}
	if len(parts) >= 2 && parts[1] == "mcp-stop" {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		api.handleStopMCPTool(w, r, parts[0])
		return
	}

	toolID := extractIDFromPath(r.URL.Path, "/v1/orchestrator/user-tools/")
	if toolID == "" {
		http.Error(w, "tool id is required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		api.handleGetUserTool(w, r, toolID)
	case http.MethodPut:
		api.handleUpdateUserTool(w, r, toolID)
	case http.MethodDelete:
		api.handleDeleteUserTool(w, r, toolID)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

type MCPServerTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (api *UserToolAPI) handleListMCPTools(w http.ResponseWriter, r *http.Request, toolID string) {
	ctx := r.Context()
	_, ok := authenticatedUserID(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if api.storage == nil {
		http.Error(w, "storage not available", http.StatusInternalServerError)
		return
	}

	def, err := api.storage.GetUserTool(ctx, toolID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if _, allowed := authorizeResourceAccess(r, "orchestrator.tool.read", authz.ScopeOwn, def.UserID, def.UserID == "system"); !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if strings.TrimSpace(def.ToolType) != string(tools.ToolTypeMCP) {
		http.Error(w, "tool type is not mcp", http.StatusBadRequest)
		return
	}

	mode := getMCPMode(def.Config)
	resp := make([]MCPServerTool, 0)
	if mode == "stdio" {
		manager := tools.GetStdioMCPManager()
		client, ok := manager.Get(toolID)
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}

		listCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		infos, err := listMCPToolsWithClient(listCtx, client)
		if err != nil {
			logger.Warnf("[UserToolAPI] ListMCPTools stdio failed tool_id=%s err=%v", toolID, err)
			manager.Remove(toolID)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}
		for _, info := range infos {
			resp = append(resp, MCPServerTool{Name: info.Name, Description: info.Description})
		}
	} else {
		serverURL := strings.TrimSpace(getStringMap(def.Config, "server_url", ""))
		if serverURL == "" {
			http.Error(w, "mcp server_url is required", http.StatusBadRequest)
			return
		}

		discovery, err := tools.NewMCPToolDiscovery(serverURL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer discovery.Close()

		listCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		infos, err := discovery.ListTools(listCtx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		for _, info := range infos {
			resp = append(resp, MCPServerTool{Name: info.Name, Description: info.Description})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (api *UserToolAPI) handleStartMCPTool(w http.ResponseWriter, r *http.Request, toolID string) {
	ctx := r.Context()
	logger.Infof("[UserToolAPI] StartMCPTool start tool_id=%s", toolID)
	_, ok := authenticatedUserID(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if api.storage == nil {
		http.Error(w, "storage not available", http.StatusInternalServerError)
		return
	}

	def, err := api.storage.GetUserTool(ctx, toolID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if _, allowed := authorizeResourceAccess(r, "orchestrator.tool.read", authz.ScopeOwn, def.UserID, def.UserID == "system"); !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if strings.TrimSpace(def.ToolType) != string(tools.ToolTypeMCP) {
		http.Error(w, "tool type is not mcp", http.StatusBadRequest)
		return
	}
	if getMCPMode(def.Config) != "stdio" {
		http.Error(w, "only stdio mcp can be started", http.StatusBadRequest)
		return
	}

	serverName, serverCfg, err := extractMCPStdioServer(def.Config)
	if err != nil {
		logger.Warnf("[UserToolAPI] StartMCPTool parse config failed tool_id=%s err=%v", toolID, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	logger.Infof("[UserToolAPI] StartMCPTool config tool_id=%s server=%s command=%q args=%v", toolID, serverName, serverCfg.Command, serverCfg.Args)
	command, args, err := tools.EnsureUvToolInstalled(ctx, serverCfg.Command, serverCfg.Args)
	if err != nil {
		logger.Warnf("[UserToolAPI] StartMCPTool uv install failed tool_id=%s err=%v", toolID, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	logger.Infof("[UserToolAPI] StartMCPTool uv ready tool_id=%s command=%q args=%v", toolID, command, args)
	manager := tools.GetStdioMCPManager()
	client, err := manager.Start(ctx, toolID, command, args)
	if err != nil {
		logger.Warnf("[UserToolAPI] StartMCPTool connect failed tool_id=%s err=%v", toolID, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	listCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	infos, err := listMCPToolsWithClient(listCtx, client)
	if err != nil {
		logger.Warnf("[UserToolAPI] StartMCPTool list tools failed tool_id=%s err=%v", toolID, err)
		manager.Remove(toolID)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	logger.Infof("[UserToolAPI] StartMCPTool done tool_id=%s tool_count=%d", toolID, len(infos))

	resp := struct {
		Started bool            `json:"started"`
		Server  string          `json:"server"`
		Tools   []MCPServerTool `json:"tools"`
	}{
		Started: true,
		Server:  serverName,
	}
	for _, info := range infos {
		resp.Tools = append(resp.Tools, MCPServerTool{Name: info.Name, Description: info.Description})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (api *UserToolAPI) handleStopMCPTool(w http.ResponseWriter, r *http.Request, toolID string) {
	ctx := r.Context()
	_, ok := authenticatedUserID(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if api.storage == nil {
		http.Error(w, "storage not available", http.StatusInternalServerError)
		return
	}

	def, err := api.storage.GetUserTool(ctx, toolID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if _, allowed := authorizeResourceAccess(r, "orchestrator.tool.read", authz.ScopeOwn, def.UserID, def.UserID == "system"); !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if strings.TrimSpace(def.ToolType) != string(tools.ToolTypeMCP) {
		http.Error(w, "tool type is not mcp", http.StatusBadRequest)
		return
	}
	if getMCPMode(def.Config) != "stdio" {
		http.Error(w, "only stdio mcp can be stopped", http.StatusBadRequest)
		return
	}

	manager := tools.GetStdioMCPManager()
	manager.Remove(toolID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"stopped": true})
}

func listMCPToolsWithClient(ctx context.Context, client *tools.MCPClient) ([]tools.RemoteToolInfo, error) {
	return client.ListTools(ctx)
}

func (api *UserToolAPI) handleListUserTools(w http.ResponseWriter, r *http.Request) {
	logger.Infof("[UserToolAPI] ListUserTools")

	ctx := r.Context()
	userID, ok := authenticatedUserID(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}

	if api.storage == nil {
		http.Error(w, "storage not available", http.StatusInternalServerError)
		return
	}

	userTools, err := api.storage.ListUserTools(ctx, userID)
	if err != nil {
		logger.Errorf("[UserToolAPI] ListUserTools failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	systemTools, err := api.storage.ListUserTools(ctx, "system")
	if err != nil {
		logger.Errorf("[UserToolAPI] List system tools failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tools := append(systemTools, userTools...)
	if hasAllScopeAccess(r, "orchestrator.tool.read") {
		allTools, allErr := api.storage.ListUserTools(ctx, "")
		if allErr != nil {
			logger.Errorf("[UserToolAPI] List all tools failed: %v", allErr)
			http.Error(w, allErr.Error(), http.StatusInternalServerError)
			return
		}
		tools = allTools
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tools)
}

func (api *UserToolAPI) handleCreateUserTool(w http.ResponseWriter, r *http.Request) {
	logger.Infof("[UserToolAPI] CreateUserTool")

	var req CreateUserToolRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.ToolID == "" {
		http.Error(w, "toolId is required", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if req.ToolType == "" {
		http.Error(w, "toolType is required", http.StatusBadRequest)
		return
	}

	def := &storage.UserToolDefinition{
		ToolID:           req.ToolID,
		UserID:           "",
		Name:             req.Name,
		Description:      req.Description,
		ToolType:         req.ToolType,
		Config:           req.Config,
		Parameters:       convertParameters(req.Parameters),
		OutputParameters: convertParameters(req.OutputParameters),
	}

	ctx := r.Context()
	userID, ok := authenticatedUserID(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if _, allowed := authorizeResourceAccess(r, "orchestrator.tool.manage", authz.ScopeOwn, userID, false); !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	def.UserID = userID
	if api.storage == nil {
		http.Error(w, "storage not available", http.StatusInternalServerError)
		return
	}

	if err := api.storage.SaveUserTool(ctx, def); err != nil {
		logger.Errorf("[UserToolAPI] CreateUserTool failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(def)
}

func (api *UserToolAPI) handleGetUserTool(w http.ResponseWriter, r *http.Request, toolID string) {
	logger.Infof("[UserToolAPI] GetUserTool: %s", toolID)

	ctx := r.Context()

	if api.storage == nil {
		http.Error(w, "storage not available", http.StatusInternalServerError)
		return
	}

	requesterID, _ := authenticatedUserID(r)
	allowAllRead := hasAllScopeAccess(r, "orchestrator.tool.read")
	allowSystemRead := true
	def, err := api.storage.GetUserToolScoped(ctx, toolID, requesterID, allowSystemRead, allowAllRead)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	_, ok := authenticatedUserID(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if _, allowed := authorizeResourceAccess(r, "orchestrator.tool.read", authz.ScopeOwn, def.UserID, def.UserID == "system"); !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(def)
}

func (api *UserToolAPI) handleUpdateUserTool(w http.ResponseWriter, r *http.Request, toolID string) {
	logger.Infof("[UserToolAPI] UpdateUserTool: %s", toolID)

	var req UpdateUserToolRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	if api.storage == nil {
		http.Error(w, "storage not available", http.StatusInternalServerError)
		return
	}

	existing, err := api.storage.GetUserTool(ctx, toolID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	_, ok := authenticatedUserID(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if existing.UserID == "system" {
		if _, allowed := authorizeResourceAccess(r, "orchestrator.tool.system.manage", authz.ScopeAll, "", true); !allowed {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	} else if _, allowed := authorizeResourceAccess(r, "orchestrator.tool.manage", authz.ScopeOwn, existing.UserID, false); !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if req.Name != "" {
		existing.Name = req.Name
	}
	if req.Description != "" {
		existing.Description = req.Description
	}
	if req.Config != nil {
		existing.Config = req.Config
	}
	if req.Parameters != nil {
		existing.Parameters = convertParameters(req.Parameters)
	}
	if req.OutputParameters != nil {
		existing.OutputParameters = convertParameters(req.OutputParameters)
	}

	if err := api.storage.SaveUserTool(ctx, existing); err != nil {
		logger.Errorf("[UserToolAPI] UpdateUserTool failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(existing)
}

func (api *UserToolAPI) handleDeleteUserTool(w http.ResponseWriter, r *http.Request, toolID string) {
	logger.Infof("[UserToolAPI] DeleteUserTool: %s", toolID)

	ctx := r.Context()
	_, ok := authenticatedUserID(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}

	if api.storage == nil {
		http.Error(w, "storage not available", http.StatusInternalServerError)
		return
	}

	existing, err := api.storage.GetUserTool(ctx, toolID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	allowSystemManageAll := hasAllScopeAccess(r, "orchestrator.tool.system.manage")
	if existing.UserID == "system" {
		if _, allowed := authorizeResourceAccess(r, "orchestrator.tool.system.manage", authz.ScopeAll, "", true); !allowed {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	} else if _, allowed := authorizeResourceAccess(r, "orchestrator.tool.manage", authz.ScopeOwn, existing.UserID, false); !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	requesterID, _ := authenticatedUserID(r)
	if err := api.storage.DeleteUserToolScoped(ctx, toolID, requesterID, true, allowSystemManageAll); err != nil {
		logger.Errorf("[UserToolAPI] DeleteUserTool failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"deleted": true})
}

type CreateUserToolRequest struct {
	ToolID           string                 `json:"toolId"`
	Name             string                 `json:"name"`
	Description      string                 `json:"description"`
	ToolType         string                 `json:"toolType"`
	Config           map[string]any         `json:"config"`
	Parameters       []ToolParameterRequest `json:"parameters"`
	OutputParameters []ToolParameterRequest `json:"outputParameters"`
}

type UpdateUserToolRequest struct {
	Name             string                 `json:"name,omitempty"`
	Description      string                 `json:"description,omitempty"`
	Config           map[string]any         `json:"config,omitempty"`
	Parameters       []ToolParameterRequest `json:"parameters,omitempty"`
	OutputParameters []ToolParameterRequest `json:"outputParameters,omitempty"`
}

type ToolParameterRequest struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
	Default     any    `json:"default,omitempty"`
	Enum        []any  `json:"enum,omitempty"`
}

func convertParameters(params []ToolParameterRequest) []storage.ToolParameterDef {
	result := make([]storage.ToolParameterDef, 0, len(params))
	for _, p := range params {
		result = append(result, storage.ToolParameterDef{
			Name:        p.Name,
			Type:        p.Type,
			Required:    p.Required,
			Description: p.Description,
			Default:     p.Default,
			Enum:        p.Enum,
		})
	}
	return result
}

func ConvertToToolsParameter(params []storage.ToolParameterDef) []tools.ToolParameter {
	result := make([]tools.ToolParameter, 0, len(params))
	for _, p := range params {
		result = append(result, tools.ToolParameter{
			Name:        p.Name,
			Type:        tools.ParameterType(p.Type),
			Required:    p.Required,
			Description: p.Description,
			Default:     p.Default,
			Enum:        p.Enum,
		})
	}
	return result
}
