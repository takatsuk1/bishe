// package orchestrator 包含任务编排相关功能，负责协调和管理任务的执行
package orchestrator

import (
	"context" // 上下文管理
	"errors"  // 错误处理
	"time"    // 时间处理
)

// ErrWorkerNotFound 表示找不到工作器的错误
var ErrWorkerNotFound = errors.New("worker not found")

// AgentStatus 表示当前工作器的健康状态和可用性
type AgentStatus string

// 定义代理状态常量
const (
	AgentStatusIdle    AgentStatus = "idle"    // 空闲状态
	AgentStatusBusy    AgentStatus = "busy"    // 忙碌状态
	AgentStatusOffline AgentStatus = "offline" // 离线状态
	AgentStatusError   AgentStatus = "error"   // 错误状态
)

// AgentCapability 描述工作器可以处理的工作负载类型
type AgentCapability string

// AgentDescriptor 定义了工作器的静态元数据和契约
type AgentDescriptor struct {
	ID           string               // 代理 ID
	Name         string               // 代理名称
	Capabilities []AgentCapability    // 代理能力列表
	InputSchema  map[string]any       // 输入模式
	OutputSchema map[string]any       // 输出模式
	Metadata     map[string]string    // 元数据
}

// AgentRuntime 跟踪用于调度和故障处理的可变运行时状态
type AgentRuntime struct {
	Status        AgentStatus // 代理状态
	LastHeartbeat time.Time   // 最后心跳时间
	LastError     string      // 最后错误信息
	UpdatedAt     time.Time   // 更新时间
}

// AgentSnapshot 结合了静态定义和运行时状态
type AgentSnapshot struct {
	Descriptor AgentDescriptor // 代理描述符
	Runtime    AgentRuntime    // 代理运行时状态
}

// Worker 是执行引擎使用的统一执行接口
type Worker interface {
	// Execute 执行任务并返回结果
	Execute(ctx context.Context, req ExecutionRequest) (ExecutionResult, error)
}

// ExecutionRequest 是传递给工作器的标准化输入
type ExecutionRequest struct {
	TaskID       string            // 任务 ID
	TaskType     string            // 任务类型
	Payload      map[string]any    // 任务负载
	WorkflowID   string            // 工作流 ID
	NodeID       string            // 节点 ID
	NodeType     NodeType          // 节点类型
	NodeConfig   map[string]any    // 节点配置
	NodeMetadata map[string]string // 节点元数据
	Attempt      int               // 尝试次数
	TriggeredAt  time.Time         // 触发时间
}

// ExecutionResult 是工作器产生的标准化输出
type ExecutionResult struct {
	Output      map[string]any // 输出数据
	RawText     string         // 原始文本
	Duration    time.Duration  // 执行持续时间
	FinishedAt  time.Time      // 完成时间
	Retryable   bool           // 是否可重试
	WorkerError string         // 工作器错误信息
}