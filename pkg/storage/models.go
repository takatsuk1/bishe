package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

const (
	metaKeyNodeConfigJSON = "__node_config_json"
	metaKeyInputType      = "__input_type"
	metaKeyOutputType     = "__output_type"
)

type UserWorkflow struct {
	ID          int64     `json:"id"`
	WorkflowID  string    `json:"workflowId"`
	UserID      string    `json:"userId"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	StartNodeID string    `json:"startNodeId"`
	Status      int8      `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type WorkflowNode struct {
	ID         int64           `json:"id"`
	WorkflowID string          `json:"workflowId"`
	NodeID     string          `json:"nodeId"`
	NodeType   string          `json:"nodeType"`
	AgentID    sql.NullString  `json:"agentId"`
	TaskType   sql.NullString  `json:"taskType"`
	PreInput   sql.NullString  `json:"preInput"`
	LoopConfig json.RawMessage `json:"loopConfig"`
	Metadata   json.RawMessage `json:"metadata"`
	CreatedAt  time.Time       `json:"createdAt"`
	UpdatedAt  time.Time       `json:"updatedAt"`
}

type WorkflowEdge struct {
	ID         int64           `json:"id"`
	WorkflowID string          `json:"workflowId"`
	FromNodeID string          `json:"fromNodeId"`
	ToNodeID   string          `json:"toNodeId"`
	Label      sql.NullString  `json:"label"`
	Mapping    json.RawMessage `json:"mapping"`
	SortOrder  int             `json:"sortOrder"`
	CreatedAt  time.Time       `json:"createdAt"`
	UpdatedAt  time.Time       `json:"updatedAt"`
}

type WorkflowDefinition struct {
	WorkflowID  string    `json:"workflowId"`
	UserID      string    `json:"userId"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	StartNodeID string    `json:"startNodeId"`
	Nodes       []NodeDef `json:"nodes"`
	Edges       []EdgeDef `json:"edges"`
}

type NodeDef struct {
	ID         string                 `json:"id"`
	Type       string                 `json:"type"`
	Config     map[string]interface{} `json:"config,omitempty"`
	AgentID    string                 `json:"agentId,omitempty"`
	TaskType   string                 `json:"taskType,omitempty"`
	InputType  string                 `json:"inputType,omitempty"`
	OutputType string                 `json:"outputType,omitempty"`
	Condition  string                 `json:"condition,omitempty"`
	PreInput   string                 `json:"preInput,omitempty"`
	LoopConfig map[string]interface{} `json:"loopConfig,omitempty"`
	Metadata   map[string]string      `json:"metadata,omitempty"`
}

type EdgeDef struct {
	From    string                 `json:"from"`
	To      string                 `json:"to"`
	Label   string                 `json:"label,omitempty"`
	Mapping map[string]interface{} `json:"mapping,omitempty"`
}

type WorkflowSummary struct {
	WorkflowID  string    `json:"workflowId"`
	UserID      string    `json:"userId"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Status      int8      `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	NodeCount   int       `json:"nodeCount"`
}

type DBNode struct {
	WorkflowID string
	NodeID     string
	NodeType   string
	AgentID    sql.NullString
	TaskType   sql.NullString
	PreInput   sql.NullString
	LoopConfig json.RawMessage
	Metadata   json.RawMessage
}

type DBEdge struct {
	WorkflowID string
	FromNodeID string
	ToNodeID   string
	Label      sql.NullString
	Mapping    json.RawMessage
	SortOrder  int
}

func (n *NodeDef) ToDBNode(workflowID string) (*WorkflowNode, error) {
	node := &WorkflowNode{
		WorkflowID: workflowID,
		NodeID:     n.ID,
		NodeType:   n.Type,
	}

	if n.AgentID != "" {
		node.AgentID = sql.NullString{String: n.AgentID, Valid: true}
	}
	if n.TaskType != "" {
		node.TaskType = sql.NullString{String: n.TaskType, Valid: true}
	}
	if n.PreInput != "" {
		node.PreInput = sql.NullString{String: n.PreInput, Valid: true}
	}
	if n.LoopConfig != nil {
		data, err := json.Marshal(n.LoopConfig)
		if err != nil {
			return nil, fmt.Errorf("marshal loop config: %w", err)
		}
		node.LoopConfig = data
	}
	if n.Metadata != nil {
		meta := make(map[string]string, len(n.Metadata)+3)
		for k, v := range n.Metadata {
			meta[k] = v
		}
		if n.InputType != "" {
			meta[metaKeyInputType] = n.InputType
		}
		if n.OutputType != "" {
			meta[metaKeyOutputType] = n.OutputType
		}
		if n.Config != nil {
			if cfg, err := json.Marshal(n.Config); err == nil {
				meta[metaKeyNodeConfigJSON] = string(cfg)
			}
		}
		data, err := json.Marshal(meta)
		if err != nil {
			return nil, fmt.Errorf("marshal metadata: %w", err)
		}
		node.Metadata = data
	} else if n.InputType != "" || n.OutputType != "" || n.Config != nil {
		meta := map[string]string{}
		if n.InputType != "" {
			meta[metaKeyInputType] = n.InputType
		}
		if n.OutputType != "" {
			meta[metaKeyOutputType] = n.OutputType
		}
		if n.Config != nil {
			if cfg, err := json.Marshal(n.Config); err == nil {
				meta[metaKeyNodeConfigJSON] = string(cfg)
			}
		}
		if len(meta) > 0 {
			data, err := json.Marshal(meta)
			if err != nil {
				return nil, fmt.Errorf("marshal metadata: %w", err)
			}
			node.Metadata = data
		}
	}

	return node, nil
}

func (e *EdgeDef) ToDBEdge(workflowID string, sortOrder int) (*WorkflowEdge, error) {
	edge := &WorkflowEdge{
		WorkflowID: workflowID,
		FromNodeID: e.From,
		ToNodeID:   e.To,
		SortOrder:  sortOrder,
	}

	if e.Label != "" {
		edge.Label = sql.NullString{String: e.Label, Valid: true}
	}
	if e.Mapping != nil {
		data, err := json.Marshal(e.Mapping)
		if err != nil {
			return nil, fmt.Errorf("marshal mapping: %w", err)
		}
		edge.Mapping = data
	}

	return edge, nil
}

func DBNodeToNodeDef(node *WorkflowNode) (*NodeDef, error) {
	n := &NodeDef{
		ID:       node.NodeID,
		Type:     node.NodeType,
		AgentID:  node.AgentID.String,
		TaskType: node.TaskType.String,
		PreInput: node.PreInput.String,
	}

	if node.LoopConfig != nil {
		var loopConfig map[string]interface{}
		if err := json.Unmarshal(node.LoopConfig, &loopConfig); err != nil {
			return nil, fmt.Errorf("unmarshal loop config: %w", err)
		}
		n.LoopConfig = loopConfig
	}

	if node.Metadata != nil {
		var metadata map[string]string
		if err := json.Unmarshal(node.Metadata, &metadata); err != nil {
			return nil, fmt.Errorf("unmarshal metadata: %w", err)
		}
		if inputType, ok := metadata[metaKeyInputType]; ok {
			n.InputType = inputType
			delete(metadata, metaKeyInputType)
		}
		if outputType, ok := metadata[metaKeyOutputType]; ok {
			n.OutputType = outputType
			delete(metadata, metaKeyOutputType)
		}
		if cfgJSON, ok := metadata[metaKeyNodeConfigJSON]; ok && cfgJSON != "" {
			var cfg map[string]interface{}
			if err := json.Unmarshal([]byte(cfgJSON), &cfg); err == nil {
				n.Config = cfg
			}
			delete(metadata, metaKeyNodeConfigJSON)
		}
		n.Metadata = metadata
	}

	return n, nil
}

func DBEdgeToEdgeDef(edge *WorkflowEdge) (*EdgeDef, error) {
	e := &EdgeDef{
		From:  edge.FromNodeID,
		To:    edge.ToNodeID,
		Label: edge.Label.String,
	}

	if edge.Mapping != nil {
		var mapping map[string]interface{}
		if err := json.Unmarshal(edge.Mapping, &mapping); err != nil {
			return nil, fmt.Errorf("unmarshal mapping: %w", err)
		}
		e.Mapping = mapping
	}

	return e, nil
}

type UserTool struct {
	ID               int64           `json:"id"`
	ToolID           string          `json:"toolId"`
	UserID           string          `json:"userId"`
	Name             string          `json:"name"`
	Description      string          `json:"description"`
	ToolType         string          `json:"toolType"`
	Config           json.RawMessage `json:"config"`
	Parameters       json.RawMessage `json:"parameters"`
	OutputParameters json.RawMessage `json:"outputParameters"`
	Status           int8            `json:"status"`
	CreatedAt        time.Time       `json:"createdAt"`
	UpdatedAt        time.Time       `json:"updatedAt"`
}

type UserToolDefinition struct {
	ToolID           string                 `json:"toolId"`
	UserID           string                 `json:"userId"`
	Name             string                 `json:"name"`
	Description      string                 `json:"description"`
	ToolType         string                 `json:"toolType"`
	Config           map[string]interface{} `json:"config"`
	Parameters       []ToolParameterDef     `json:"parameters"`
	OutputParameters []ToolParameterDef     `json:"outputParameters"`
}

type ToolParameterDef struct {
	Name        string        `json:"name"`
	Type        string        `json:"type"`
	Required    bool          `json:"required"`
	Description string        `json:"description"`
	Default     interface{}   `json:"default,omitempty"`
	Enum        []interface{} `json:"enum,omitempty"`
}

func (t *UserToolDefinition) ToDBTool() (*UserTool, error) {
	tool := &UserTool{
		ToolID:      t.ToolID,
		UserID:      t.UserID,
		Name:        t.Name,
		Description: t.Description,
		ToolType:    t.ToolType,
		Status:      1,
	}

	configData, err := json.Marshal(t.Config)
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}
	tool.Config = configData

	if t.Parameters != nil {
		paramsData, err := json.Marshal(t.Parameters)
		if err != nil {
			return nil, fmt.Errorf("marshal parameters: %w", err)
		}
		tool.Parameters = paramsData
	}

	if t.OutputParameters != nil {
		outputParamsData, err := json.Marshal(t.OutputParameters)
		if err != nil {
			return nil, fmt.Errorf("marshal output parameters: %w", err)
		}
		tool.OutputParameters = outputParamsData
	}

	return tool, nil
}

func DBToolToDefinition(tool *UserTool) (*UserToolDefinition, error) {
	def := &UserToolDefinition{
		ToolID:      tool.ToolID,
		UserID:      tool.UserID,
		Name:        tool.Name,
		Description: tool.Description,
		ToolType:    tool.ToolType,
	}

	if tool.Config != nil {
		var config map[string]interface{}
		if err := json.Unmarshal(tool.Config, &config); err != nil {
			return nil, fmt.Errorf("unmarshal config: %w", err)
		}
		def.Config = config
	}

	if tool.Parameters != nil {
		var params []ToolParameterDef
		if err := json.Unmarshal(tool.Parameters, &params); err != nil {
			return nil, fmt.Errorf("unmarshal parameters: %w", err)
		}
		def.Parameters = params
	}

	if tool.OutputParameters != nil {
		var outputParams []ToolParameterDef
		if err := json.Unmarshal(tool.OutputParameters, &outputParams); err != nil {
			return nil, fmt.Errorf("unmarshal output parameters: %w", err)
		}
		def.OutputParameters = outputParams
	}

	return def, nil
}

type AgentStatus string

const (
	AgentStatusDraft     AgentStatus = "draft"
	AgentStatusTesting   AgentStatus = "testing"
	AgentStatusPublished AgentStatus = "published"
	AgentStatusStopped   AgentStatus = "stopped"
)

type UserAgent struct {
	ID          int64          `json:"id"`
	AgentID     string         `json:"agentId"`
	UserID      string         `json:"userId"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	WorkflowID  string         `json:"workflowId"`
	Status      AgentStatus    `json:"status"`
	Port        sql.NullInt64  `json:"port"`
	ProcessPID  sql.NullInt64  `json:"processPid"`
	CodePath    sql.NullString `json:"codePath"`
	PublishedAt sql.NullTime   `json:"publishedAt"`
	CreatedAt   time.Time      `json:"createdAt"`
	UpdatedAt   time.Time      `json:"updatedAt"`
}

type UserAgentDefinition struct {
	AgentID     string      `json:"agentId"`
	UserID      string      `json:"userId"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	WorkflowID  string      `json:"workflowId"`
	Status      AgentStatus `json:"status"`
	Port        int         `json:"port,omitempty"`
	ProcessPID  int         `json:"processPid,omitempty"`
	CodePath    string      `json:"codePath,omitempty"`
	PublishedAt *time.Time  `json:"publishedAt,omitempty"`
}

type UserAccount struct {
	ID           int64     `json:"id"`
	UserID       string    `json:"userId"`
	Username     string    `json:"username"`
	DisplayName  string    `json:"displayName"`
	PasswordHash string    `json:"-"`
	Status       int8      `json:"status"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type Role struct {
	ID          int64     `json:"id"`
	RoleCode    string    `json:"roleCode"`
	RoleName    string    `json:"roleName"`
	Description string    `json:"description"`
	Status      int8      `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type UserRole struct {
	ID        int64     `json:"id"`
	UserID    string    `json:"userId"`
	RoleCode  string    `json:"roleCode"`
	CreatedAt time.Time `json:"createdAt"`
}

type UserRefreshToken struct {
	ID        int64        `json:"id"`
	TokenID   string       `json:"tokenId"`
	UserID    string       `json:"userId"`
	TokenHash string       `json:"-"`
	ExpiresAt time.Time    `json:"expiresAt"`
	RevokedAt sql.NullTime `json:"revokedAt"`
	CreatedAt time.Time    `json:"createdAt"`
}

func (a *UserAgentDefinition) ToDBAgent() (*UserAgent, error) {
	agent := &UserAgent{
		AgentID:     a.AgentID,
		UserID:      a.UserID,
		Name:        a.Name,
		Description: a.Description,
		WorkflowID:  a.WorkflowID,
		Status:      a.Status,
	}

	if a.Port > 0 {
		agent.Port = sql.NullInt64{Int64: int64(a.Port), Valid: true}
	}
	if a.ProcessPID > 0 {
		agent.ProcessPID = sql.NullInt64{Int64: int64(a.ProcessPID), Valid: true}
	}
	if a.CodePath != "" {
		agent.CodePath = sql.NullString{String: a.CodePath, Valid: true}
	}
	if a.PublishedAt != nil {
		agent.PublishedAt = sql.NullTime{Time: *a.PublishedAt, Valid: true}
	}

	return agent, nil
}

func DBAgentToDefinition(agent *UserAgent) (*UserAgentDefinition, error) {
	def := &UserAgentDefinition{
		AgentID:     agent.AgentID,
		UserID:      agent.UserID,
		Name:        agent.Name,
		Description: agent.Description,
		WorkflowID:  agent.WorkflowID,
		Status:      agent.Status,
	}

	if agent.Port.Valid {
		def.Port = int(agent.Port.Int64)
	}
	if agent.ProcessPID.Valid {
		def.ProcessPID = int(agent.ProcessPID.Int64)
	}
	if agent.CodePath.Valid {
		def.CodePath = agent.CodePath.String
	}
	if agent.PublishedAt.Valid {
		def.PublishedAt = &agent.PublishedAt.Time
	}

	return def, nil
}

type MonitorRun struct {
	ID            int64      `json:"id"`
	RunID         string     `json:"runId"`
	WorkflowID    string     `json:"workflowId"`
	UserID        string     `json:"userId"`
	SourceAgentID string     `json:"sourceAgentId,omitempty"`
	TaskID        string     `json:"taskId,omitempty"`
	Status        string     `json:"status"`
	StartedAt     time.Time  `json:"startedAt"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
	DurationMs    int64      `json:"durationMs"`
	CurrentNodeID string     `json:"currentNodeId,omitempty"`
	ErrorMessage  string     `json:"errorMessage,omitempty"`
	AlertCount    int        `json:"alertCount"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

type MonitorEvent struct {
	ID             int64     `json:"id"`
	EventID        string    `json:"eventId"`
	RunID          string    `json:"runId"`
	TaskID         string    `json:"taskId,omitempty"`
	WorkflowID     string    `json:"workflowId"`
	UserID         string    `json:"userId"`
	AgentID        string    `json:"agentId,omitempty"`
	NodeID         string    `json:"nodeId,omitempty"`
	EventType      string    `json:"eventType"`
	Status         string    `json:"status"`
	Message        string    `json:"message,omitempty"`
	InputSnapshot  string    `json:"inputSnapshot,omitempty"`
	OutputSnapshot string    `json:"outputSnapshot,omitempty"`
	ErrorMessage   string    `json:"errorMessage,omitempty"`
	DurationMs     int64     `json:"durationMs"`
	CreatedAt      time.Time `json:"createdAt"`
}

type MonitorAlert struct {
	ID          int64      `json:"id"`
	AlertID     string     `json:"alertId"`
	RunID       string     `json:"runId"`
	WorkflowID  string     `json:"workflowId"`
	AgentID     string     `json:"agentId,omitempty"`
	NodeID      string     `json:"nodeId,omitempty"`
	AlertType   string     `json:"alertType"`
	Severity    string     `json:"severity"`
	Title       string     `json:"title"`
	Content     string     `json:"content,omitempty"`
	Status      string     `json:"status"`
	TriggeredAt time.Time  `json:"triggeredAt"`
	ResolvedAt  *time.Time `json:"resolvedAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
}

type MonitorRunQuery struct {
	UserID     string
	WorkflowID string
	TaskID     string
	Status     string
	Page       int
	PageSize   int
}

type MonitorEventQuery struct {
	RunID    string
	Page     int
	PageSize int
}

type MonitorAlertQuery struct {
	UserID     string
	RunID      string
	WorkflowID string
	Status     string
	Page       int
	PageSize   int
}

type MonitorOverview struct {
	TotalRuns         int64        `json:"totalRuns"`
	SucceededRuns     int64        `json:"succeededRuns"`
	FailedRuns        int64        `json:"failedRuns"`
	SuccessRate       float64      `json:"successRate"`
	AverageDurationMs int64        `json:"averageDurationMs"`
	AlertTotal        int64        `json:"alertTotal"`
	RecentRuns        []MonitorRun `json:"recentRuns"`
}

type MonitorRunDetail struct {
	Run               MonitorRun       `json:"run"`
	AlertCount        int64            `json:"alertCount"`
	NodeStatusSummary map[string]int64 `json:"nodeStatusSummary"`
	LatestError       string           `json:"latestError,omitempty"`
}

type MonitorRunFamily struct {
	RootRunID string       `json:"rootRunId"`
	Runs      []MonitorRun `json:"runs"`
}
