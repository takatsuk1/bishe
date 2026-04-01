package taskmanager

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"ai/pkg/protocol"
)

// ErrTaskNotFound 表示任务未找到
var ErrTaskNotFound = errors.New("task not found")

// Manager 任务管理器接口
type Manager interface {
	// BuildTask 构建任务
	// taskID: 任务ID（可选）
	// metadata: 元数据
	// 返回任务ID和可能的错误
	BuildTask(taskID *string, metadata map[string]string) (string, error)
	// GetTask 获取任务
	// ctx: 上下文
	// taskID: 任务ID
	// 返回任务和可能的错误
	GetTask(ctx context.Context, taskID string) (*protocol.Task, error)
	// SubscribeTask 订阅任务
	// ctx: 上下文
	// taskID: 任务ID
	// 返回事件通道和可能的错误
	SubscribeTask(ctx context.Context, taskID string) (<-chan protocol.StreamEvent, error)
	// UpdateTaskState 更新任务状态
	// ctx: 上下文
	// taskID: 任务ID
	// state: 新状态
	// msg: 消息（可选）
	// 返回可能的错误
	UpdateTaskState(ctx context.Context, taskID string, state protocol.TaskState, msg *protocol.Message) error
	// CancelTask 取消任务
	// ctx: 上下文
	// taskID: 任务ID
	// 返回可能的错误
	CancelTask(ctx context.Context, taskID string) error
}

// InMemoryManager 内存任务管理器
type InMemoryManager struct {
	// mu 读写锁
	mu          sync.RWMutex
	// tasks 任务映射
	tasks       map[string]*protocol.Task
	// subscribers 订阅者映射
	subscribers map[string][]chan protocol.StreamEvent
	// nowFunc 当前时间函数
	nowFunc     func() time.Time
}

// NewInMemoryManager 创建一个新的内存任务管理器
// 返回创建的内存任务管理器
func NewInMemoryManager() *InMemoryManager {
	return &InMemoryManager{
		tasks:       make(map[string]*protocol.Task),
		subscribers: make(map[string][]chan protocol.StreamEvent),
		nowFunc:     time.Now,
	}
}

// BuildTask 构建任务
// taskID: 任务ID（可选）
// metadata: 元数据
// 返回任务ID和可能的错误
func (m *InMemoryManager) BuildTask(taskID *string, metadata map[string]string) (string, error) {
	id := ""
	// 如果提供了任务ID，则使用它
	if taskID != nil {
		id = *taskID
	}
	// 如果没有提供任务ID，则生成一个
	if id == "" {
		id = fmt.Sprintf("task-%d", m.nowFunc().UnixNano())
	}

	// 加锁
	m.mu.Lock()
	defer m.mu.Unlock()
	// 检查任务是否已存在
	if _, exists := m.tasks[id]; exists {
		return id, nil
	}
	// 创建新任务
	m.tasks[id] = &protocol.Task{
		ID:       id,
		Metadata: cloneStringMap(metadata),
		Status: protocol.TaskStatus{
			State:     protocol.TaskStateQueued,
			UpdatedAt: m.nowFunc().UnixMilli(),
		},
		CreatedAt: m.nowFunc().UnixMilli(),
	}
	return id, nil
}

// GetTask 获取任务
// ctx: 上下文
// taskID: 任务ID
// 返回任务和可能的错误
func (m *InMemoryManager) GetTask(ctx context.Context, taskID string) (*protocol.Task, error) {
	_ = ctx
	// 加读锁
	m.mu.RLock()
	defer m.mu.RUnlock()
	// 查找任务
	task, ok := m.tasks[taskID]
	if !ok {
		return nil, ErrTaskNotFound
	}
	// 克隆任务
	out := *task
	out.Metadata = cloneStringMap(task.Metadata)
	// 克隆消息
	if task.Status.Message != nil {
		msg := *task.Status.Message
		msg.Metadata = cloneAnyMap(task.Status.Message.Metadata)
		msg.Parts = append([]protocol.Part(nil), task.Status.Message.Parts...)
		out.Status.Message = &msg
	}
	return &out, nil
}

