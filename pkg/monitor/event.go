package monitor

import "time"

type EventType string

type EventStatus string

const (
	EventTypeWorkflowStarted  EventType = "workflow_started"
	EventTypeWorkflowFinished EventType = "workflow_finished"
	EventTypeWorkflowFailed   EventType = "workflow_failed"
	EventTypeNodeStarted      EventType = "node_started"
	EventTypeNodeFinished     EventType = "node_finished"
	EventTypeNodeFailed       EventType = "node_failed"
	EventTypeModelCalled      EventType = "model_called"
	EventTypeAgentCalled      EventType = "agent_called"
	EventTypeToolCalled       EventType = "tool_called"
	EventTypeRetryTriggered   EventType = "retry_triggered"
	EventTypeTimeoutTriggered EventType = "timeout_triggered"
	EventTypeAlertTriggered   EventType = "alert_triggered"
)

const (
	StatusPending   EventStatus = "pending"
	StatusRunning   EventStatus = "running"
	StatusSucceeded EventStatus = "succeeded"
	StatusFailed    EventStatus = "failed"
	StatusTimeout   EventStatus = "timeout"
	StatusRetrying  EventStatus = "retrying"
)

type CreateRunInput struct {
	RunID         string
	WorkflowID    string
	UserID        string
	SourceAgentID string
	TaskID        string
	Status        EventStatus
	StartedAt     time.Time
}

type FinishRunInput struct {
	RunID        string
	Status       EventStatus
	FinishedAt   time.Time
	DurationMs   int64
	ErrorMessage string
}

type AppendEventInput struct {
	EventID        string
	RunID          string
	TaskID         string
	WorkflowID     string
	UserID         string
	AgentID        string
	NodeID         string
	EventType      EventType
	Status         EventStatus
	Message        string
	InputSnapshot  any
	OutputSnapshot any
	ErrorMessage   string
	DurationMs     int64
}

type TriggerAlertInput struct {
	AlertID     string
	RunID       string
	WorkflowID  string
	TaskID      string
	UserID      string
	AgentID     string
	NodeID      string
	AlertType   string
	Severity    string
	Title       string
	Content     string
	Status      string
	TriggeredAt time.Time
}

type ListRunsInput struct {
	UserID     string
	WorkflowID string
	TaskID     string
	Status     string
	Page       int
	PageSize   int
}

type ListRunEventsInput struct {
	RunID    string
	Page     int
	PageSize int
}

type ListAlertsInput struct {
	UserID     string
	RunID      string
	WorkflowID string
	Status     string
	Page       int
	PageSize   int
}
