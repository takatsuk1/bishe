package protocol

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// MessageRole 表示消息角色
type MessageRole string

// 消息角色常量定义
const (
	// MessageRoleUser 表示用户角色
	MessageRoleUser  MessageRole = "user"
	// MessageRoleAgent 表示代理角色
	MessageRoleAgent MessageRole = "agent"
)

// PartType 表示消息部分类型
type PartType string

// 消息部分类型常量定义
const (
	// PartTypeText 表示文本类型
	PartTypeText PartType = "text"
)

// Part 表示消息的一个部分
type Part struct {
	// Type 部分类型
	Type PartType `json:"type"`
	// Text 文本内容（仅当类型为文本时使用）
	Text string   `json:"text,omitempty"`
}

// NewTextPart 创建一个新的文本部分
// text: 文本内容
// 返回创建的文本部分
func NewTextPart(text string) Part {
	return Part{Type: PartTypeText, Text: text}
}

// Message 表示一条消息
type Message struct {
	// Role 消息角色
	Role     MessageRole    `json:"role"`
	// Parts 消息部分列表
	Parts    []Part         `json:"parts"`
	// TaskID 任务ID（可选）
	TaskID   *string        `json:"task_id,omitempty"`
	// Metadata 元数据（可选）
	Metadata map[string]any `json:"metadata,omitempty"`
	// TimeUnix 时间戳（可选）
	TimeUnix int64          `json:"time_unix,omitempty"`
}

// SendMessageRequest 表示发送消息的请求
type SendMessageRequest struct {
	// Message 要发送的消息
	Message Message `json:"message"`
}

// SendMessageResponse 表示发送消息的响应
type SendMessageResponse struct {
	// TaskID 创建的任务ID
	TaskID string `json:"task_id"`
}

// CancelTaskResponse 表示取消任务的响应
type CancelTaskResponse struct {
	// Task 任务引用
	Task protocolTaskRef `json:"task"`
}

// protocolTaskRef 表示协议任务引用
type protocolTaskRef struct {
	// ID 任务ID
	ID string `json:"id"`
}

// FirstText 获取消息的第一个文本部分
// 返回第一个文本部分的内容，如果没有则返回空字符串
func (m Message) FirstText() string {
	for _, p := range m.Parts {
		if p.Type == PartTypeText {
			return p.Text
		}
	}
	return ""
}

// Validate 验证消息是否有效
// 返回可能的错误
func (m Message) Validate() error {
	// 检查消息角色是否为空
	if m.Role == "" {
		return errors.New("message role is required")
	}
	// 检查消息部分是否为空
	if len(m.Parts) == 0 {
		return errors.New("message parts are required")
	}
	// 检查每个消息部分
	for idx, part := range m.Parts {
		// 检查部分类型是否为空
		if part.Type == "" {
			return fmt.Errorf("message part[%d] type is required", idx)
		}
		// 检查文本类型的部分是否为空
		if part.Type == PartTypeText && strings.TrimSpace(part.Text) == "" {
			return fmt.Errorf("message part[%d] text is empty", idx)
		}
	}
	return nil
}

// TaskState 表示任务状态
type TaskState string

// 任务状态常量定义
const (
	// TaskStateQueued 表示任务已排队
	TaskStateQueued    TaskState = "queued"
	// TaskStateWorking 表示任务正在工作
	TaskStateWorking   TaskState = "working"
	// TaskStateCompleted 表示任务已完成
	TaskStateCompleted TaskState = "completed"
	// TaskStateFailed 表示任务失败
	TaskStateFailed    TaskState = "failed"
	// TaskStateCanceled 表示任务已取消
	TaskStateCanceled  TaskState = "canceled"
)

// IsTerminal 检查任务状态是否为终态
// 返回是否为终态
func (s TaskState) IsTerminal() bool {
	return s == TaskStateCompleted || s == TaskStateFailed || s == TaskStateCanceled
}

// CanTransition 检查任务是否可以从一个状态转换到另一个状态
// from: 源状态
// to: 目标状态
// 返回是否可以转换
func CanTransition(from, to TaskState) bool {
	switch from {
	case "":
		// 初始状态只能转换到排队状态
		return to == TaskStateQueued
	case TaskStateQueued:
		// 排队状态可以转换到工作或取消状态
		return to == TaskStateWorking || to == TaskStateCanceled
	case TaskStateWorking:
		// 工作状态可以转换到完成、失败或取消状态
		return to == TaskStateCompleted || to == TaskStateFailed || to == TaskStateCanceled
	case TaskStateCompleted, TaskStateFailed, TaskStateCanceled:
		// 终态不能转换到其他状态
		return false
	default:
		// 未知状态不能转换
		return false
	}
}