// SubscribeTask 订阅任务
// ctx: 上下文
// taskID: 任务ID
// 返回事件通道和可能的错误
func (m *InMemoryManager) SubscribeTask(ctx context.Context, taskID string) (<-chan protocol.StreamEvent, error) {
	// 加锁
	m.mu.Lock()
	// 检查任务是否存在
	if _, ok := m.tasks[taskID]; !ok {
		m.mu.Unlock()
		return nil, ErrTaskNotFound
	}
	// 创建事件通道
	ch := make(chan protocol.StreamEvent, 32)
	// 添加订阅者
	m.subscribers[taskID] = append(m.subscribers[taskID], ch)
	m.mu.Unlock()

	// 当上下文完成时，移除订阅者并关闭通道
	go func() {
		<-ctx.Done()
		m.removeSubscriber(taskID, ch)
		close(ch)
	}()
	return ch, nil
}

// UpdateTaskState 更新任务状态
// ctx: 上下文
// taskID: 任务ID
// state: 新状态
// msg: 消息（可选）
// 返回可能的错误
func (m *InMemoryManager) UpdateTaskState(ctx context.Context, taskID string, state protocol.TaskState, msg *protocol.Message) error {
	_ = ctx
	// 加锁
	m.mu.Lock()
	// 查找任务
	task, ok := m.tasks[taskID]
	if !ok {
		m.mu.Unlock()
		return ErrTaskNotFound
	}
	// 检查任务是否已处于终态
	if task.Status.State.IsTerminal() {
		m.mu.Unlock()
		return fmt.Errorf("task %s already terminal: %s", taskID, task.Status.State)
	}
	// 检查状态是否变化
	stateChanged := task.Status.State != state
	if stateChanged {
		// 检查状态转换是否有效
		if !protocol.CanTransition(task.Status.State, state) {
			m.mu.Unlock()
			return fmt.Errorf("invalid state transition: %s -> %s", task.Status.State, state)
		}
	}
	// 检查是否需要广播
	shouldBroadcast := stateChanged || msg != nil
	if !shouldBroadcast {
		m.mu.Unlock()
		return nil
	}

	// 更新任务状态
	now := m.nowFunc().UnixMilli()
	if stateChanged {
		task.Status.State = state
	}
	task.Status.UpdatedAt = now
	task.Status.Error = ""
	// 如果状态为失败，设置错误信息
	if state == protocol.TaskStateFailed {
		if msg != nil {
			task.Status.Error = msg.FirstText()
		}
	}
	// 如果有消息，克隆并设置
	if msg != nil {
		cloned := *msg
		cloned.Metadata = cloneAnyMap(msg.Metadata)
		cloned.Parts = append([]protocol.Part(nil), msg.Parts...)
		task.Status.Message = &cloned
	}
	// 创建任务状态事件
	event := protocol.NewTaskStatusEvent(taskID, task.Status)
	// 复制订阅者列表
	subs := append([]chan protocol.StreamEvent(nil), m.subscribers[taskID]...)
	m.mu.Unlock()

	// 广播事件
	for _, sub := range subs {
		select {
		case sub <- event:
		default:
		}
	}
	return nil
}

// CancelTask 取消任务
// ctx: 上下文
// taskID: 任务ID
// 返回可能的错误
func (m *InMemoryManager) CancelTask(ctx context.Context, taskID string) error {
	// 创建取消消息
	msg := protocol.Message{
		Role:  protocol.MessageRoleAgent,
		Parts: []protocol.Part{protocol.NewTextPart("task canceled")},
	}
	// 更新任务状态为取消
	return m.UpdateTaskState(ctx, taskID, protocol.TaskStateCanceled, &msg)
}

// removeSubscriber 移除订阅者
// taskID: 任务ID
// ch: 事件通道
func (m *InMemoryManager) removeSubscriber(taskID string, ch chan protocol.StreamEvent) {
	// 加锁
	m.mu.Lock()
	defer m.mu.Unlock()
	// 获取订阅者列表
	list := m.subscribers[taskID]
	// 查找并移除订阅者
	for idx := range list {
		if list[idx] == ch {
			m.subscribers[taskID] = append(list[:idx], list[idx+1:]...)
			break
		}
	}
	// 如果没有订阅者，删除订阅者列表
	if len(m.subscribers[taskID]) == 0 {
		delete(m.subscribers, taskID)
	}
}

// cloneStringMap 克隆一个map[string]string
// in: 输入map
// 返回克隆后的map
func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// cloneAnyMap 克隆一个map[string]any
// in: 输入map
// 返回克隆后的map
func cloneAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}