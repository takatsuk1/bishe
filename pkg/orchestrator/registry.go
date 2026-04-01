// package orchestrator 包含任务编排相关功能，负责协调和管理任务的执行
package orchestrator

import (
	"errors" // 错误处理
	"sort"   // 排序
	"sync"   // 同步原语
	"time"   // 时间处理
)

// 定义错误常量
var (
	ErrAgentAlreadyExists = errors.New("agent already exists") // 代理已存在错误
	ErrInvalidAgentID     = errors.New("invalid agent id")     // 无效代理 ID 错误
)

// AgentRegistry 以统一方式管理代理生命周期
type AgentRegistry interface {
	// Register 注册代理
	Register(desc AgentDescriptor, worker Worker) error
	// Unregister 注销代理
	Unregister(agentID string) error
	// Update 更新代理描述
	Update(desc AgentDescriptor) error
	// SetStatus 设置代理状态
	SetStatus(agentID string, status AgentStatus, errMsg string) error
	// Get 获取代理信息
	Get(agentID string) (AgentSnapshot, Worker, error)
	// List 列出所有代理
	List() []AgentSnapshot
	// Match 匹配具有特定能力的代理
	Match(capabilities ...AgentCapability) []AgentSnapshot
}

// registryEntry 表示注册表中的条目
type registryEntry struct {
	snapshot AgentSnapshot // 代理快照
	worker   Worker       // 工作器
}

// InMemoryAgentRegistry 是单节点调度器的进程内实现
type InMemoryAgentRegistry struct {
	mu      sync.RWMutex              // 读写锁
	agents  map[string]*registryEntry // 代理映射
	nowFunc func() time.Time          // 当前时间函数
}

// NewInMemoryAgentRegistry 创建一个新的内存代理注册表
func NewInMemoryAgentRegistry() *InMemoryAgentRegistry {
	return &InMemoryAgentRegistry{
		agents:  make(map[string]*registryEntry),
		nowFunc: time.Now,
	}
}

// Register 注册代理
func (r *InMemoryAgentRegistry) Register(desc AgentDescriptor, worker Worker) error {
	// 检查代理 ID 是否为空
	if desc.ID == "" {
		return ErrInvalidAgentID
	}
	// 检查工作器是否为 nil
	if worker == nil {
		return errors.New("worker is nil")
	}

	// 加锁并注册代理
	r.mu.Lock()
	defer r.mu.Unlock()
	// 检查代理是否已存在
	if _, exists := r.agents[desc.ID]; exists {
		return ErrAgentAlreadyExists
	}
	// 获取当前时间
	now := r.nowFunc()
	// 创建注册表条目
	r.agents[desc.ID] = &registryEntry{
		snapshot: AgentSnapshot{
			Descriptor: cloneDescriptor(desc),
			Runtime: AgentRuntime{
				Status:        AgentStatusIdle,
				LastHeartbeat: now,
				UpdatedAt:     now,
			},
		},
		worker: worker,
	}
	return nil
}

// Unregister 注销代理
func (r *InMemoryAgentRegistry) Unregister(agentID string) error {
	// 检查代理 ID 是否为空
	if agentID == "" {
		return ErrInvalidAgentID
	}
	// 加锁并注销代理
	r.mu.Lock()
	defer r.mu.Unlock()
	// 检查代理是否存在
	if _, exists := r.agents[agentID]; !exists {
		return ErrWorkerNotFound
	}
	// 删除代理
	delete(r.agents, agentID)
	return nil
}

// Update 更新代理描述
func (r *InMemoryAgentRegistry) Update(desc AgentDescriptor) error {
	// 检查代理 ID 是否为空
	if desc.ID == "" {
		return ErrInvalidAgentID
	}
	// 加锁并更新代理
	r.mu.Lock()
	defer r.mu.Unlock()
	// 检查代理是否存在
	entry, exists := r.agents[desc.ID]
	if !exists {
		return ErrWorkerNotFound
	}
	// 更新描述符
	entry.snapshot.Descriptor = cloneDescriptor(desc)
	// 更新时间
	entry.snapshot.Runtime.UpdatedAt = r.nowFunc()
	return nil
}

