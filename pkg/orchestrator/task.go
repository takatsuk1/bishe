package orchestrator

import (
	"errors"
	"fmt"
	"time"
)

// 错误定义
var (
	// ErrInvalidTaskTransition 表示无效的任务状态转换
	ErrInvalidTaskTransition = errors.New("invalid task transition")
	// ErrInvalidTaskID 表示无效的任务ID
	ErrInvalidTaskID         = errors.New("invalid task id")
)

// TaskState 表示任务的状态
type TaskState string

// 任务状态常量定义
const (
	// TaskStatePending 表示任务处于待处理状态
	TaskStatePending   TaskState = "pending"
	// TaskStateQueued 表示任务处于队列中
	TaskStateQueued    TaskState = "queued"
	// TaskStateRunning 表示任务正在运行
	TaskStateRunning   TaskState = "running"
	// TaskStateSucceeded 表示任务成功完成
	TaskStateSucceeded TaskState = "succeeded"
	// TaskStateFailed 表示任务失败
	TaskStateFailed    TaskState = "failed"
	// TaskStateCanceled 表示任务被取消
	TaskStateCanceled  TaskState = "canceled"
	// TaskStatePaused 表示任务暂停
	TaskStatePaused    TaskState = "paused"
)

// RetryPolicy 定义任务的重试策略
type RetryPolicy struct {
	// MaxAttempts 最大尝试次数
	MaxAttempts int
	// BaseBackoff 基础退避时间
	BaseBackoff time.Duration
	// MaxBackoff 最大退避时间
	MaxBackoff  time.Duration
}

// Task 表示一个任务
type Task struct {
	// ID 任务唯一标识符
	ID          string
	// Type 任务类型
	Type        string
	// Content 任务内容
	Content     string
	// Input 任务输入参数
	Input       map[string]any
	// Timeout 任务超时时间
	Timeout     time.Duration
	// RetryPolicy 任务重试策略
	RetryPolicy RetryPolicy
	// State 任务当前状态
	State       TaskState
	// Attempts 已尝试次数
	Attempts    int
	// CreatedAt 任务创建时间
	CreatedAt   time.Time
	// UpdatedAt 任务更新时间
	UpdatedAt   time.Time
	// StartedAt 任务开始时间
	StartedAt   *time.Time
	// FinishedAt 任务完成时间
	FinishedAt  *time.Time
	// LastError 上次错误信息
	LastError   string
}

// NewTask 创建一个新任务
// id: 任务唯一标识符
// taskType: 任务类型
// input: 任务输入参数
// 返回创建的任务和可能的错误
func NewTask(id, taskType string, input map[string]any) (*Task, error) {
	// 检查任务ID是否为空
	if id == "" {
		return nil, ErrInvalidTaskID
	}
	// 获取当前时间
	now := time.Now()
	// 创建并返回任务
	return &Task{
		ID:    id,
		Type:  taskType,
		Input: cloneAnyMap(input),
		State: TaskStatePending,
		RetryPolicy: RetryPolicy{
			MaxAttempts: 1,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// CanTransition 检查任务是否可以转换到下一个状态
// next: 下一个状态
// 返回是否可以转换
func (t *Task) CanTransition(next TaskState) bool {
	// 根据当前状态判断是否可以转换到下一个状态
	switch t.State {
	case TaskStatePending:
		// 待处理状态可以转换到队列或取消状态
		return next == TaskStateQueued || next == TaskStateCanceled
	case TaskStateQueued:
		// 队列状态可以转换到运行、取消或暂停状态
		return next == TaskStateRunning || next == TaskStateCanceled || next == TaskStatePaused
	case TaskStateRunning:
		// 运行状态可以转换到成功、失败、取消或暂停状态
		return next == TaskStateSucceeded || next == TaskStateFailed || next == TaskStateCanceled || next == TaskStatePaused
	case TaskStatePaused:
		// 暂停状态可以转换到队列或取消状态
		return next == TaskStateQueued || next == TaskStateCanceled
	case TaskStateFailed:
		// 失败状态可以转换到队列状态（用于重试）
		return next == TaskStateQueued
	case TaskStateSucceeded, TaskStateCanceled:
		// 成功和取消状态是终态，不能转换到其他状态
		return false
	default:
		// 未知状态，不能转换
		return false
	}
}

// TransitionTo 将任务转换到指定状态
// next: 下一个状态
// errMsg: 错误信息（当状态为失败时使用）
// 返回可能的错误
func (t *Task) TransitionTo(next TaskState, errMsg string) error {
	// 检查是否可以转换到下一个状态
	if !t.CanTransition(next) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTaskTransition, t.State, next)
	}
	// 获取当前时间
	now := time.Now()
	// 如果转换到运行状态且开始时间为nil，则设置开始时间
	if next == TaskStateRunning && t.StartedAt == nil {
		t.StartedAt = &now
	}
	// 如果是终态，则设置完成时间
	if isTerminalTaskState(next) {
		t.FinishedAt = &now
	}
	// 如果转换到失败状态，设置错误信息
	if next == TaskStateFailed {
		t.LastError = errMsg
	} else if next != TaskStatePaused {
		// 否则清空错误信息（暂停状态保留错误信息）
		t.LastError = ""
	}
	// 如果转换到运行状态，增加尝试次数
	if next == TaskStateRunning {
		t.Attempts++
	}
	// 更新状态和更新时间
	t.State = next
	t.UpdatedAt = now
	return nil
}

// ShouldRetry 检查任务是否应该重试
// 返回是否应该重试
func (t *Task) ShouldRetry() bool {
	// 只有失败状态的任务才需要重试
	if t.State != TaskStateFailed {
		return false
	}
	// 如果最大尝试次数小于等于1，则不重试
	if t.RetryPolicy.MaxAttempts <= 1 {
		return false
	}
	// 如果已尝试次数小于最大尝试次数，则应该重试
	return t.Attempts < t.RetryPolicy.MaxAttempts
}

// NextBackoff 计算下次重试的退避时间
// 返回退避时间
func (t *Task) NextBackoff() time.Duration {
	// 如果基础退避时间小于等于0，则不使用退避
	if t.RetryPolicy.BaseBackoff <= 0 {
		return 0
	}
	// 获取当前尝试次数
	attempt := t.Attempts
	if attempt <= 0 {
		attempt = 1
	}
	// 计算退避时间（指数退避）
	backoff := t.RetryPolicy.BaseBackoff << (attempt - 1)
	// 如果计算的退避时间超过最大退避时间，则使用最大退避时间
	if t.RetryPolicy.MaxBackoff > 0 && backoff > t.RetryPolicy.MaxBackoff {
		return t.RetryPolicy.MaxBackoff
	}
	return backoff
}

// isTerminalTaskState 检查是否为终态
// s: 任务状态
// 返回是否为终态
func isTerminalTaskState(s TaskState) bool {
	// 成功、失败和取消状态是终态
	return s == TaskStateSucceeded || s == TaskStateFailed || s == TaskStateCanceled
}

// cloneAnyMap 克隆一个map[string]any
// in: 输入map
// 返回克隆后的map
func cloneAnyMap(in map[string]any) map[string]any {
	// 如果输入为nil，返回nil
	if in == nil {
		return nil
	}
	// 创建新的map并复制所有键值对
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}