// TaskStatus 表示任务状态
type TaskStatus struct {
	// State 任务状态
	State     TaskState `json:"state"`
	// Message 消息（可选）
	Message   *Message  `json:"message,omitempty"`
	// Error 错误信息（可选）
	Error     string    `json:"error,omitempty"`
	// UpdatedAt 更新时间戳
	UpdatedAt int64     `json:"updated_at"`
}

// Task 表示一个任务
type Task struct {
	// ID 任务ID
	ID        string            `json:"id"`
	// Metadata 元数据（可选）
	Metadata  map[string]string `json:"metadata,omitempty"`
	// Status 任务状态
	Status    TaskStatus        `json:"status"`
	// CreatedAt 创建时间戳
	CreatedAt int64             `json:"created_at"`
}

// TaskStatusUpdateEvent 表示任务状态更新事件
type TaskStatusUpdateEvent struct {
	// TaskID 任务ID
	TaskID string     `json:"task_id"`
	// Status 任务状态
	Status TaskStatus `json:"status"`
}

// StreamEvent 表示流事件
type StreamEvent struct {
	// Type 事件类型
	Type             string                 `json:"type"`
	// TaskStatusUpdate 任务状态更新（可选）
	TaskStatusUpdate *TaskStatusUpdateEvent `json:"task_status_update,omitempty"`
	// Timestamp 时间戳
	Timestamp        int64                  `json:"timestamp"`
}

// AgentProvider 表示代理提供者
type AgentProvider struct {
	// Organization 组织名称（可选）
	Organization string `json:"organization,omitempty"`
}

// AgentCapabilities 表示代理能力
type AgentCapabilities struct {
	// PushNotifications 是否支持推送通知（可选）
	PushNotifications      *bool `json:"push_notifications,omitempty"`
	// StateTransitionHistory 是否支持状态转换历史（可选）
	StateTransitionHistory *bool `json:"state_transition_history,omitempty"`
}

// AgentSkill 表示代理技能
type AgentSkill struct {
	// ID 技能ID
	ID          string   `json:"id"`
	// Name 技能名称
	Name        string   `json:"name"`
	// Description 技能描述（可选）
	Description *string  `json:"description,omitempty"`
	// Tags 标签列表（可选）
	Tags        []string `json:"tags,omitempty"`
	// Examples 示例列表（可选）
	Examples    []string `json:"examples,omitempty"`
	// InputModes 输入模式列表（可选）
	InputModes  []string `json:"input_modes,omitempty"`
	// OutputModes 输出模式列表（可选）
	OutputModes []string `json:"output_modes,omitempty"`
}

// AgentCard 表示代理卡片
type AgentCard struct {
	// Name 代理名称
	Name               string            `json:"name"`
	// Description 代理描述（可选）
	Description        string            `json:"description,omitempty"`
	// Version 代理版本（可选）
	Version            string            `json:"version,omitempty"`
	// Provider 代理提供者（可选）
	Provider           *AgentProvider    `json:"provider,omitempty"`
	// Capabilities 代理能力
	Capabilities       AgentCapabilities `json:"capabilities,omitempty"`
	// DefaultInputModes 默认输入模式列表（可选）
	DefaultInputModes  []string          `json:"default_input_modes,omitempty"`
	// DefaultOutputModes 默认输出模式列表（可选）
	DefaultOutputModes []string          `json:"default_output_modes,omitempty"`
	// Skills 技能列表（可选）
	Skills             []AgentSkill      `json:"skills,omitempty"`
}

// NewTaskStatusEvent 创建一个新的任务状态事件
// taskID: 任务ID
// status: 任务状态
// 返回创建的流事件
func NewTaskStatusEvent(taskID string, status TaskStatus) StreamEvent {
	return StreamEvent{
		Type: "task_status_update",
		TaskStatusUpdate: &TaskStatusUpdateEvent{
			TaskID: taskID,
			Status: status,
		},
		Timestamp: time.Now().UnixMilli(),
	}
}