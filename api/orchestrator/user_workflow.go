package orchestrator

import (
	"ai/pkg/authz"
	"ai/pkg/logger"
	"ai/pkg/storage"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
)

type SaveWorkflowRequest struct {
	WorkflowID  string            `json:"workflowId,omitempty"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	StartNodeID string            `json:"startNodeId"`
	Nodes       []storage.NodeDef `json:"nodes"`
	Edges       []storage.EdgeDef `json:"edges"`
}

type SaveWorkflowResponse struct {
	WorkflowID string `json:"workflowId"`
	CreatedAt  string `json:"createdAt"`
	UpdatedAt  string `json:"updatedAt"`
}

type ListWorkflowsResponse struct {
	Workflows []storage.WorkflowSummary `json:"workflows"`
}

type GetWorkflowResponse struct {
	WorkflowID  string            `json:"workflowId"`
	UserID      string            `json:"userId"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	StartNodeID string            `json:"startNodeId"`
	Nodes       []storage.NodeDef `json:"nodes"`
	Edges       []storage.EdgeDef `json:"edges"`
	CreatedAt   string            `json:"createdAt"`
	UpdatedAt   string            `json:"updatedAt"`
}

func (api *OrchestratorAPI) handleSaveUserWorkflow(w http.ResponseWriter, r *http.Request) {
	logger.Infof("[SaveWorkflow] Received request: method=%s, path=%s", r.Method, r.URL.Path)

	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		logger.Infof("[SaveWorkflow] Method not allowed: %s", r.Method)
		writeJSONError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed")
		return
	}

	var req SaveWorkflowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.Errorf("[SaveWorkflow] Failed to decode request body: %v", err)
		writeJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", fmt.Sprintf("Invalid request body: %v", err))
		return
	}
	logger.Infof("[SaveWorkflow] Decoded request: workflowId=%s, userId=%s, name=%s, startNodeId=%s, nodes=%d, edges=%d",
		req.WorkflowID, "<auth>", req.Name, req.StartNodeID, len(req.Nodes), len(req.Edges))
	req.Nodes = normalizeStorageNodeDefinitions(req.Nodes)

	for i, node := range req.Nodes {
		model := ""
		url := ""
		hasAPIKey := false
		if node.Config != nil {
			if v, ok := node.Config["model"].(string); ok {
				model = v
			}
			if v, ok := node.Config["url"].(string); ok {
				url = v
			}
			if v, ok := node.Config["apikey"].(string); ok && v != "" {
				hasAPIKey = true
			}
		}
		logger.Infof("[SaveWorkflow] Node[%d]: id=%s, type=%s, agentId=%s, inputType=%s, outputType=%s, hasConfig=%t, model=%s, url=%s, hasAPIKey=%t",
			i, node.ID, node.Type, node.AgentID, node.InputType, node.OutputType, node.Config != nil, model, url, hasAPIKey)
	}
	for i, edge := range req.Edges {
		logger.Infof("[SaveWorkflow] Edge[%d]: from=%s, to=%s, label=%s", i, edge.From, edge.To, edge.Label)
	}

	if req.Name == "" {
		logger.Infof("[SaveWorkflow] Validation failed: name is required")
		writeJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "name is required")
		return
	}
	if req.StartNodeID == "" {
		logger.Infof("[SaveWorkflow] Validation failed: startNodeId is required")
		writeJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "startNodeId is required")
		return
	}
	if len(req.Nodes) == 0 {
		logger.Infof("[SaveWorkflow] Validation failed: nodes cannot be empty")
		writeJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "nodes cannot be empty")
		return
	}

	userID, ok := authenticatedUserID(r)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	if _, allowed := authorizeResourceAccess(r, "orchestrator.workflow.manage", authz.ScopeOwn, userID, false); !allowed {
		writeJSONError(w, http.StatusForbidden, "FORBIDDEN", "insufficient workflow permissions")
		return
	}

	workflowID := req.WorkflowID
	if workflowID == "" {
		workflowID = uuid.New().String()
		logger.Infof("[SaveWorkflow] Generated new workflowId: %s", workflowID)
	}

	logger.Infof("[SaveWorkflow] Getting MySQL storage...")
	db, err := storage.GetMySQLStorage()
	if err != nil {
		logger.Errorf("[SaveWorkflow] Failed to get MySQL storage: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "DATABASE_ERROR", fmt.Sprintf("Database connection error: %v", err))
		return
	}

	def := &storage.WorkflowDefinition{
		WorkflowID:  workflowID,
		UserID:      userID,
		Name:        req.Name,
		Description: req.Description,
		StartNodeID: req.StartNodeID,
		Nodes:       req.Nodes,
		Edges:       req.Edges,
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	allowManageAll := hasAllScopeAccess(r, "orchestrator.workflow.manage")

	if req.WorkflowID != "" {
		existing, getErr := db.GetWorkflowScoped(ctx, req.WorkflowID, userID, allowManageAll)
		if getErr == nil {
			if _, allowed := authorizeResourceAccess(r, "orchestrator.workflow.manage", authz.ScopeOwn, existing.UserID, false); !allowed {
				writeJSONError(w, http.StatusForbidden, "FORBIDDEN", "workflow does not belong to current user")
				return
			}
		}
	}

	logger.Infof("[SaveWorkflow] Saving workflow to database: workflowId=%s, userId=%s", workflowID, userID)

	if err := db.SaveWorkflow(ctx, def); err != nil {
		logger.Errorf("[SaveWorkflow] Failed to save workflow: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "SAVE_ERROR", fmt.Sprintf("Failed to save workflow: %v", err))
		return
	}

	logger.Infof("[SaveWorkflow] Successfully saved workflow: workflowId=%s, userId=%s", workflowID, userID)

	storage.DeleteDraftWorkflow(ctx, workflowID)
	logger.Infof("[SaveWorkflow] Deleted draft from Redis: workflowId=%s", workflowID)

	response := SaveWorkflowResponse{
		WorkflowID: workflowID,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
	logger.Infof("[SaveWorkflow] Response sent: workflowId=%s", workflowID)
}

func (api *OrchestratorAPI) handleListUserWorkflows(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed")
		return
	}

	userID, ok := authenticatedUserID(r)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}

	db, err := storage.GetMySQLStorage()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "DATABASE_ERROR", fmt.Sprintf("Database connection error: %v", err))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	allowReadAll := hasAllScopeAccess(r, "orchestrator.workflow.read")

	workflows, err := db.ListWorkflowsScoped(ctx, userID, allowReadAll)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "QUERY_ERROR", fmt.Sprintf("Failed to list workflows: %v", err))
		return
	}

	response := ListWorkflowsResponse{
		Workflows: workflows,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

func (api *OrchestratorAPI) handleGetUserWorkflow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed")
		return
	}

	workflowID := extractPathParam(r.URL.Path, "/v1/orchestrator/user-workflows/")
	if workflowID == "" {
		writeJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "workflowId is required")
		return
	}

	db, err := storage.GetMySQLStorage()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "DATABASE_ERROR", fmt.Sprintf("Database connection error: %v", err))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	userID, ok := authenticatedUserID(r)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	allowReadAll := hasAllScopeAccess(r, "orchestrator.workflow.read")

	def, err := db.GetWorkflowScoped(ctx, workflowID, userID, allowReadAll)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "NOT_FOUND", fmt.Sprintf("Workflow not found: %v", err))
		return
	}
	if _, allowed := authorizeResourceAccess(r, "orchestrator.workflow.read", authz.ScopeOwn, def.UserID, false); !allowed {
		writeJSONError(w, http.StatusForbidden, "FORBIDDEN", "workflow does not belong to current user")
		return
	}

	response := GetWorkflowResponse{
		WorkflowID:  def.WorkflowID,
		UserID:      def.UserID,
		Name:        def.Name,
		Description: def.Description,
		StartNodeID: def.StartNodeID,
		Nodes:       normalizeStorageNodeDefinitions(def.Nodes),
		Edges:       def.Edges,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:   time.Now().UTC().Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

func (api *OrchestratorAPI) handleDeleteUserWorkflow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSONError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed")
		return
	}

	workflowID := extractPathParam(r.URL.Path, "/v1/orchestrator/user-workflows/")
	if workflowID == "" {
		writeJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "workflowId is required")
		return
	}

	db, err := storage.GetMySQLStorage()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "DATABASE_ERROR", fmt.Sprintf("Database connection error: %v", err))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	userID, ok := authenticatedUserID(r)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	allowManageAll := hasAllScopeAccess(r, "orchestrator.workflow.manage")

	def, getErr := db.GetWorkflowScoped(ctx, workflowID, userID, allowManageAll)
	if getErr != nil {
		writeJSONError(w, http.StatusNotFound, "NOT_FOUND", fmt.Sprintf("Workflow not found: %v", getErr))
		return
	}
	if _, allowed := authorizeResourceAccess(r, "orchestrator.workflow.manage", authz.ScopeOwn, def.UserID, false); !allowed {
		writeJSONError(w, http.StatusForbidden, "FORBIDDEN", "workflow does not belong to current user")
		return
	}

	if err := db.DeleteWorkflowScoped(ctx, workflowID, userID, allowManageAll); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "DELETE_ERROR", fmt.Sprintf("Failed to delete workflow: %v", err))
		return
	}

	logger.Infof("Deleted workflow %s", workflowID)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"message": "Workflow deleted successfully"})
}

func extractPathParam(path, prefix string) string {
	if len(path) <= len(prefix) {
		return ""
	}
	return path[len(prefix):]
}

func writeJSONError(w http.ResponseWriter, statusCode int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(ErrorResponse{
		APIVersion: APIVersionV1,
		Error: ErrorDetail{
			Code:    code,
			Message: message,
		},
	})
}

func (api *OrchestratorAPI) handleUserWorkflowByID(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		api.handleGetUserWorkflow(w, r)
	case http.MethodDelete:
		api.handleDeleteUserWorkflow(w, r)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed")
	}
}
