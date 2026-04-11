package storage

import (
	"ai/pkg/logger"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

type MySQLStorage struct {
	db *sql.DB
}

var (
	mysqlStorage     *MySQLStorage
	mysqlStorageOnce sync.Once
	mysqlStorageErr  error
)

var defaultRoles = []Role{
	{RoleCode: "viewer", RoleName: "Viewer", Description: "Read-only role", Status: 1},
	{RoleCode: "user", RoleName: "User", Description: "Default registered user", Status: 1},
	{RoleCode: "operator", RoleName: "Operator", Description: "Can operate system resources", Status: 1},
	{RoleCode: "admin", RoleName: "Administrator", Description: "Full access", Status: 1},
}

var rolePriorityOrder = map[string]int{
	"admin":    1,
	"operator": 2,
	"user":     3,
	"viewer":   4,
}

func rolePriority(roleCode string) int {
	if v, ok := rolePriorityOrder[strings.ToLower(strings.TrimSpace(roleCode))]; ok {
		return v
	}
	return 100
}

func pickSingleRoleCode(candidates []string) (string, error) {
	if len(candidates) == 0 {
		return "", fmt.Errorf("exactly one role is required")
	}
	best := ""
	bestScore := 101
	seen := map[string]struct{}{}
	for _, rc := range candidates {
		code := strings.TrimSpace(strings.ToLower(rc))
		if code == "" {
			continue
		}
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		score := rolePriority(code)
		if best == "" || score < bestScore {
			best = code
			bestScore = score
		}
	}
	if best == "" {
		return "", fmt.Errorf("exactly one valid role is required")
	}
	return best, nil
}

func InitMySQL(dsn string) (*MySQLStorage, error) {
	mysqlStorageOnce.Do(func() {
		mysqlStorage, mysqlStorageErr = NewMySQLStorage(dsn)
	})
	return mysqlStorage, mysqlStorageErr
}

func GetMySQLStorage() (*MySQLStorage, error) {
	if mysqlStorage == nil {
		return nil, fmt.Errorf("mysql storage not initialized, call InitMySQL first")
	}
	return mysqlStorage, nil
}

func NewMySQLStorage(dsn string) (*MySQLStorage, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)

	return &MySQLStorage{db: db}, nil
}

func (s *MySQLStorage) Close() error {
	return s.db.Close()
}

