package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"ai/pkg/agentmanager"
	"ai/pkg/authz"
	"ai/pkg/codegen"
	"ai/pkg/executor"
	"ai/pkg/logger"
	"ai/pkg/storage"
	"ai/pkg/tools"
)

type UserAgentAPI struct {
	storage    *storage.MySQLStorage
	executor   *executor.InterpretiveExecutor
	generator  *codegen.CodeGenerator
	processMgr *agentmanager.AgentProcessManager
}

func NewUserAgentAPI(mysqlStorage *storage.MySQLStorage, exec *executor.InterpretiveExecutor, processMgr *agentmanager.AgentProcessManager) *UserAgentAPI {
	projectRoot, _ := os.Getwd()
	generator := codegen.NewCodeGenerator(codegen.GeneratorConfig{
		OutputDir: projectRoot,
	})

	return &UserAgentAPI{
		storage:    mysqlStorage,
		executor:   exec,
		generator:  generator,
		processMgr: processMgr,
	}
}

func (api *UserAgentAPI) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v1/orchestrator/user-agents", api.handleUserAgents)
	mux.HandleFunc("/v1/orchestrator/user-agents/", api.handleUserAgentByID)
}

func (api *UserAgentAPI) handleUserAgents(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		api.handleListUserAgents(w, r)
	case http.MethodPost:
		api.handleCreateUserAgent(w, r)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (api *UserAgentAPI) handleUserAgentByID(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	prefix := "/v1/orchestrator/user-agents/"
	if len(path) <= len(prefix) {
		http.Error(w, "agent id is required", http.StatusBadRequest)
		return
	}

	remaining := path[len(prefix):]

	if remaining == "test" {
		api.handleTestUserAgent(w, r)
		return
	}

	parts := splitPath(remaining)
	if len(parts) == 0 {
		http.Error(w, "agent id is required", http.StatusBadRequest)
		return
	}

	agentID := parts[0]

	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			api.handleGetUserAgent(w, r, agentID)
		case http.MethodPut:
			api.handleUpdateUserAgent(w, r, agentID)
		case http.MethodDelete:
			api.handleDeleteUserAgent(w, r, agentID)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}

	action := parts[1]
	switch action {
	case "test":
		api.handleTestUserAgentByID(w, r, agentID)
	case "publish":
		api.handlePublishUserAgent(w, r, agentID)
	case "stop":
		api.handleStopUserAgent(w, r, agentID)
	case "restart":
		api.handleRestartUserAgent(w, r, agentID)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (api *UserAgentAPI) handleListUserAgents(w http.ResponseWriter, r *http.Request) {
	logger.Infof("[UserAgentAPI] ListUserAgents")

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

	agents := make([]storage.UserAgentDefinition, 0)
	if hasAllScopeAccess(r, "orchestrator.agent.read") {
		allAgents, err := api.storage.ListUserAgents(ctx, "")
		if err != nil {
			logger.Errorf("[UserAgentAPI] List all agents failed: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		agents = allAgents
	} else {
		userAgents, err := api.storage.ListUserAgents(ctx, userID)
		if err != nil {
			logger.Errorf("[UserAgentAPI] ListUserAgents failed: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		systemAgents, err := api.storage.ListUserAgents(ctx, "system")
		if err != nil {
			logger.Errorf("[UserAgentAPI] List system agents failed: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		agents = append(systemAgents, userAgents...)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(agents)
}

func (api *UserAgentAPI) handleCreateUserAgent(w http.ResponseWriter, r *http.Request) {
	logger.Infof("[UserAgentAPI] CreateUserAgent")

	var req CreateUserAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.AgentID == "" {
		http.Error(w, "agentId is required", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if req.WorkflowID == "" {
		http.Error(w, "workflowId is required", http.StatusBadRequest)
		return
	}

	def := &storage.UserAgentDefinition{
		AgentID:     req.AgentID,
		UserID:      "",
		Name:        req.Name,
		Description: req.Description,
		WorkflowID:  req.WorkflowID,
		Status:      storage.AgentStatusDraft,
	}

	ctx := r.Context()
	userID, ok := authenticatedUserID(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if _, allowed := authorizeResourceAccess(r, "orchestrator.agent.manage", authz.ScopeOwn, userID, false); !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	def.UserID = userID
	if api.storage == nil {
		http.Error(w, "storage not available", http.StatusInternalServerError)
		return
	}
	allowWorkflowManageAll := hasAllScopeAccess(r, "orchestrator.workflow.manage")
	if _, err := api.storage.GetWorkflowScoped(ctx, req.WorkflowID, userID, allowWorkflowManageAll); err != nil {
		http.Error(w, "workflow not found or not owned by current user", http.StatusForbidden)
		return
	}

	if err := api.storage.SaveUserAgent(ctx, def); err != nil {
		logger.Errorf("[UserAgentAPI] CreateUserAgent failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(def)
}

func (api *UserAgentAPI) handleGetUserAgent(w http.ResponseWriter, r *http.Request, agentID string) {
	logger.Infof("[UserAgentAPI] GetUserAgent: %s", agentID)

	ctx := r.Context()

	if api.storage == nil {
		http.Error(w, "storage not available", http.StatusInternalServerError)
		return
	}

	requesterID, _ := authenticatedUserID(r)
	allowAllRead := hasAllScopeAccess(r, "orchestrator.agent.read")
	def, err := api.storage.GetUserAgentScoped(ctx, agentID, requesterID, true, allowAllRead)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	_, ok := authenticatedUserID(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if _, allowed := authorizeResourceAccess(r, "orchestrator.agent.read", authz.ScopeOwn, def.UserID, def.UserID == "system"); !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	response := api.buildAgentResponse(def)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func (api *UserAgentAPI) handleUpdateUserAgent(w http.ResponseWriter, r *http.Request, agentID string) {
	logger.Infof("[UserAgentAPI] UpdateUserAgent: %s", agentID)

	var req UpdateUserAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	if api.storage == nil {
		http.Error(w, "storage not available", http.StatusInternalServerError)
		return
	}

	existing, err := api.storage.GetUserAgent(ctx, agentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	userID, ok := authenticatedUserID(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if _, allowed := authorizeResourceAccess(r, "orchestrator.agent.manage", authz.ScopeOwn, existing.UserID, existing.UserID == "system"); !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if req.Name != "" {
		existing.Name = req.Name
	}
	if req.Description != "" {
		existing.Description = req.Description
	}
	if req.WorkflowID != "" {
		allowWorkflowManageAll := hasAllScopeAccess(r, "orchestrator.workflow.manage")
		if _, wfErr := api.storage.GetWorkflowScoped(ctx, req.WorkflowID, userID, allowWorkflowManageAll); wfErr != nil {
			http.Error(w, "workflow not found or not owned by current user", http.StatusForbidden)
			return
		}
		existing.WorkflowID = req.WorkflowID
	}

	if err := api.storage.SaveUserAgent(ctx, existing); err != nil {
		logger.Errorf("[UserAgentAPI] UpdateUserAgent failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(existing)
}

func (api *UserAgentAPI) handleDeleteUserAgent(w http.ResponseWriter, r *http.Request, agentID string) {
	logger.Infof("[UserAgentAPI] DeleteUserAgent: %s", agentID)

	ctx := r.Context()

	if api.storage == nil {
		http.Error(w, "storage not available", http.StatusInternalServerError)
		return
	}

	if api.processMgr != nil {
		status, _ := api.processMgr.GetAgentStatus(agentID)
		if status == agentmanager.ProcessStatusRunning {
			_ = api.processMgr.StopAgent(ctx, agentID)
		}
	}

	existing, err := api.storage.GetUserAgent(ctx, agentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	_, ok := authenticatedUserID(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if _, allowed := authorizeResourceAccess(r, "orchestrator.agent.manage", authz.ScopeOwn, existing.UserID, existing.UserID == "system"); !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	requesterID, _ := authenticatedUserID(r)
	allowAllManage := hasAllScopeAccess(r, "orchestrator.agent.manage")
	if err := api.storage.DeleteUserAgentScoped(ctx, agentID, requesterID, false, allowAllManage); err != nil {
		logger.Errorf("[UserAgentAPI] DeleteUserAgent failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"deleted": true})
}

func (api *UserAgentAPI) handleTestUserAgent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req TestAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	workflowDef := req.WorkflowDef.ToExecutorDefinition()
	userID, ok := authenticatedUserID(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if _, allowed := authorizeResourceAccess(r, "orchestrator.agent.manage", authz.ScopeOwn, userID, false); !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	api.executeTest(w, r, workflowDef, req.Input, userID)
}

func (api *UserAgentAPI) handleTestUserAgentByID(w http.ResponseWriter, r *http.Request, agentID string) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	if api.storage == nil {
		http.Error(w, "storage not available", http.StatusInternalServerError)
		return
	}

	def, err := api.storage.GetUserAgent(ctx, agentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	_, ok := authenticatedUserID(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if _, allowed := authorizeResourceAccess(r, "orchestrator.agent.manage", authz.ScopeOwn, def.UserID, false); !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	api.stopExistingAgentForRepublish(ctx, def)
	if api.processMgr == nil {
		def.Status = storage.AgentStatusDraft
		_ = api.storage.SaveUserAgent(ctx, def)
		http.Error(w, "process manager not available", http.StatusInternalServerError)
		return
	}

	workflowDef, err := api.storage.GetWorkflow(ctx, def.WorkflowID)
	if err != nil {
		http.Error(w, fmt.Sprintf("workflow not found: %s", def.WorkflowID), http.StatusNotFound)
		return
	}

	var req TestAgentInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	execWorkflowDef := convertStorageToExecutorDef(workflowDef)

	api.executeTest(w, r, execWorkflowDef, req.Input, def.UserID)
}

func (api *UserAgentAPI) executeTest(w http.ResponseWriter, r *http.Request, workflowDef *executor.WorkflowDefinition, input map[string]any, userID string) {
	if api.executor == nil {
		http.Error(w, "executor not available", http.StatusInternalServerError)
		return
	}
	if input == nil {
		input = make(map[string]any)
	}
	logger.Infof("[UserAgentAPI] executeTest workflowId=%s userId=%s inputKeys=%v inputTypes=%v", workflowDef.WorkflowID, userID, mapKeys(input), summarizeInputTypes(input))

	ctx := r.Context()

	cleanup := func() {}
	if strings.TrimSpace(userID) != "" {
		var err error
		cleanup, err = api.registerUserToolsForTest(ctx, userID)
		if err != nil {
			http.Error(w, fmt.Sprintf("load user tools: %v", err), http.StatusInternalServerError)
			return
		}
	}
	defer cleanup()

	result, err := api.executor.ExecuteWorkflowFromDefinition(ctx, workflowDef, input)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func mapKeys(m map[string]any) []string {
	if len(m) == 0 {
		return []string{}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func summarizeInputTypes(m map[string]any) map[string]string {
	if len(m) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = fmt.Sprintf("%T", v)
	}
	return out
}

func (api *UserAgentAPI) handlePublishUserAgent(w http.ResponseWriter, r *http.Request, agentID string) {
	logger.Infof("[UserAgentAPI] PublishUserAgent: %s", agentID)

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	if api.storage == nil {
		http.Error(w, "storage not available", http.StatusInternalServerError)
		return
	}

	def, err := api.storage.GetUserAgent(ctx, agentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	prevDef := *def
	_, ok := authenticatedUserID(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if _, allowed := authorizeResourceAccess(r, "orchestrator.agent.manage", authz.ScopeOwn, def.UserID, false); !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	workflowDef, err := api.storage.GetWorkflow(ctx, def.WorkflowID)
	if err != nil {
		http.Error(w, fmt.Sprintf("workflow not found: %s", def.WorkflowID), http.StatusNotFound)
		return
	}

	execWorkflowDef := convertStorageToExecutorDef(workflowDef)
	toolDefs := make([]codegen.ToolDefinition, 0)
	if api.storage != nil {
		systemTools, sysErr := api.storage.ListUserTools(ctx, "system")
		if sysErr != nil {
			logger.Warnf("[UserAgentAPI] List system tools failed while publishing: %v", sysErr)
		}
		userTools, userErr := api.storage.ListUserTools(ctx, def.UserID)
		if userErr != nil {
			logger.Warnf("[UserAgentAPI] List user tools failed while publishing: %v", userErr)
		}

		mergedByName := make(map[string]storage.UserToolDefinition, len(systemTools)+len(userTools))
		for _, t := range systemTools {
			mergedByName[t.Name] = t
		}
		for _, t := range userTools {
			mergedByName[t.Name] = t
		}

		for _, t := range mergedByName {
			toolDefs = append(toolDefs, codegen.ToolDefinition{
				ToolID:      t.ToolID,
				Name:        t.Name,
				Description: t.Description,
				ToolType:    tools.ToolType(t.ToolType),
				Config:      t.Config,
				Parameters:  ConvertToToolsParameter(t.Parameters),
			})
		}
	}

	genReq := &codegen.AgentGenerateRequest{
		AgentID:     agentID,
		Name:        def.Name,
		Description: def.Description,
		WorkflowDef: execWorkflowDef,
		Tools:       toolDefs,
	}

	genResult, err := api.generator.GenerateAgent(genReq)
	if err != nil {
		logger.Errorf("[UserAgentAPI] GenerateAgent failed: %v", err)
		http.Error(w, fmt.Sprintf("generate agent code: %v", err), http.StatusInternalServerError)
		return
	}

	def.CodePath = genResult.AgentDir
	now := time.Now()
	def.PublishedAt = &now
	def.Status = storage.AgentStatusPublished

	if err := api.storage.SaveUserAgent(ctx, def); err != nil {
		logger.Errorf("[UserAgentAPI] SaveUserAgent failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if api.processMgr != nil {
		if err := api.processMgr.CompileAgent(ctx, agentID, genResult.AgentDir); err != nil {
			logger.Errorf("[UserAgentAPI] CompileAgent failed: %v", err)
			def.Status = storage.AgentStatusDraft
			_ = api.storage.SaveUserAgent(ctx, def)
			http.Error(w, fmt.Sprintf("compile agent: %v", err), http.StatusInternalServerError)
			return
		}

		api.stopExistingAgentForRepublish(ctx, &prevDef)

		if err := api.processMgr.StartAgent(ctx, agentID, genResult.AgentDir); err != nil {
			logger.Errorf("[UserAgentAPI] StartAgent failed: %v", err)
			def.Status = storage.AgentStatusDraft
			_ = api.storage.SaveUserAgent(ctx, def)
			http.Error(w, fmt.Sprintf("start agent: %v", err), http.StatusInternalServerError)
			return
		}
	}

	if latest, err := api.storage.GetUserAgent(ctx, agentID); err == nil && latest != nil {
		def = latest
	}

	response := api.buildAgentResponse(def)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func (api *UserAgentAPI) stopExistingAgentForRepublish(ctx context.Context, def *storage.UserAgentDefinition) {
	if def == nil {
		return
	}

	if api.processMgr != nil {
		if err := api.processMgr.StopAgent(ctx, def.AgentID); err != nil {
			logger.Warnf("[UserAgentAPI] Republish stop via process manager skipped for %s: %v", def.AgentID, err)
		}
	}

	if def.ProcessPID > 0 {
		if proc, err := os.FindProcess(def.ProcessPID); err == nil && proc != nil {
			if killErr := proc.Kill(); killErr != nil {
				logger.Warnf("[UserAgentAPI] Republish pid kill skipped for %s pid=%d: %v", def.AgentID, def.ProcessPID, killErr)
			} else {
				logger.Infof("[UserAgentAPI] Republish pid kill succeeded for %s pid=%d", def.AgentID, def.ProcessPID)
			}
		}
	}

	if api.storage != nil {
		_ = api.storage.UpdateAgentStatus(ctx, def.AgentID, storage.AgentStatusStopped, 0, 0)
	}
	def.Status = storage.AgentStatusStopped
	def.Port = 0
	def.ProcessPID = 0
}

func (api *UserAgentAPI) handleStopUserAgent(w http.ResponseWriter, r *http.Request, agentID string) {
	logger.Infof("[UserAgentAPI] StopUserAgent: %s", agentID)

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	if api.storage != nil {
		def, err := api.storage.GetUserAgent(ctx, agentID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		_, ok := authenticatedUserID(r)
		if !ok {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if _, allowed := authorizeResourceAccess(r, "orchestrator.agent.ops", authz.ScopeAll, "", false); !allowed {
			if _, ownAllowed := authorizeResourceAccess(r, "orchestrator.agent.manage", authz.ScopeOwn, def.UserID, false); !ownAllowed {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
	}

	if api.processMgr == nil {
		http.Error(w, "process manager not available", http.StatusInternalServerError)
		return
	}

	if err := api.processMgr.StopAgent(ctx, agentID); err != nil {
		logger.Errorf("[UserAgentAPI] StopAgent failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if api.storage != nil {
		_ = api.storage.UpdateAgentStatus(ctx, agentID, storage.AgentStatusStopped, 0, 0)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"stopped": true})
}

func (api *UserAgentAPI) handleRestartUserAgent(w http.ResponseWriter, r *http.Request, agentID string) {
	logger.Infof("[UserAgentAPI] RestartUserAgent: %s", agentID)

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	if api.storage != nil {
		def, err := api.storage.GetUserAgent(ctx, agentID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		_, ok := authenticatedUserID(r)
		if !ok {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if _, allowed := authorizeResourceAccess(r, "orchestrator.agent.ops", authz.ScopeAll, "", false); !allowed {
			if _, ownAllowed := authorizeResourceAccess(r, "orchestrator.agent.manage", authz.ScopeOwn, def.UserID, false); !ownAllowed {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
	}

	if api.processMgr == nil {
		http.Error(w, "process manager not available", http.StatusInternalServerError)
		return
	}

	if err := api.processMgr.RestartAgent(ctx, agentID); err != nil {
		logger.Errorf("[UserAgentAPI] RestartAgent failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"restarted": true})
}

func (api *UserAgentAPI) buildAgentResponse(def *storage.UserAgentDefinition) *UserAgentResponse {
	response := &UserAgentResponse{
		AgentID:     def.AgentID,
		UserID:      def.UserID,
		Name:        def.Name,
		Description: def.Description,
		WorkflowID:  def.WorkflowID,
		Status:      string(def.Status),
		Port:        def.Port,
		ProcessPID:  def.ProcessPID,
		CodePath:    def.CodePath,
	}

	if def.PublishedAt != nil {
		response.PublishedAt = def.PublishedAt.Format(time.RFC3339)
	}

	if api.processMgr != nil {
		status, _ := api.processMgr.GetAgentStatus(def.AgentID)
		response.ProcessStatus = string(status)
	}

	return response
}

type CreateUserAgentRequest struct {
	AgentID     string `json:"agentId"`
	Name        string `json:"name"`
	Description string `json:"description"`
	WorkflowID  string `json:"workflowId"`
}

type UpdateUserAgentRequest struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	WorkflowID  string `json:"workflowId,omitempty"`
}

type TestAgentRequest struct {
	WorkflowDef TestWorkflowDefinition `json:"workflowDef"`
	Input       map[string]any         `json:"input"`
}

type TestAgentInput struct {
	Input map[string]any `json:"input"`
}

// TestWorkflowDefinition accepts both `workflowId` and legacy `id` from frontend payloads.
type TestWorkflowDefinition struct {
	WorkflowID  string             `json:"workflowId,omitempty"`
	ID          string             `json:"id,omitempty"`
	Name        string             `json:"name,omitempty"`
	Description string             `json:"description,omitempty"`
	StartNodeID string             `json:"startNodeId,omitempty"`
	Nodes       []executor.NodeDef `json:"nodes"`
	Edges       []executor.EdgeDef `json:"edges"`
}

func (d TestWorkflowDefinition) ToExecutorDefinition() *executor.WorkflowDefinition {
	workflowID := strings.TrimSpace(d.WorkflowID)
	if workflowID == "" {
		workflowID = strings.TrimSpace(d.ID)
	}
	if workflowID == "" {
		workflowID = fmt.Sprintf("adhoc_test_%d", time.Now().UnixNano())
	}

	startNodeID := strings.TrimSpace(d.StartNodeID)
	if startNodeID == "" && len(d.Nodes) > 0 {
		startNodeID = d.Nodes[0].ID
	}

	nodes := make([]executor.NodeDef, 0, len(d.Nodes))
	for _, n := range d.Nodes {
		nn := n
		if strings.EqualFold(strings.TrimSpace(nn.Type), "tool") {
			nn.AgentID = ""
			nn.TaskType = ""
		}
		nodes = append(nodes, nn)
	}

	return &executor.WorkflowDefinition{
		WorkflowID:  workflowID,
		Name:        d.Name,
		Description: d.Description,
		StartNodeID: startNodeID,
		Nodes:       nodes,
		Edges:       d.Edges,
	}
}

type UserAgentResponse struct {
	AgentID       string `json:"agentId"`
	UserID        string `json:"userId"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	WorkflowID    string `json:"workflowId"`
	Status        string `json:"status"`
	Port          int    `json:"port,omitempty"`
	ProcessPID    int    `json:"processPid,omitempty"`
	ProcessStatus string `json:"processStatus,omitempty"`
	CodePath      string `json:"codePath,omitempty"`
	PublishedAt   string `json:"publishedAt,omitempty"`
}

func convertStorageToExecutorDef(storageDef *storage.WorkflowDefinition) *executor.WorkflowDefinition {
	nodes := make([]executor.NodeDef, 0, len(storageDef.Nodes))
	for _, n := range storageDef.Nodes {
		agentID := n.AgentID
		taskType := n.TaskType
		if strings.EqualFold(strings.TrimSpace(n.Type), "tool") {
			agentID = ""
			taskType = ""
		}

		var loopConfig map[string]any
		if n.LoopConfig != nil {
			loopConfig = make(map[string]any)
			if v, ok := n.LoopConfig["max_iterations"]; ok {
				loopConfig["max_iterations"] = v
			}
			if v, ok := n.LoopConfig["continue_to"]; ok {
				loopConfig["continue_to"] = v
			}
			if v, ok := n.LoopConfig["exit_to"]; ok {
				loopConfig["exit_to"] = v
			}
		}

		nodes = append(nodes, executor.NodeDef{
			ID:         n.ID,
			Type:       n.Type,
			Config:     n.Config,
			AgentID:    agentID,
			TaskType:   taskType,
			Condition:  n.Condition,
			PreInput:   n.PreInput,
			LoopConfig: loopConfig,
			Metadata:   n.Metadata,
		})
	}

	edges := make([]executor.EdgeDef, 0, len(storageDef.Edges))
	for _, e := range storageDef.Edges {
		var mapping map[string]any
		if e.Mapping != nil {
			mapping = make(map[string]any)
			for k, v := range e.Mapping {
				mapping[k] = v
			}
		}

		edges = append(edges, executor.EdgeDef{
			From:    e.From,
			To:      e.To,
			Label:   e.Label,
			Mapping: mapping,
		})
	}

	return &executor.WorkflowDefinition{
		WorkflowID:  storageDef.WorkflowID,
		Name:        storageDef.Name,
		Description: storageDef.Description,
		StartNodeID: storageDef.StartNodeID,
		Nodes:       nodes,
		Edges:       edges,
	}
}

func splitPath(path string) []string {
	path = strings.Trim(path, "/")
	if path == "" {
		return []string{}
	}
	parts := strings.Split(path, "/")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (api *UserAgentAPI) registerUserToolsForTest(ctx context.Context, userID string) (func(), error) {
	if api.storage == nil {
		return func() {}, nil
	}
	systemTools, err := api.storage.ListUserTools(ctx, "system")
	if err != nil {
		return nil, err
	}
	userTools, err := api.storage.ListUserTools(ctx, userID)
	if err != nil {
		return nil, err
	}

	// Merge tools by name: user-defined tools override system tools with the same name.
	mergedByName := make(map[string]storage.UserToolDefinition, len(systemTools)+len(userTools))
	for _, t := range systemTools {
		mergedByName[t.Name] = t
	}
	for _, t := range userTools {
		mergedByName[t.Name] = t
	}
	allTools := make([]storage.UserToolDefinition, 0, len(mergedByName))
	for _, t := range mergedByName {
		allTools = append(allTools, t)
	}

	reg := api.executor.GetToolRegistry()
	type restoreEntry struct {
		name string
		tool tools.Tool
	}
	toRestore := make([]restoreEntry, 0)
	toRemove := make([]string, 0)

	for _, t := range allTools {
		toolImpl, convErr := buildRuntimeTool(t)
		if convErr != nil {
			return nil, fmt.Errorf("tool %s: %w", t.Name, convErr)
		}

		if reg.Exists(t.Name) {
			existing, getErr := reg.Get(t.Name)
			if getErr == nil && existing != nil {
				toRestore = append(toRestore, restoreEntry{name: t.Name, tool: existing})
			}
			_ = reg.Unregister(t.Name)
		}

		if regErr := reg.Register(toolImpl); regErr != nil {
			return nil, fmt.Errorf("register tool %s: %w", t.Name, regErr)
		}
		toRemove = append(toRemove, t.Name)
	}

	cleanup := func() {
		for _, name := range toRemove {
			_ = reg.Unregister(name)
		}
		for _, entry := range toRestore {
			_ = reg.Register(entry.tool)
		}
	}

	return cleanup, nil
}

func buildRuntimeTool(def storage.UserToolDefinition) (tools.Tool, error) {
	params := ConvertToToolsParameter(def.Parameters)
	switch tools.ToolType(def.ToolType) {
	case tools.ToolTypeHTTP:
		cfg := tools.HTTPToolConfig{
			Method:  getStringMap(def.Config, "method", "GET"),
			URL:     getStringMap(def.Config, "url", ""),
			Timeout: time.Duration(getIntMap(def.Config, "timeout", 30)) * time.Second,
		}
		if headers := getStringStringMap(def.Config, "headers"); len(headers) > 0 {
			cfg.Headers = headers
		}
		cfg.BodyTemplate = getStringMap(def.Config, "body_template", "")
		return tools.NewHTTPTool(def.Name, def.Description, params, cfg), nil

	case tools.ToolTypeMCP:
		mode := getMCPMode(def.Config)
		cfg := tools.MCPToolConfig{
			Mode:     mode,
			ToolName: getStringMap(def.Config, "tool_name", ""),
		}
		if mode == "stdio" {
			_, serverCfg, err := extractMCPStdioServer(def.Config)
			if err != nil {
				return nil, err
			}
			command, args := tools.NormalizeStdioCommand(serverCfg.Command, serverCfg.Args)
			cfg.Command = command
			cfg.Args = args
		} else {
			cfg.ServerURL = getStringMap(def.Config, "server_url", "")
			if strings.TrimSpace(cfg.ServerURL) == "" {
				return nil, fmt.Errorf("mcp server_url is required")
			}
		}
		return tools.NewMCPTool(def.Name, def.Description, params, cfg), nil

	default:
		return nil, fmt.Errorf("unsupported tool type: %s", def.ToolType)
	}
}

func getStringMap(m map[string]interface{}, key string, fallback string) string {
	if m == nil {
		return fallback
	}
	if raw, ok := m[key]; ok {
		switch v := raw.(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return v
			}
		}
	}
	return fallback
}

func getIntMap(m map[string]interface{}, key string, fallback int) int {
	if m == nil {
		return fallback
	}
	raw, ok := m[key]
	if !ok {
		return fallback
	}
	switch v := raw.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil {
			return i
		}
	}
	return fallback
}

func getStringStringMap(m map[string]interface{}, key string) map[string]string {
	out := make(map[string]string)
	if m == nil {
		return out
	}
	raw, ok := m[key]
	if !ok || raw == nil {
		return out
	}
	typed, ok := raw.(map[string]interface{})
	if !ok {
		return out
	}
	for k, v := range typed {
		out[k] = fmt.Sprintf("%v", v)
	}
	return out
}