// SetStatus 设置代理状态
func (r *InMemoryAgentRegistry) SetStatus(agentID string, status AgentStatus, errMsg string) error {
	// 检查代理 ID 是否为空
	if agentID == "" {
		return ErrInvalidAgentID
	}
	// 加锁并设置状态
	r.mu.Lock()
	defer r.mu.Unlock()
	// 检查代理是否存在
	entry, exists := r.agents[agentID]
	if !exists {
		return ErrWorkerNotFound
	}
	// 获取当前时间
	now := r.nowFunc()
	// 更新状态
	entry.snapshot.Runtime.Status = status
	entry.snapshot.Runtime.UpdatedAt = now
	entry.snapshot.Runtime.LastHeartbeat = now
	// 处理错误信息
	if errMsg != "" {
		entry.snapshot.Runtime.LastError = errMsg
	} else if status != AgentStatusError {
		entry.snapshot.Runtime.LastError = ""
	}
	return nil
}

// Get 获取代理信息
func (r *InMemoryAgentRegistry) Get(agentID string) (AgentSnapshot, Worker, error) {
	// 检查代理 ID 是否为空
	if agentID == "" {
		return AgentSnapshot{}, nil, ErrInvalidAgentID
	}
	// 加读锁并获取代理
	r.mu.RLock()
	defer r.mu.RUnlock()
	// 检查代理是否存在
	entry, exists := r.agents[agentID]
	if !exists {
		return AgentSnapshot{}, nil, ErrWorkerNotFound
	}
	// 返回克隆的快照和工作器
	return cloneSnapshot(entry.snapshot), entry.worker, nil
}

// List 列出所有代理
func (r *InMemoryAgentRegistry) List() []AgentSnapshot {
	// 加读锁并列出代理
	r.mu.RLock()
	defer r.mu.RUnlock()
	// 准备输出
	out := make([]AgentSnapshot, 0, len(r.agents))
	for _, entry := range r.agents {
		out = append(out, cloneSnapshot(entry.snapshot))
	}
	// 按代理 ID 排序
	sort.Slice(out, func(i, j int) bool {
		return out[i].Descriptor.ID < out[j].Descriptor.ID
	})
	return out
}

// Match 匹配具有特定能力的代理
func (r *InMemoryAgentRegistry) Match(capabilities ...AgentCapability) []AgentSnapshot {
	// 加读锁并匹配代理
	r.mu.RLock()
	defer r.mu.RUnlock()
	// 如果没有能力要求，返回空
	if len(capabilities) == 0 {
		return nil
	}
	// 构建需要的能力集合
	needed := make(map[AgentCapability]struct{}, len(capabilities))
	for _, c := range capabilities {
		needed[c] = struct{}{}
	}
	// 准备输出
	out := make([]AgentSnapshot, 0)
	// 匹配代理
	for _, entry := range r.agents {
		if containsCapabilities(entry.snapshot.Descriptor.Capabilities, needed) {
			out = append(out, cloneSnapshot(entry.snapshot))
		}
	}
	// 按代理 ID 排序
	sort.Slice(out, func(i, j int) bool {
		return out[i].Descriptor.ID < out[j].Descriptor.ID
	})
	return out
}

// containsCapabilities 检查是否包含所有需要的能力
func containsCapabilities(have []AgentCapability, needed map[AgentCapability]struct{}) bool {
	// 如果没有需要的能力，返回 true
	if len(needed) == 0 {
		return true
	}
	// 构建已有的能力集合
	got := make(map[AgentCapability]struct{}, len(have))
	for _, c := range have {
		got[c] = struct{}{}
	}
	// 检查是否包含所有需要的能力
	for c := range needed {
		if _, ok := got[c]; !ok {
			return false
		}
	}
	return true
}

// cloneDescriptor 克隆代理描述符
func cloneDescriptor(in AgentDescriptor) AgentDescriptor {
	out := in
	// 克隆能力列表
	out.Capabilities = append([]AgentCapability(nil), in.Capabilities...)
	// 克隆输入模式
	if in.InputSchema != nil {
		out.InputSchema = make(map[string]any, len(in.InputSchema))
		for k, v := range in.InputSchema {
			out.InputSchema[k] = v
		}
	}
	// 克隆输出模式
	if in.OutputSchema != nil {
		out.OutputSchema = make(map[string]any, len(in.OutputSchema))
		for k, v := range in.OutputSchema {
			out.OutputSchema[k] = v
		}
	}
	// 克隆元数据
	if in.Metadata != nil {
		out.Metadata = make(map[string]string, len(in.Metadata))
		for k, v := range in.Metadata {
			out.Metadata[k] = v
		}
	}
	return out
}

// cloneSnapshot 克隆代理快照
func cloneSnapshot(in AgentSnapshot) AgentSnapshot {
	return AgentSnapshot{
		Descriptor: cloneDescriptor(in.Descriptor),
		Runtime:    in.Runtime,
	}
}