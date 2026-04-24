package noncore_service

type AgentInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type WorkflowSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	UpdatedAt string `json:"updatedAt"`
}

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

type PortDefinition struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

type LoopConfig struct {
	MaxIterations int    `json:"maxIterations"`
	ContinueTo    string `json:"continueTo"`
	ExitTo        string `json:"exitTo"`
}

type EdgeDefinition struct {
	From    string            `json:"from"`
	To      string            `json:"to"`
	Label   string            `json:"label,omitempty"`
	Mapping map[string]string `json:"mapping,omitempty"`
}

type WorkflowDefinition struct {
	ID            string           `json:"id"`
	Name          string           `json:"name"`
	Description   string           `json:"description,omitempty"`
	SchemaVersion int              `json:"schemaVersion,omitempty"`
	StartNodeId   string           `json:"startNodeId"`
	Nodes         []NodeDefinition `json:"nodes"`
	Edges         []EdgeDefinition `json:"edges"`
}

type WorkflowGetResponse struct {
	Definition WorkflowDefinition `json:"definition"`
	UpdatedAt  string             `json:"updatedAt"`
}

type NodeRunResult struct {
	NodeID   string                 `json:"nodeId"`
	TaskID   string                 `json:"taskId"`
	State    string                 `json:"state"`
	Output   map[string]interface{} `json:"output,omitempty"`
	ErrorMsg string                 `json:"errorMsg,omitempty"`
}

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
