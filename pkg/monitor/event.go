package monitor

import "time"

// EventType 事件类型
type EventType string

// EventStatus 事件状态
type EventStatus string

const (
	EventTypeWorkflowStarted  EventType = "workflow_started"  // 工作流开始
	EventTypeWorkflowFinished EventType = "workflow_finished" // 工作流完成
	EventTypeWorkflowFailed   EventType = "workflow_failed"   // 工作流失败
	EventTypeNodeStarted      EventType = "node_started"      // 节点开始
	EventTypeNodeFinished     EventType = "node_finished"     // 节点完成
	EventTypeNodeFailed       EventType = "node_failed"       // 节点失败
	EventTypeModelCalled      EventType = "model_called"      // 模型调用
	EventTypeAgentCalled      EventType = "agent_called"      // 代理调用
	EventTypeToolCalled       EventType = "tool_called"       // 工具调用
	EventTypeRetryTriggered   EventType = "retry_triggered"   // 重试触发
	EventTypeTimeoutTriggered EventType = "timeout_triggered" // 超时触发
	EventTypeAlertTriggered   EventType = "alert_triggered"   // 警报触发
)

const (
	StatusPending   EventStatus = "pending"   // 待处理
	StatusRunning   EventStatus = "running"   // 运行中
	StatusSucceeded EventStatus = "succeeded" // 成功
	StatusFailed    EventStatus = "failed"    // 失败
	StatusTimeout   EventStatus = "timeout"   // 超时
	StatusRetrying  EventStatus = "retrying"  // 重试中
)

// CreateRunInput 创建运行输入
type CreateRunInput struct {
	RunID         string      // 运行ID
	WorkflowID    string      // 工作流ID
	UserID        string      // 用户ID
	SourceAgentID string      // 源代理ID
	TaskID        string      // 任务ID
	Status        EventStatus // 状态
	StartedAt     time.Time   // 开始时间
}

// FinishRunInput 完成运行输入
type FinishRunInput struct {
	RunID        string      // 运行ID
	Status       EventStatus // 状态
	FinishedAt   time.Time   // 完成时间
	DurationMs   int64       // 持续时间（毫秒）
	ErrorMessage string      // 错误信息
}

// AppendEventInput 添加事件输入
type AppendEventInput struct {
	EventID        string      // 事件ID
	RunID          string      // 运行ID
	TaskID         string      // 任务ID
	WorkflowID     string      // 工作流ID
	UserID         string      // 用户ID
	AgentID        string      // 代理ID
	NodeID         string      // 节点ID
	EventType      EventType   // 事件类型
	Status         EventStatus // 状态
	Message        string      // 消息
	InputSnapshot  any         // 输入快照
	OutputSnapshot any         // 输出快照
	ErrorMessage   string      // 错误信息
	DurationMs     int64       // 持续时间（毫秒）
}

// TriggerAlertInput 触发警报输入
type TriggerAlertInput struct {
	AlertID     string    // 警报ID
	RunID       string    // 运行ID
	WorkflowID  string    // 工作流ID
	TaskID      string    // 任务ID
	UserID      string    // 用户ID
	AgentID     string    // 代理ID
	NodeID      string    // 节点ID
	AlertType   string    // 警报类型
	Severity    string    // 严重程度
	Title       string    // 标题
	Content     string    // 内容
	Status      string    // 状态
	TriggeredAt time.Time // 触发时间
}

// ListRunsInput 列出运行输入
type ListRunsInput struct {
	UserID     string // 用户ID
	WorkflowID string // 工作流ID
	TaskID     string // 任务ID
	Status     string // 状态
	Page       int    // 页码
	PageSize   int    // 每页大小
}

// ListRunEventsInput 列出运行事件输入
type ListRunEventsInput struct {
	RunID    string // 运行ID
	Page     int    // 页码
	PageSize int    // 每页大小
}

// ListAlertsInput 列出警报输入
type ListAlertsInput struct {
	UserID     string // 用户ID
	RunID      string // 运行ID
	WorkflowID string // 工作流ID
	Status     string // 状态
	Page       int    // 页码
	PageSize   int    // 每页大小
}