func (s *MySQLStorage) SaveWorkflow(ctx context.Context, def *WorkflowDefinition) error {
	logger.Infof("[MySQL] Starting SaveWorkflow: workflowId=%s, userId=%s, name=%s", def.WorkflowID, def.UserID, def.Name)
	hasPreInput, err := s.hasWorkflowNodePreInputColumn(ctx)
	if err != nil {
		return fmt.Errorf("detect workflow_node.pre_input column: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		logger.Errorf("[MySQL] Failed to begin transaction: %v", err)
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	var existingID int64
	err = tx.QueryRowContext(ctx,
		"SELECT id FROM user_workflow WHERE workflow_id = ?", def.WorkflowID).Scan(&existingID)
	if err != nil && err != sql.ErrNoRows {
		logger.Errorf("[MySQL] Failed to check existing workflow: %v", err)
		return fmt.Errorf("check existing workflow: %w", err)
	}

	if existingID > 0 {
		logger.Infof("[MySQL] Updating existing workflow: id=%d, workflowId=%s", existingID, def.WorkflowID)
		_, err = tx.ExecContext(ctx,
			"UPDATE user_workflow SET name = ?, description = ?, start_node_id = ?, updated_at = NOW() WHERE workflow_id = ?",
			def.Name, def.Description, def.StartNodeID, def.WorkflowID)
		if err != nil {
			logger.Errorf("[MySQL] Failed to update workflow: %v", err)
			return fmt.Errorf("update workflow: %w", err)
		}

		logger.Infof("[MySQL] Deleting old edges for workflow: %s", def.WorkflowID)
		_, err = tx.ExecContext(ctx, "DELETE FROM workflow_edge WHERE workflow_id = ?", def.WorkflowID)
		if err != nil {
			logger.Errorf("[MySQL] Failed to delete old edges: %v", err)
			return fmt.Errorf("delete old edges: %w", err)
		}

		logger.Infof("[MySQL] Deleting old nodes for workflow: %s", def.WorkflowID)
		_, err = tx.ExecContext(ctx, "DELETE FROM workflow_node WHERE workflow_id = ?", def.WorkflowID)
		if err != nil {
			logger.Errorf("[MySQL] Failed to delete old nodes: %v", err)
			return fmt.Errorf("delete old nodes: %w", err)
		}
	} else {
		logger.Infof("[MySQL] Inserting new workflow: workflowId=%s", def.WorkflowID)
		_, err = tx.ExecContext(ctx,
			"INSERT INTO user_workflow (workflow_id, user_id, name, description, start_node_id, status) VALUES (?, ?, ?, ?, ?, 1)",
			def.WorkflowID, def.UserID, def.Name, def.Description, def.StartNodeID)
		if err != nil {
			logger.Errorf("[MySQL] Failed to insert workflow: %v", err)
			return fmt.Errorf("insert workflow: %w", err)
		}
	}

	logger.Infof("[MySQL] Inserting %d nodes for workflow: %s", len(def.Nodes), def.WorkflowID)
	for _, node := range def.Nodes {
		dbNode, err := node.ToDBNode(def.WorkflowID)
		if err != nil {
			logger.Errorf("[MySQL] Failed to convert node %s: %v", node.ID, err)
			return fmt.Errorf("convert node %s: %w", node.ID, err)
		}

		logger.Infof("[MySQL] Inserting node: nodeId=%s, nodeType=%s, agentId=%v, inputType=%s, outputType=%s, hasConfig=%t",
			dbNode.NodeID, dbNode.NodeType, dbNode.AgentID, node.InputType, node.OutputType, node.Config != nil)
		insertSQL := "INSERT INTO workflow_node (workflow_id, node_id, node_type, agent_id, task_type, pre_input, loop_config, metadata) VALUES (?, ?, ?, ?, ?, ?, ?, ?)"
		if !hasPreInput {
			insertSQL = "INSERT INTO workflow_node (workflow_id, node_id, node_type, agent_id, task_type, condition_expr, loop_config, metadata) VALUES (?, ?, ?, ?, ?, ?, ?, ?)"
		}
		_, err = tx.ExecContext(ctx,
			insertSQL,
			dbNode.WorkflowID, dbNode.NodeID, dbNode.NodeType, dbNode.AgentID, dbNode.TaskType, dbNode.PreInput, dbNode.LoopConfig, dbNode.Metadata)
		if err != nil {
			logger.Errorf("[MySQL] Failed to insert node %s: %v", node.ID, err)
			return fmt.Errorf("insert node %s: %w", node.ID, err)
		}
	}

	logger.Infof("[MySQL] Inserting %d edges for workflow: %s", len(def.Edges), def.WorkflowID)
	for i, edge := range def.Edges {
		dbEdge, err := edge.ToDBEdge(def.WorkflowID, i)
		if err != nil {
			logger.Errorf("[MySQL] Failed to convert edge %s->%s: %v", edge.From, edge.To, err)
			return fmt.Errorf("convert edge %s->%s: %w", edge.From, edge.To, err)
		}

		logger.Infof("[MySQL] Inserting edge: from=%s, to=%s, label=%v", dbEdge.FromNodeID, dbEdge.ToNodeID, dbEdge.Label)
		_, err = tx.ExecContext(ctx,
			"INSERT INTO workflow_edge (workflow_id, from_node_id, to_node_id, label, mapping, sort_order) VALUES (?, ?, ?, ?, ?, ?)",
			dbEdge.WorkflowID, dbEdge.FromNodeID, dbEdge.ToNodeID, dbEdge.Label, dbEdge.Mapping, dbEdge.SortOrder)
		if err != nil {
			logger.Errorf("[MySQL] Failed to insert edge %s->%s: %v", edge.From, edge.To, err)
			return fmt.Errorf("insert edge %s->%s: %w", edge.From, edge.To, err)
		}
	}

	if err := tx.Commit(); err != nil {
		logger.Errorf("[MySQL] Failed to commit transaction: %v", err)
		return fmt.Errorf("commit transaction: %w", err)
	}

	logger.Infof("[MySQL] Successfully saved workflow: workflowId=%s", def.WorkflowID)
	return nil
}

func (s *MySQLStorage) GetWorkflow(ctx context.Context, workflowID string) (*WorkflowDefinition, error) {
	var wf UserWorkflow
	err := s.db.QueryRowContext(ctx,
		"SELECT workflow_id, user_id, name, description, start_node_id FROM user_workflow WHERE workflow_id = ? AND status = 1",
		workflowID).Scan(&wf.WorkflowID, &wf.UserID, &wf.Name, &wf.Description, &wf.StartNodeID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("workflow not found: %s", workflowID)
		}
		return nil, fmt.Errorf("query workflow: %w", err)
	}

	def := &WorkflowDefinition{
		WorkflowID:  wf.WorkflowID,
		UserID:      wf.UserID,
		Name:        wf.Name,
		Description: wf.Description,
		StartNodeID: wf.StartNodeID,
	}

	nodesSelectSQL, err := s.workflowNodeSelectSQL(ctx)
	if err != nil {
		return nil, fmt.Errorf("build workflow node select SQL: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, nodesSelectSQL, workflowID)
	if err != nil {
		return nil, fmt.Errorf("query nodes: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var nodeID, nodeType string
		var agentID, taskType, preInput sql.NullString
		var loopConfig, metadata []byte

		err := rows.Scan(&nodeID, &nodeType, &agentID, &taskType, &preInput, &loopConfig, &metadata)
		if err != nil {
			return nil, fmt.Errorf("scan node: %w", err)
		}

		rawNode := &WorkflowNode{
			WorkflowID: workflowID,
			NodeID:     nodeID,
			NodeType:   nodeType,
			AgentID:    agentID,
			TaskType:   taskType,
			PreInput:   preInput,
			LoopConfig: loopConfig,
			Metadata:   metadata,
		}

		nodeDef, convErr := DBNodeToNodeDef(rawNode)
		if convErr != nil {
			return nil, fmt.Errorf("convert node %s: %w", nodeID, convErr)
		}
		def.Nodes = append(def.Nodes, *nodeDef)
	}

	rows, err = s.db.QueryContext(ctx,
		"SELECT from_node_id, to_node_id, label, mapping FROM workflow_edge WHERE workflow_id = ? ORDER BY sort_order",
		workflowID)
	if err != nil {
		return nil, fmt.Errorf("query edges: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var fromNodeID, toNodeID string
		var label sql.NullString
		var mapping []byte

		err := rows.Scan(&fromNodeID, &toNodeID, &label, &mapping)
		if err != nil {
			return nil, fmt.Errorf("scan edge: %w", err)
		}

		edgeDef := &EdgeDef{
			From:  fromNodeID,
			To:    toNodeID,
			Label: label.String,
		}

		if mapping != nil {
			var m map[string]interface{}
			if err := json.Unmarshal(mapping, &m); err == nil {
				edgeDef.Mapping = m
			}
		}

		def.Edges = append(def.Edges, *edgeDef)
	}

	return def, nil
}

func (s *MySQLStorage) ListWorkflows(ctx context.Context, userID string) ([]WorkflowSummary, error) {
	query := `
		SELECT w.workflow_id, w.user_id, w.name, w.description, w.status, w.created_at, w.updated_at, COUNT(n.id) as node_count
		FROM user_workflow w
		LEFT JOIN workflow_node n ON w.workflow_id = n.workflow_id
		WHERE w.status = 1
	`
	args := []interface{}{}
	if userID != "" {
		query += " AND w.user_id = ?"
		args = append(args, userID)
	}
	query += " GROUP BY w.workflow_id ORDER BY w.updated_at DESC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query workflows: %w", err)
	}
	defer rows.Close()

	var summaries []WorkflowSummary
	for rows.Next() {
		var s WorkflowSummary
		err := rows.Scan(&s.WorkflowID, &s.UserID, &s.Name, &s.Description, &s.Status, &s.CreatedAt, &s.UpdatedAt, &s.NodeCount)
		if err != nil {
			return nil, fmt.Errorf("scan workflow summary: %w", err)
		}
		summaries = append(summaries, s)
	}

	return summaries, nil
}

func (s *MySQLStorage) ListWorkflowsScoped(ctx context.Context, requesterUserID string, allowAll bool) ([]WorkflowSummary, error) {
	if allowAll {
		return s.ListWorkflows(ctx, "")
	}
	return s.ListWorkflows(ctx, requesterUserID)
}

func (s *MySQLStorage) GetWorkflowScoped(ctx context.Context, workflowID string, requesterUserID string, allowAll bool) (*WorkflowDefinition, error) {
	def, err := s.GetWorkflow(ctx, workflowID)
	if err != nil {
		return nil, err
	}
	if allowAll {
		return def, nil
	}
	if strings.TrimSpace(def.UserID) != strings.TrimSpace(requesterUserID) {
		return nil, fmt.Errorf("workflow not found: %s", workflowID)
	}
	return def, nil
}

func (s *MySQLStorage) DeleteWorkflow(ctx context.Context, workflowID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, "DELETE FROM workflow_edge WHERE workflow_id = ?", workflowID)
	if err != nil {
		return fmt.Errorf("delete edges: %w", err)
	}

	_, err = tx.ExecContext(ctx, "DELETE FROM workflow_node WHERE workflow_id = ?", workflowID)
	if err != nil {
		return fmt.Errorf("delete nodes: %w", err)
	}

	_, err = tx.ExecContext(ctx, "DELETE FROM user_workflow WHERE workflow_id = ?", workflowID)
	if err != nil {
		return fmt.Errorf("delete workflow: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}

func (s *MySQLStorage) DeleteWorkflowScoped(ctx context.Context, workflowID string, requesterUserID string, allowAll bool) error {
	def, err := s.GetWorkflow(ctx, workflowID)
	if err != nil {
		return err
	}
	if !allowAll && strings.TrimSpace(def.UserID) != strings.TrimSpace(requesterUserID) {
		return fmt.Errorf("workflow not found: %s", workflowID)
	}
	return s.DeleteWorkflow(ctx, workflowID)
}

func (s *MySQLStorage) WorkflowExists(ctx context.Context, workflowID string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM user_workflow WHERE workflow_id = ?", workflowID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check workflow exists: %w", err)
	}
	return count > 0, nil
}

func (s *MySQLStorage) GetAgentWorkflows(ctx context.Context) ([]WorkflowDefinition, error) {
	query := `
		SELECT workflow_id, user_id, name, description, start_node_id
		FROM user_workflow 
		WHERE user_id = 'system' AND status = 1
		ORDER BY workflow_id
	`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query agent workflows: %w", err)
	}
	defer rows.Close()

	var workflows []WorkflowDefinition
	for rows.Next() {
		var wf WorkflowDefinition
		err := rows.Scan(&wf.WorkflowID, &wf.UserID, &wf.Name, &wf.Description, &wf.StartNodeID)
		if err != nil {
			return nil, fmt.Errorf("scan workflow: %w", err)
		}
		workflows = append(workflows, wf)
	}

	for i := range workflows {
		nodes, err := s.getNodes(ctx, workflows[i].WorkflowID)
		if err != nil {
			return nil, err
		}
		workflows[i].Nodes = nodes

		edges, err := s.getEdges(ctx, workflows[i].WorkflowID)
		if err != nil {
			return nil, err
		}
		workflows[i].Edges = edges
	}

	return workflows, nil
}

func (s *MySQLStorage) getNodes(ctx context.Context, workflowID string) ([]NodeDef, error) {
	nodesSelectSQL, err := s.workflowNodeSelectSQL(ctx)
	if err != nil {
		return nil, fmt.Errorf("build workflow node select SQL: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, nodesSelectSQL, workflowID)
	if err != nil {
		return nil, fmt.Errorf("query nodes: %w", err)
	}
	defer rows.Close()

	var nodes []NodeDef
	for rows.Next() {
		var nodeID, nodeType string
		var agentID, taskType, preInput sql.NullString
		var loopConfig, metadata []byte

		err := rows.Scan(&nodeID, &nodeType, &agentID, &taskType, &preInput, &loopConfig, &metadata)
		if err != nil {
			return nil, fmt.Errorf("scan node: %w", err)
		}

		nodeDef := &NodeDef{
			ID:       nodeID,
			Type:     nodeType,
			AgentID:  agentID.String,
			TaskType: taskType.String,
			PreInput: preInput.String,
		}

		if loopConfig != nil {
			var lc map[string]interface{}
			if err := json.Unmarshal(loopConfig, &lc); err == nil {
				nodeDef.LoopConfig = lc
			}
		}

		if metadata != nil {
			var m map[string]string
			if err := json.Unmarshal(metadata, &m); err == nil {
				nodeDef.Metadata = m
			}
		}

		nodes = append(nodes, *nodeDef)
	}

	return nodes, nil
}

func (s *MySQLStorage) hasWorkflowNodePreInputColumn(ctx context.Context) (bool, error) {
	var cnt int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE()
		  AND TABLE_NAME = 'workflow_node'
		  AND COLUMN_NAME = 'pre_input'
	`).Scan(&cnt)
	if err != nil {
		return false, err
	}
	return cnt > 0, nil
}

func (s *MySQLStorage) workflowNodeSelectSQL(ctx context.Context) (string, error) {
	hasPreInput, err := s.hasWorkflowNodePreInputColumn(ctx)
	if err != nil {
		return "", err
	}
	if hasPreInput {
		return "SELECT node_id, node_type, agent_id, task_type, pre_input, loop_config, metadata FROM workflow_node WHERE workflow_id = ? ORDER BY id", nil
	}
	return "SELECT node_id, node_type, agent_id, task_type, condition_expr AS pre_input, loop_config, metadata FROM workflow_node WHERE workflow_id = ? ORDER BY id", nil
}

func (s *MySQLStorage) getEdges(ctx context.Context, workflowID string) ([]EdgeDef, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT from_node_id, to_node_id, label, mapping FROM workflow_edge WHERE workflow_id = ? ORDER BY sort_order",
		workflowID)
	if err != nil {
		return nil, fmt.Errorf("query edges: %w", err)
	}
	defer rows.Close()

	var edges []EdgeDef
	for rows.Next() {
		var fromNodeID, toNodeID string
		var label sql.NullString
		var mapping []byte

		err := rows.Scan(&fromNodeID, &toNodeID, &label, &mapping)
		if err != nil {
			return nil, fmt.Errorf("scan edge: %w", err)
		}

		edgeDef := &EdgeDef{
			From:  fromNodeID,
			To:    toNodeID,
			Label: label.String,
		}

		if mapping != nil {
			var m map[string]interface{}
			if err := json.Unmarshal(mapping, &m); err == nil {
				edgeDef.Mapping = m
			}
		}

		edges = append(edges, *edgeDef)
	}

	return edges, nil
}

func (s *MySQLStorage) SaveUserTool(ctx context.Context, def *UserToolDefinition) error {
	tool, err := def.ToDBTool()
	if err != nil {
		return fmt.Errorf("convert tool definition: %w", err)
	}

	var existingID int64
	err = s.db.QueryRowContext(ctx,
		"SELECT id FROM user_tool WHERE tool_id = ?", tool.ToolID).Scan(&existingID)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("check existing tool: %w", err)
	}

	if existingID > 0 {
		_, err = s.db.ExecContext(ctx,
			"UPDATE user_tool SET name = ?, description = ?, tool_type = ?, config = ?, parameters = ?, output_parameters = ?, updated_at = NOW() WHERE tool_id = ?",
			tool.Name, tool.Description, tool.ToolType, tool.Config, tool.Parameters, tool.OutputParameters, tool.ToolID)
		if err != nil {
			return fmt.Errorf("update tool: %w", err)
		}
	} else {
		_, err = s.db.ExecContext(ctx,
			"INSERT INTO user_tool (tool_id, user_id, name, description, tool_type, config, parameters, output_parameters, status) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1)",
			tool.ToolID, tool.UserID, tool.Name, tool.Description, tool.ToolType, tool.Config, tool.Parameters, tool.OutputParameters)
		if err != nil {
			return fmt.Errorf("insert tool: %w", err)
		}
	}

	return nil
}

func (s *MySQLStorage) GetUserTool(ctx context.Context, toolID string) (*UserToolDefinition, error) {
	var tool UserTool
	var configData []byte
	var paramsData []byte
	var outputParamsData []byte
	err := s.db.QueryRowContext(ctx,
		"SELECT id, tool_id, user_id, name, description, tool_type, config, parameters, output_parameters, status, created_at, updated_at FROM user_tool WHERE tool_id = ? AND status = 1",
		toolID).Scan(&tool.ID, &tool.ToolID, &tool.UserID, &tool.Name, &tool.Description, &tool.ToolType, &configData, &paramsData, &outputParamsData, &tool.Status, &tool.CreatedAt, &tool.UpdatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("tool not found: %s", toolID)
		}
		return nil, fmt.Errorf("query tool: %w", err)
	}
	tool.Config = configData
	tool.Parameters = paramsData
	tool.OutputParameters = outputParamsData

	return DBToolToDefinition(&tool)
}

func (s *MySQLStorage) GetUserToolScoped(ctx context.Context, toolID string, requesterUserID string, allowSystem bool, allowAll bool) (*UserToolDefinition, error) {
	def, err := s.GetUserTool(ctx, toolID)
	if err != nil {
		return nil, err
	}
	if allowAll {
		return def, nil
	}
	owner := strings.TrimSpace(def.UserID)
	if owner == strings.TrimSpace(requesterUserID) {
		return def, nil
	}
	if allowSystem && owner == "system" {
		return def, nil
	}
	return nil, fmt.Errorf("tool not found: %s", toolID)
}

func (s *MySQLStorage) ListUserTools(ctx context.Context, userID string) ([]UserToolDefinition, error) {
	query := "SELECT id, tool_id, user_id, name, description, tool_type, config, parameters, output_parameters, status, created_at, updated_at FROM user_tool WHERE status = 1"
	args := []interface{}{}
	if userID != "" {
		query += " AND user_id = ?"
		args = append(args, userID)
	}
	query += " ORDER BY updated_at DESC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query tools: %w", err)
	}
	defer rows.Close()

	tools := make([]UserToolDefinition, 0)
	for rows.Next() {
		var tool UserTool
		var configData []byte
		var paramsData []byte
		var outputParamsData []byte
		err := rows.Scan(&tool.ID, &tool.ToolID, &tool.UserID, &tool.Name, &tool.Description, &tool.ToolType, &configData, &paramsData, &outputParamsData, &tool.Status, &tool.CreatedAt, &tool.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("scan tool: %w", err)
		}
		tool.Config = configData
		tool.Parameters = paramsData
		tool.OutputParameters = outputParamsData

		def, err := DBToolToDefinition(&tool)
		if err != nil {
			return nil, fmt.Errorf("convert tool: %w", err)
		}
		tools = append(tools, *def)
	}

	return tools, nil
}
func (s *MySQLStorage) DeleteUserTool(ctx context.Context, toolID string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE user_tool SET status = 0, updated_at = NOW() WHERE tool_id = ?", toolID)
	if err != nil {
		return fmt.Errorf("delete tool: %w", err)
	}
	return nil
}

func (s *MySQLStorage) DeleteUserToolScoped(ctx context.Context, toolID string, requesterUserID string, allowSystem bool, allowAll bool) error {
	def, err := s.GetUserToolScoped(ctx, toolID, requesterUserID, allowSystem, allowAll)
	if err != nil {
		return err
	}
	return s.DeleteUserTool(ctx, def.ToolID)
}

func (s *MySQLStorage) SaveUserAgent(ctx context.Context, def *UserAgentDefinition) error {
	agent, err := def.ToDBAgent()
	if err != nil {
		return fmt.Errorf("convert agent definition: %w", err)
	}

	var existingID int64
	err = s.db.QueryRowContext(ctx,
		"SELECT id FROM user_agent WHERE agent_id = ?", agent.AgentID).Scan(&existingID)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("check existing agent: %w", err)
	}

	if existingID > 0 {
		_, err = s.db.ExecContext(ctx,
			"UPDATE user_agent SET name = ?, description = ?, workflow_id = ?, status = ?, port = ?, process_pid = ?, code_path = ?, published_at = ?, updated_at = NOW() WHERE agent_id = ?",
			agent.Name, agent.Description, agent.WorkflowID, agent.Status, agent.Port, agent.ProcessPID, agent.CodePath, agent.PublishedAt, agent.AgentID)
		if err != nil {
			return fmt.Errorf("update agent: %w", err)
		}
	} else {
		_, err = s.db.ExecContext(ctx,
			"INSERT INTO user_agent (agent_id, user_id, name, description, workflow_id, status, port, process_pid, code_path, published_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			agent.AgentID, agent.UserID, agent.Name, agent.Description, agent.WorkflowID, agent.Status, agent.Port, agent.ProcessPID, agent.CodePath, agent.PublishedAt)
		if err != nil {
			return fmt.Errorf("insert agent: %w", err)
		}
	}

	return nil
}

func (s *MySQLStorage) GetUserAgent(ctx context.Context, agentID string) (*UserAgentDefinition, error) {
	var agent UserAgent
	err := s.db.QueryRowContext(ctx,
		"SELECT id, agent_id, user_id, name, description, workflow_id, status, port, process_pid, code_path, published_at, created_at, updated_at FROM user_agent WHERE agent_id = ?",
		agentID).Scan(&agent.ID, &agent.AgentID, &agent.UserID, &agent.Name, &agent.Description, &agent.WorkflowID, &agent.Status, &agent.Port, &agent.ProcessPID, &agent.CodePath, &agent.PublishedAt, &agent.CreatedAt, &agent.UpdatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("agent not found: %s", agentID)
		}
		return nil, fmt.Errorf("query agent: %w", err)
	}

	return DBAgentToDefinition(&agent)
}

func (s *MySQLStorage) GetUserAgentScoped(ctx context.Context, agentID string, requesterUserID string, allowSystem bool, allowAll bool) (*UserAgentDefinition, error) {
	def, err := s.GetUserAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if allowAll {
		return def, nil
	}
	owner := strings.TrimSpace(def.UserID)
	if owner == strings.TrimSpace(requesterUserID) {
		return def, nil
	}
	if allowSystem && owner == "system" {
		return def, nil
	}
	return nil, fmt.Errorf("agent not found: %s", agentID)
}

func (s *MySQLStorage) ListUserAgents(ctx context.Context, userID string) ([]UserAgentDefinition, error) {
	query := "SELECT id, agent_id, user_id, name, description, workflow_id, status, port, process_pid, code_path, published_at, created_at, updated_at FROM user_agent WHERE 1=1"
	args := []interface{}{}
	if userID != "" {
		query += " AND user_id = ?"
		args = append(args, userID)
	}
	query += " ORDER BY updated_at DESC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query agents: %w", err)
	}
	defer rows.Close()

	var agents []UserAgentDefinition
	for rows.Next() {
		var agent UserAgent
		err := rows.Scan(&agent.ID, &agent.AgentID, &agent.UserID, &agent.Name, &agent.Description, &agent.WorkflowID, &agent.Status, &agent.Port, &agent.ProcessPID, &agent.CodePath, &agent.PublishedAt, &agent.CreatedAt, &agent.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}

		def, err := DBAgentToDefinition(&agent)
		if err != nil {
			return nil, fmt.Errorf("convert agent: %w", err)
		}
		agents = append(agents, *def)
	}

	return agents, nil
}

func (s *MySQLStorage) DeleteUserAgent(ctx context.Context, agentID string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM user_agent WHERE agent_id = ?", agentID)
	if err != nil {
		return fmt.Errorf("delete agent: %w", err)
	}
	return nil
}

func (s *MySQLStorage) DeleteUserAgentScoped(ctx context.Context, agentID string, requesterUserID string, allowSystem bool, allowAll bool) error {
	def, err := s.GetUserAgentScoped(ctx, agentID, requesterUserID, allowSystem, allowAll)
	if err != nil {
		return err
	}
	return s.DeleteUserAgent(ctx, def.AgentID)
}

func (s *MySQLStorage) UpdateAgentStatus(ctx context.Context, agentID string, status AgentStatus, port int, processPID int) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE user_agent SET status = ?, port = ?, process_pid = ?, updated_at = NOW() WHERE agent_id = ?",
		status, port, processPID, agentID)
	if err != nil {
		return fmt.Errorf("update agent status: %w", err)
	}
	return nil
}

func (s *MySQLStorage) GetPublishedAgents(ctx context.Context) ([]UserAgentDefinition, error) {
	query := "SELECT id, agent_id, user_id, name, description, workflow_id, status, port, process_pid, code_path, published_at, created_at, updated_at FROM user_agent WHERE status = ?"
	rows, err := s.db.QueryContext(ctx, query, AgentStatusPublished)
	if err != nil {
		return nil, fmt.Errorf("query published agents: %w", err)
	}
	defer rows.Close()

	var agents []UserAgentDefinition
	for rows.Next() {
		var agent UserAgent
		err := rows.Scan(&agent.ID, &agent.AgentID, &agent.UserID, &agent.Name, &agent.Description, &agent.WorkflowID, &agent.Status, &agent.Port, &agent.ProcessPID, &agent.CodePath, &agent.PublishedAt, &agent.CreatedAt, &agent.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}

		def, err := DBAgentToDefinition(&agent)
		if err != nil {
			return nil, fmt.Errorf("convert agent: %w", err)
		}
		agents = append(agents, *def)
	}

	return agents, nil
}

func (s *MySQLStorage) CreateUser(ctx context.Context, user *UserAccount) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO users (user_id, username, display_name, password_hash, status) VALUES (?, ?, ?, ?, ?)",
		user.UserID, user.Username, user.DisplayName, user.PasswordHash, user.Status)
	if err != nil {
		return fmt.Errorf("insert user: %w", err)
	}
	return nil
}

func (s *MySQLStorage) EnsureDefaultRoles(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("mysql storage is not initialized")
	}
	for _, role := range defaultRoles {
		_, err := s.db.ExecContext(ctx,
			"INSERT INTO role (role_code, role_name, description, status) VALUES (?, ?, ?, ?) ON DUPLICATE KEY UPDATE role_name = VALUES(role_name), description = VALUES(description), status = VALUES(status), updated_at = NOW()",
			role.RoleCode, role.RoleName, role.Description, role.Status,
		)
		if err != nil {
			return fmt.Errorf("upsert role %s: %w", role.RoleCode, err)
		}
	}
	return nil
}

func (s *MySQLStorage) BindUserRole(ctx context.Context, userID string, roleCode string) error {
	userID = strings.TrimSpace(userID)
	roleCode = strings.TrimSpace(strings.ToLower(roleCode))
	if userID == "" || roleCode == "" {
		return fmt.Errorf("userID and roleCode are required")
	}

	var userCount int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM users WHERE user_id = ?",
		userID,
	).Scan(&userCount); err != nil {
		return fmt.Errorf("check user exists: %w", err)
	}
	if userCount == 0 {
		return fmt.Errorf("user not found")
	}

	var count int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM role WHERE role_code = ? AND status = 1",
		roleCode,
	).Scan(&count); err != nil {
		return fmt.Errorf("validate role %s: %w", roleCode, err)
	}
	if count == 0 {
		return fmt.Errorf("role not found: %s", roleCode)
	}

	if _, err := s.db.ExecContext(ctx,
		"UPDATE users SET role_code = ?, updated_at = NOW() WHERE user_id = ?",
		roleCode, userID,
	); err != nil {
		return fmt.Errorf("update user role: %w", err)
	}
	return nil
}

func (s *MySQLStorage) ListUserRoles(ctx context.Context, userID string) ([]Role, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, fmt.Errorf("userID is required")
	}
	var role Role
	err := s.db.QueryRowContext(ctx,
		`SELECT r.id, r.role_code, r.role_name, IFNULL(r.description, ''), r.status, r.created_at, r.updated_at
		 FROM users u
		 INNER JOIN role r ON r.role_code = u.role_code
		 WHERE u.user_id = ? AND r.status = 1`,
		userID,
	).Scan(&role.ID, &role.RoleCode, &role.RoleName, &role.Description, &role.Status, &role.CreatedAt, &role.UpdatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return []Role{}, nil
		}
		return nil, fmt.Errorf("list user roles: %w", err)
	}
	return []Role{role}, nil
}

func (s *MySQLStorage) GetUserByUsername(ctx context.Context, username string) (*UserAccount, error) {
	var user UserAccount
	err := s.db.QueryRowContext(ctx,
		"SELECT id, user_id, username, IFNULL(display_name, ''), password_hash, status, created_at, updated_at FROM users WHERE username = ?",
		username,
	).Scan(&user.ID, &user.UserID, &user.Username, &user.DisplayName, &user.PasswordHash, &user.Status, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("user not found")
		}
		return nil, fmt.Errorf("query user by username: %w", err)
	}
	return &user, nil
}

func (s *MySQLStorage) GetUserByUserID(ctx context.Context, userID string) (*UserAccount, error) {
	var user UserAccount
	err := s.db.QueryRowContext(ctx,
		"SELECT id, user_id, username, IFNULL(display_name, ''), password_hash, status, created_at, updated_at FROM users WHERE user_id = ?",
		userID,
	).Scan(&user.ID, &user.UserID, &user.Username, &user.DisplayName, &user.PasswordHash, &user.Status, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("user not found")
		}
		return nil, fmt.Errorf("query user by user_id: %w", err)
	}
	return &user, nil
}

func (s *MySQLStorage) ListUsers(ctx context.Context) ([]UserAccount, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, user_id, username, IFNULL(display_name, ''), password_hash, status, created_at, updated_at FROM users ORDER BY created_at DESC",
	)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	users := make([]UserAccount, 0)
	for rows.Next() {
		var user UserAccount
		if scanErr := rows.Scan(&user.ID, &user.UserID, &user.Username, &user.DisplayName, &user.PasswordHash, &user.Status, &user.CreatedAt, &user.UpdatedAt); scanErr != nil {
			return nil, fmt.Errorf("scan user: %w", scanErr)
		}
		user.PasswordHash = ""
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate users: %w", err)
	}
	return users, nil
}

func (s *MySQLStorage) UpdateUserStatus(ctx context.Context, userID string, status int8) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return fmt.Errorf("userID is required")
	}
	if status != 0 && status != 1 {
		return fmt.Errorf("status must be 0 or 1")
	}
	res, err := s.db.ExecContext(ctx,
		"UPDATE users SET status = ?, updated_at = NOW() WHERE user_id = ?",
		status, userID,
	)
	if err != nil {
		return fmt.Errorf("update user status: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("user not found")
	}
	return nil
}

func (s *MySQLStorage) ReplaceUserRoles(ctx context.Context, userID string, roleCodes []string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return fmt.Errorf("userID is required")
	}
	var userCount int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE user_id = ?", userID).Scan(&userCount); err != nil {
		return fmt.Errorf("check user exists: %w", err)
	}
	if userCount == 0 {
		return fmt.Errorf("user not found")
	}
	code, err := pickSingleRoleCode(roleCodes)
	if err != nil {
		return err
	}
	return s.BindUserRole(ctx, userID, code)
}

func (s *MySQLStorage) UpdateUserDisplayName(ctx context.Context, userID string, displayName string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE users SET display_name = ?, updated_at = NOW() WHERE user_id = ?",
		displayName, userID)
	if err != nil {
		return fmt.Errorf("update user display name: %w", err)
	}
	return nil
}

func (s *MySQLStorage) UpdateUserPasswordHash(ctx context.Context, userID string, passwordHash string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE users SET password_hash = ?, updated_at = NOW() WHERE user_id = ?",
		passwordHash, userID)
	if err != nil {
		return fmt.Errorf("update user password hash: %w", err)
	}
	return nil
}

func (s *MySQLStorage) SaveRefreshToken(ctx context.Context, token *UserRefreshToken) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO user_refresh_token (token_id, user_id, token_hash, expires_at) VALUES (?, ?, ?, ?)",
		token.TokenID, token.UserID, token.TokenHash, token.ExpiresAt)
	if err != nil {
		return fmt.Errorf("insert refresh token: %w", err)
	}
	return nil
}

func (s *MySQLStorage) GetRefreshToken(ctx context.Context, tokenHash string) (*UserRefreshToken, error) {
	var t UserRefreshToken
	err := s.db.QueryRowContext(ctx,
		"SELECT id, token_id, user_id, token_hash, expires_at, revoked_at, created_at FROM user_refresh_token WHERE token_hash = ?",
		tokenHash,
	).Scan(&t.ID, &t.TokenID, &t.UserID, &t.TokenHash, &t.ExpiresAt, &t.RevokedAt, &t.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("refresh token not found")
		}
		return nil, fmt.Errorf("query refresh token: %w", err)
	}
	return &t, nil
}

func (s *MySQLStorage) RevokeRefreshTokenByHash(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE user_refresh_token SET revoked_at = NOW() WHERE token_hash = ? AND revoked_at IS NULL",
		tokenHash)
	if err != nil {
		return fmt.Errorf("revoke refresh token: %w", err)
	}
	return nil
}

func (s *MySQLStorage) RevokeRefreshTokensByUserID(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE user_refresh_token SET revoked_at = NOW() WHERE user_id = ? AND revoked_at IS NULL",
		userID)
	if err != nil {
		return fmt.Errorf("revoke user refresh tokens: %w", err)
	}
	return nil
}

func (s *MySQLStorage) IsRefreshTokenActive(token *UserRefreshToken, now time.Time) bool {
	if token == nil {
		return false
	}
	if token.RevokedAt.Valid {
		return false
	}
	return token.ExpiresAt.After(now)
}
