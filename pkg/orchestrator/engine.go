// package orchestrator 包含任务编排相关功能，负责协调和管理任务的执行
package orchestrator

import (
	"ai/pkg/monitor"
	"context" // 上下文管理
	"errors"  // 错误处理
	"fmt"     // 格式化输出
	"strconv"
	"strings" // 字符串处理
	"sync"    // 同步原语
	"time"    // 时间处理
)

// ErrWorkflowNotFound 表示找不到工作流的错误
var ErrWorkflowNotFound = errors.New("workflow not found")

// ErrRunNotFound 表示找不到工作流运行的错误
var ErrRunNotFound = errors.New("workflow run not found")

// ErrTaskPaused 表示任务已暂停的错误
var ErrTaskPaused = errors.New("task paused")

// Engine 是主机或任务处理器使用的顶级编排门面
type Engine interface {
	// RegisterWorker 注册工作器
	RegisterWorker(desc AgentDescriptor, worker Worker) error
	// RegisterWorkflow 注册工作流
	RegisterWorkflow(wf *Workflow) error
	// StartWorkflow 启动工作流
	StartWorkflow(ctx context.Context, workflowID string, input map[string]any) (string, error)
	// WaitRun 等待工作流运行完成
	WaitRun(ctx context.Context, runID string) (RunResult, error)
	// GetRun 获取工作流运行状态
	GetRun(ctx context.Context, runID string) (RunResult, error)
	// CancelTask 取消任务
	CancelTask(ctx context.Context, taskID string) error
	// PauseTask 暂停任务
	PauseTask(ctx context.Context, taskID string) error
	// ResumeTask 恢复任务
	ResumeTask(ctx context.Context, taskID string) error
}

// Config 控制引擎行为，可以从 config.MainConfig.Orchestrator 映射
type Config struct {
	DefaultTaskTimeoutSec int // 默认任务超时时间（秒）
	RetryMaxAttempts      int // 最大重试次数
	RetryBaseBackoffMs    int // 基础退避时间（毫秒）
	RetryMaxBackoffMs     int // 最大退避时间（毫秒）
	MonitorService        *monitor.Service
}

// RunState 表示工作流运行状态
type RunState string

// 定义运行状态常量
const (
	RunStateRunning   RunState = "running"   // 运行中
	RunStateSucceeded RunState = "succeeded" // 成功
	RunStateFailed    RunState = "failed"    // 失败
	RunStateCanceled  RunState = "canceled"  // 取消
	RunStatePaused    RunState = "paused"    // 暂停
)

// NodeRunResult 表示节点运行结果
type NodeRunResult struct {
	NodeID   string         `json:"nodeId"`             // 节点 ID
	TaskID   string         `json:"taskId"`             // 任务 ID
	State    TaskState      `json:"state"`              // 任务状态
	Output   map[string]any `json:"output,omitempty"`   // 输出
	ErrorMsg string         `json:"errorMsg,omitempty"` // 错误信息
}

// RunResult 表示工作流运行结果
type RunResult struct {
	RunID         string          `json:"runId"`                   // 运行 ID
	WorkflowID    string          `json:"workflowId"`              // 工作流 ID
	State         RunState        `json:"state"`                   // 运行状态
	StartedAt     time.Time       `json:"startedAt"`               // 开始时间
	FinishedAt    time.Time       `json:"finishedAt"`              // 完成时间
	UpdatedAt     time.Time       `json:"updatedAt"`               // 更新时间
	CurrentNodeID string          `json:"currentNodeId,omitempty"` // 当前节点 ID
	CurrentTaskID string          `json:"currentTaskId,omitempty"` // 当前任务 ID
	NodeResults   []NodeRunResult `json:"nodeResults"`             // 节点运行结果
	FinalOutput   map[string]any  `json:"finalOutput,omitempty"`   // 最终输出
	ErrorMessage  string          `json:"errorMessage,omitempty"`  // 错误信息
}

// workflowRun 表示工作流运行实例
type workflowRun struct {
	result        RunResult          // 运行结果
	done          chan struct{}      // 完成通道
	cancel        context.CancelFunc // 取消函数
	userID        string
	taskID        string
	sourceAgentID string

	mu     sync.RWMutex // 读写锁
	paused bool         // 是否暂停
}

// nodeHandler 定义了节点处理函数类型
type nodeHandler func(ctx context.Context, e *engine, wf *Workflow, run *workflowRun,
	node Node, nextIndex map[string][]string, shared map[string]any) (NodeRunResult, string, error)

// engine 是 Engine 接口的实现
type engine struct {
	cfg        Config                   // 配置
	registry   AgentRegistry            // 代理注册表
	handlers   map[NodeType]nodeHandler // 节点处理器映射
	monitorSvc *monitor.Service

	mu        sync.RWMutex            // 读写锁
	workflows map[string]*Workflow    // 工作流映射
	runs      map[string]*workflowRun // 运行实例映射
	nextRunID uint64                  // 下一个运行 ID
}

// NewEngine 创建一个新的引擎实例
func NewEngine(cfg Config, registry AgentRegistry) Engine {
	// 如果注册表为 nil，使用内存注册表
	if registry == nil {
		registry = NewInMemoryAgentRegistry()
	}
	// 设置默认值
	if cfg.DefaultTaskTimeoutSec <= 0 {
		cfg.DefaultTaskTimeoutSec = 600 // 默认 10 分钟
	}
	if cfg.RetryMaxAttempts <= 0 {
		cfg.RetryMaxAttempts = 1 // 默认 1 次
	}
	return &engine{
		cfg:        cfg,
		registry:   registry,
		handlers:   defaultNodeHandlers(),
		monitorSvc: cfg.MonitorService,
		workflows:  make(map[string]*Workflow),
		runs:       make(map[string]*workflowRun),
	}
}

// RegisterWorker 注册工作器
func (e *engine) RegisterWorker(desc AgentDescriptor, worker Worker) error {
	return e.registry.Register(desc, worker)
}

// RegisterWorkflow 注册工作流
func (e *engine) RegisterWorkflow(wf *Workflow) error {
	// 检查工作流是否为 nil
	if wf == nil {
		return errors.New("workflow is nil")
	}
	// 验证工作流
	if err := wf.Validate(); err != nil {
		return err
	}
	// 加锁并注册工作流
	e.mu.Lock()
	defer e.mu.Unlock()
	e.workflows[wf.ID] = wf
	return nil
}

// StartWorkflow 启动工作流
func (e *engine) StartWorkflow(ctx context.Context, workflowID string, input map[string]any) (string, error) {
	userID := firstNonEmptyString(mapString(input, "user_id"), mapString(input, "userId"), mapString(input, "UserID"))
	taskID := firstNonEmptyString(mapString(input, "task_id"), mapString(input, "taskId"), mapString(input, "TaskID"))
	sourceAgentID := firstNonEmptyString(
		mapString(input, "source_agent_id"),
		mapString(input, "sourceAgentId"),
		mapString(input, "agent_id"),
		mapString(input, "agentId"),
	)

	e.mu.Lock()
	// 查找工作流
	wf, ok := e.workflows[workflowID]
	if !ok {
		e.mu.Unlock()
		return "", ErrWorkflowNotFound
	}
	// 生成运行 ID
	e.nextRunID++
	runID := fmt.Sprintf("%s:run:%d:%d", workflowID, time.Now().UnixMilli(), e.nextRunID)
	// 创建上下文和运行实例
	runCtx, cancel := context.WithCancel(ctx)
	run := &workflowRun{
		result: RunResult{
			RunID:      runID,
			WorkflowID: workflowID,
			State:      RunStateRunning,
			StartedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		},
		done:          make(chan struct{}),
		cancel:        cancel,
		userID:        userID,
		taskID:        taskID,
		sourceAgentID: sourceAgentID,
	}
	// 注册运行实例
	e.runs[runID] = run
	e.mu.Unlock()

	if e.monitorSvc != nil {
		_ = e.monitorSvc.CreateRun(ctx, monitor.CreateRunInput{
			RunID:         runID,
			WorkflowID:    workflowID,
			UserID:        userID,
			SourceAgentID: sourceAgentID,
			TaskID:        taskID,
			Status:        monitor.StatusRunning,
			StartedAt:     run.result.StartedAt,
		})
	}

	// 异步执行工作流
	go e.executeRun(runCtx, wf, run, cloneAnyMap(input))
	return runID, nil
}

// WaitRun 等待工作流运行完成
func (e *engine) WaitRun(ctx context.Context, runID string) (RunResult, error) {
	// 获取运行实例
	run, err := e.getRun(runID)
	if err != nil {
		return RunResult{}, err
	}
	// 等待完成
	select {
	case <-run.done:
		run.mu.RLock()
		defer run.mu.RUnlock()
		return cloneRunResult(run.result), nil
	case <-ctx.Done():
		return RunResult{}, ctx.Err()
	}
}

// GetRun 获取工作流运行状态
func (e *engine) GetRun(ctx context.Context, runID string) (RunResult, error) {
	_ = ctx
	// 获取运行实例
	run, err := e.getRun(runID)
	if err != nil {
		return RunResult{}, err
	}
	run.mu.RLock()
	defer run.mu.RUnlock()
	return cloneRunResult(run.result), nil
}

// CancelTask 取消任务
func (e *engine) CancelTask(ctx context.Context, taskID string) error {
	_ = ctx
	// 获取运行实例
	run, err := e.getRun(taskID)
	if err != nil {
		return err
	}
	// 取消运行
	run.cancel()
	return nil
}

// PauseTask 暂停任务
func (e *engine) PauseTask(ctx context.Context, taskID string) error {
	_ = ctx
	// 获取运行实例
	run, err := e.getRun(taskID)
	if err != nil {
		return err
	}
	// 暂停运行
	run.mu.Lock()
	run.paused = true
	if run.result.State == RunStateRunning {
		run.result.State = RunStatePaused
	}
	run.mu.Unlock()
	return nil
}

// ResumeTask 恢复任务
func (e *engine) ResumeTask(ctx context.Context, taskID string) error {
	_ = ctx
	// 获取运行实例
	run, err := e.getRun(taskID)
	if err != nil {
		return err
	}
	// 恢复运行
	run.mu.Lock()
	run.paused = false
	if run.result.State == RunStatePaused {
		run.result.State = RunStateRunning
	}
	run.mu.Unlock()
	return nil
}

// getRun 获取运行实例
func (e *engine) getRun(runID string) (*workflowRun, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	run, ok := e.runs[runID]
	if !ok {
		return nil, ErrRunNotFound
	}
	return run, nil
}

// executeRun 执行工作流运行
func (e *engine) executeRun(ctx context.Context, wf *Workflow, run *workflowRun, input map[string]any) {
	defer close(run.done)

	// 构建下一个节点索引
	nextIndex := buildNextIndex(wf)

	// 准备共享数据
	shared := cloneAnyMap(input)
	if shared == nil {
		shared = make(map[string]any)
	}
	seedInputQueryHistory(shared)
	// 准备节点结果
	results := make([]NodeRunResult, 0, len(wf.Nodes))
	currentNodeID := wf.StartNodeID
	maxSteps := 10000

	// 执行工作流
	for step := 0; currentNodeID != ""; step++ {
		nodeStartedAt := time.Now()
		// 设置运行进度
		e.setRunProgress(run, currentNodeID, "")
		// 检查是否超过最大步骤
		if step >= maxSteps {
			e.finishRunWithResults(run, RunStateFailed, "workflow exceeded max execution steps", shared, results)
			return
		}
		// 检查上下文是否取消
		if ctx.Err() != nil {
			e.finishRun(run, RunStateCanceled, ctx.Err().Error(), shared)
			return
		}
		// 检查是否暂停
		if err := e.waitIfPaused(ctx, run); err != nil {
			e.finishRun(run, RunStateCanceled, err.Error(), shared)
			return
		}

		// 获取节点
		node, ok := wf.Nodes[currentNodeID]
		if !ok {
			errMsg := fmt.Sprintf("node %s not found", currentNodeID)
			e.finishRunWithResults(run, RunStateFailed, errMsg, shared, results)
			return
		}

		e.emitNodeStarted(ctx, run, wf.ID, node, shared)

		// 获取节点处理器
		handler, ok := e.handlers[node.Type]
		if !ok {
			errMsg := fmt.Sprintf("node type %s not implemented", node.Type)
			results = append(results, NodeRunResult{NodeID: node.ID, State: TaskStateFailed, ErrorMsg: errMsg})
			e.finishRunWithResults(run, RunStateFailed, errMsg, shared, results)
			return
		}

		// 执行节点
		nodeRes, nextNodeID, execErr := handler(ctx, e, wf, run, node, nextIndex, shared)
		durationMs := time.Since(nodeStartedAt).Milliseconds()
		updateSharedOutputState(shared, node.ID, nodeRes.Output)
		results = append(results, nodeRes)
		if execErr != nil {
			e.emitNodeFailed(ctx, run, wf.ID, node, nodeRes, execErr.Error(), durationMs)
			e.finishRunWithResults(run, RunStateFailed, execErr.Error(), shared, results)
			return
		}
		if nodeRes.State != TaskStateSucceeded {
			errMsg := nodeRes.ErrorMsg
			if errMsg == "" {
				errMsg = fmt.Sprintf("node %s failed", node.ID)
			}
			e.emitNodeFailed(ctx, run, wf.ID, node, nodeRes, errMsg, durationMs)
			e.finishRunWithResults(run, RunStateFailed, errMsg, shared, results)
			return
		}
		e.emitNodeFinished(ctx, run, wf.ID, node, nodeRes, durationMs)
		// 进入下一个节点
		currentNodeID = nextNodeID
	}

	// 完成工作流
	e.finishRunWithResults(run, RunStateSucceeded, "", shared, results)
}

// defaultNodeHandlers 返回默认的节点处理器
func defaultNodeHandlers() map[NodeType]nodeHandler {
	return map[NodeType]nodeHandler{
		NodeTypeStart:     startNodeHandler,
		NodeTypeEnd:       endNodeHandler,
		NodeTypePreInput:  preInputNodeHandler,
		NodeTypeCondition: conditionNodeHandler,
		NodeTypeLoop:      loopNodeHandler,
		NodeTypeChatModel: chatModelNodeHandler,
		NodeTypeTool:      toolNodeHandler,
	}
}

func preInputNodeHandler(ctx context.Context, e *engine, _ *Workflow, run *workflowRun, node Node,
	nextIndex map[string][]string, shared map[string]any) (NodeRunResult, string, error) {
	_ = ctx
	e.setRunProgress(run, node.ID, "")
	query := strings.TrimSpace(composeNodeInput(node.PreInput, selectNodeInputText(node, shared)))
	if query == "" {
		query = firstNonEmpty(
			strings.TrimSpace(fmt.Sprint(shared["query"])),
			strings.TrimSpace(fmt.Sprint(shared["text"])),
			strings.TrimSpace(fmt.Sprint(shared["input"])),
		)
	}
	if query != "" {
		shared["query"] = query
	}
	out := map[string]any{"query": query}
	shared[node.ID] = out
	nextNodeID, nextErr := resolveTaskNext(node, nextIndex[node.ID])
	if nextErr != nil {
		return NodeRunResult{NodeID: node.ID, State: TaskStateFailed, ErrorMsg: nextErr.Error()}, "", nextErr
	}
	return NodeRunResult{NodeID: node.ID, State: TaskStateSucceeded, Output: out}, nextNodeID, nil
}

// startNodeHandler 处理开始节点
func startNodeHandler(ctx context.Context, e *engine, _ *Workflow, run *workflowRun, node Node,
	nextIndex map[string][]string, shared map[string]any) (NodeRunResult, string, error) {
	_ = ctx
	// 设置运行进度
	e.setRunProgress(run, node.ID, "")
	// 解析下一个节点
	nextNodeID, nextErr := resolveTaskNext(node, nextIndex[node.ID])
	if nextErr != nil {
		return NodeRunResult{NodeID: node.ID, State: TaskStateFailed, ErrorMsg: nextErr.Error()}, "", nextErr
	}
	// 设置输出
	out := map[string]any{"next": nextNodeID}
	shared[node.ID] = out
	return NodeRunResult{NodeID: node.ID, State: TaskStateSucceeded, Output: out}, nextNodeID, nil
}

// endNodeHandler 处理结束节点
func endNodeHandler(ctx context.Context, e *engine, _ *Workflow, run *workflowRun, node Node,
	_ map[string][]string, shared map[string]any) (NodeRunResult, string, error) {
	_ = ctx
	// 设置运行进度
	e.setRunProgress(run, node.ID, "")
	// 设置输出
	out := map[string]any{"ended": true}
	shared[node.ID] = out
	return NodeRunResult{NodeID: node.ID, State: TaskStateSucceeded, Output: out}, "", nil
}

// taskNodeHandler 处理任务节点
func taskNodeHandler(ctx context.Context, e *engine, wf *Workflow, run *workflowRun, node Node,
	nextIndex map[string][]string, shared map[string]any) (NodeRunResult, string, error) {
	// 执行任务节点
	nodeRes := e.executeTaskNode(ctx, wf, run, node, shared)
	if nodeRes.State != TaskStateSucceeded {
		return nodeRes, "", nil
	}
	// 处理输出
	if nodeRes.Output != nil {
		for k, v := range nodeRes.Output {
			shared[k] = v
		}
		if q := extractQueryFromNodeOutput(nodeRes.Output); q != "" {
			shared["query"] = q
		}
	}
	shared[node.ID] = cloneAnyMap(nodeRes.Output)
	// 解析下一个节点
	nextNodeID, nextErr := resolveTaskNext(node, nextIndex[node.ID])
	if nextErr != nil {
		return nodeRes, "", nextErr
	}
	return nodeRes, nextNodeID, nil
}

// chatModelNodeHandler 处理聊天模型节点
func chatModelNodeHandler(ctx context.Context, e *engine, wf *Workflow, run *workflowRun, node Node,
	nextIndex map[string][]string, shared map[string]any) (NodeRunResult, string, error) {
	return semanticTaskNodeHandler(ctx, e, wf, run, node, nextIndex, shared, "chat_model")
}

// toolNodeHandler 处理工具节点
func toolNodeHandler(ctx context.Context, e *engine, wf *Workflow, run *workflowRun, node Node,
	nextIndex map[string][]string, shared map[string]any) (NodeRunResult, string, error) {
	return semanticTaskNodeHandler(ctx, e, wf, run, node, nextIndex, shared, "tool")
}

// semanticTaskNodeHandler 处理语义任务节点
func semanticTaskNodeHandler(ctx context.Context, e *engine, wf *Workflow, run *workflowRun, node Node,
	nextIndex map[string][]string, shared map[string]any, defaultTaskType string) (NodeRunResult, string, error) {
	// 解析节点配置
	resolved := node
	resolvedTaskType := strings.TrimSpace(resolved.TaskType)
	resolvedAgentID := strings.TrimSpace(resolved.AgentID)

	// 从配置中获取任务类型和代理 ID
	if resolved.Config != nil {
		if v, ok := resolved.Config["task_type"].(string); ok {
			if s := strings.TrimSpace(v); s != "" {
				resolvedTaskType = s
			}
		}
		if v, ok := resolved.Config["agent_id"].(string); ok {
			if s := strings.TrimSpace(v); s != "" {
				resolvedAgentID = s
			}
		}
	}

	// 设置默认任务类型
	if resolvedTaskType == "" {
		resolvedTaskType = defaultTaskType
	}
	// 检查代理 ID
	if resolvedAgentID == "" {
		err := fmt.Errorf("node %s missing agent_id", node.ID)
		return NodeRunResult{NodeID: node.ID, State: TaskStateFailed, ErrorMsg: err.Error()}, "", err
	}

	// 更新节点信息
	resolved.TaskType = resolvedTaskType
	resolved.AgentID = resolvedAgentID

	// 执行任务节点
	nodeRes, nextNodeID, execErr := taskNodeHandler(ctx, e, wf, run, resolved, nextIndex, shared)
	if nodeRes.NodeID == "" {
		nodeRes.NodeID = node.ID
	}
	return nodeRes, nextNodeID, execErr
}

// conditionNodeHandler 处理条件节点
func conditionNodeHandler(ctx context.Context, e *engine, _ *Workflow, run *workflowRun, node Node,
	nextIndex map[string][]string, shared map[string]any) (NodeRunResult, string, error) {
	_ = ctx
	// 设置运行进度
	e.setRunProgress(run, node.ID, "")
	// 评估条件
	matched := evaluateConditionNode(node, shared)
	// 解析下一个节点
	nextNodeID, nextErr := resolveConditionNext(node, nextIndex[node.ID], matched)
	if nextErr != nil {
		return NodeRunResult{NodeID: node.ID, State: TaskStateFailed, ErrorMsg: nextErr.Error()}, "", nextErr
	}
	// 设置输出
	out := map[string]any{"matched": matched, "next": nextNodeID}
	shared[node.ID] = out
	return NodeRunResult{NodeID: node.ID, State: TaskStateSucceeded, Output: out}, nextNodeID, nil
}

// loopNodeHandler 处理循环节点
func loopNodeHandler(ctx context.Context, e *engine, wf *Workflow, run *workflowRun, node Node,
	nextIndex map[string][]string, shared map[string]any) (NodeRunResult, string, error) {
	_ = ctx
	// 设置运行进度
	e.setRunProgress(run, node.ID, "")
	// 计算迭代次数
	iterKey := fmt.Sprintf("__loop_iter_%s", node.ID)
	iter, _ := shared[iterKey].(int)
	iter++
	shared[iterKey] = iter
	// 获取最大迭代次数
	maxIter := 1
	if node.LoopConfig != nil && node.LoopConfig.MaxIterations > 0 {
		maxIter = node.LoopConfig.MaxIterations
	}
	if node.Config != nil {
		if v, ok := node.Config["max_iterations"]; ok {
			switch vv := v.(type) {
			case int:
				if vv > 0 {
					maxIter = vv
				}
			case float64:
				if int(vv) > 0 {
					maxIter = int(vv)
				}
			}
		}
	}
	if maxIter > 10 {
		maxIter = 10
	}
	// 检查是否继续循环
	loopContinue := iter < maxIter
	// 解析下一个节点
	nextNodeID, nextErr := resolveLoopNext(node, wf, nextIndex[node.ID], loopContinue)
	if nextErr != nil {
		return NodeRunResult{NodeID: node.ID, State: TaskStateFailed, ErrorMsg: nextErr.Error()}, "", nextErr
	}
	// 设置输出
	out := map[string]any{"iteration": iter, "continue": loopContinue, "next": nextNodeID}
	shared[node.ID] = out
	return NodeRunResult{NodeID: node.ID, State: TaskStateSucceeded, Output: out}, nextNodeID, nil
}

// executeTaskNode 执行任务节点
func (e *engine) executeTaskNode(ctx context.Context, wf *Workflow, run *workflowRun, node Node, shared map[string]any) NodeRunResult {
	// 生成任务 ID
	taskID := fmt.Sprintf("%s:%s", run.result.RunID, node.ID)
	// 设置运行进度
	e.setRunProgress(run, node.ID, taskID)
	// 准备负载
	payload := cloneAnyMap(shared)
	if payload == nil {
		payload = make(map[string]any)
	}
	payload["query"] = composeNodeInput(node.PreInput, selectNodeInputText(node, shared))
	if _, ok := payload["query"]; !ok || strings.TrimSpace(fmt.Sprint(payload["query"])) == "" {
		if selected := strings.TrimSpace(selectNodeInputText(node, shared)); selected != "" {
			payload["query"] = selected
		}
	}
	// 处理元数据
	if node.Metadata != nil {
		for k, v := range node.Metadata {
			if strings.HasPrefix(k, "set.") {
				key := strings.TrimPrefix(k, "set.")
				key = strings.TrimSpace(key)
				if key != "" {
					payload[key] = v
				}
			}
		}
	}

	// 创建任务
	task, err := NewTask(taskID, node.TaskType, payload)
	if err != nil {
		return NodeRunResult{NodeID: node.ID, TaskID: taskID, State: TaskStateFailed, ErrorMsg: err.Error()}
	}
	// 设置任务超时和重试策略
	task.Timeout = time.Duration(e.cfg.DefaultTaskTimeoutSec) * time.Second
	task.RetryPolicy = RetryPolicy{
		MaxAttempts: e.cfg.RetryMaxAttempts,
		BaseBackoff: time.Duration(e.cfg.RetryBaseBackoffMs) * time.Millisecond,
		MaxBackoff:  time.Duration(e.cfg.RetryMaxBackoffMs) * time.Millisecond,
	}
	if task.RetryPolicy.MaxAttempts <= 0 {
		task.RetryPolicy.MaxAttempts = 1
	}

	// 转换任务状态为排队
	if err = task.TransitionTo(TaskStateQueued, ""); err != nil {
		return NodeRunResult{NodeID: node.ID, TaskID: taskID, State: TaskStateFailed, ErrorMsg: err.Error()}
	}

	// 获取工作器
	_, worker, err := e.registry.Get(node.AgentID)
	if err != nil {
		_ = task.TransitionTo(TaskStateFailed, err.Error())
		return NodeRunResult{NodeID: node.ID, TaskID: taskID, State: task.State, ErrorMsg: err.Error()}
	}

	// 执行任务
	attempt := 0
	for {
		attempt++
		// 转换任务状态为运行中
		_ = task.TransitionTo(TaskStateRunning, "")
		// 设置代理状态为忙碌
		_ = e.registry.SetStatus(node.AgentID, AgentStatusBusy, "")

		// 创建节点上下文
		nodeCtx := ctx
		cancel := func() {}
		if task.Timeout > 0 {
			nodeCtx, cancel = context.WithTimeout(ctx, task.Timeout)
		}

		// 执行工作器
		started := time.Now()
		execResult, execErr := worker.Execute(nodeCtx, ExecutionRequest{
			TaskID:       taskID,
			TaskType:     node.TaskType,
			Payload:      cloneAnyMap(payload),
			WorkflowID:   wf.ID,
			NodeID:       node.ID,
			NodeType:     node.Type,
			NodeConfig:   cloneAnyMap(node.Config),
			NodeMetadata: cloneStringMap(node.Metadata),
			Attempt:      attempt,
			TriggeredAt:  started,
		})
		cancel()
		// 设置代理状态为空闲
		_ = e.registry.SetStatus(node.AgentID, AgentStatusIdle, "")

		// 处理执行结果
		if execErr == nil {
			_ = task.TransitionTo(TaskStateSucceeded, "")
			return NodeRunResult{
				NodeID: node.ID,
				TaskID: taskID,
				State:  task.State,
				Output: cloneAnyMap(execResult.Output),
			}
		}

		// 处理错误
		_ = task.TransitionTo(TaskStateFailed, execErr.Error())
		if !task.ShouldRetry() {
			return NodeRunResult{
				NodeID:   node.ID,
				TaskID:   taskID,
				State:    task.State,
				ErrorMsg: execErr.Error(),
			}
		}
		// 退避重试
		backoff := task.NextBackoff()
		if backoff > 0 {
			select {
			case <-ctx.Done():
				return NodeRunResult{NodeID: node.ID, TaskID: taskID, State: TaskStateCanceled, ErrorMsg: ctx.Err().Error()}
			case <-time.After(backoff):
			}
		}
		// 转换任务状态为排队
		_ = task.TransitionTo(TaskStateQueued, "")
	}
}

// setRunProgress 设置运行进度
func (e *engine) setRunProgress(run *workflowRun, nodeID, taskID string) {
	if run == nil {
		return
	}
	run.mu.Lock()
	run.result.CurrentNodeID = nodeID
	run.result.CurrentTaskID = taskID
	run.result.UpdatedAt = time.Now()
	run.mu.Unlock()
}

// waitIfPaused 等待如果任务已暂停
func (e *engine) waitIfPaused(ctx context.Context, run *workflowRun) error {
	for {
		run.mu.RLock()
		paused := run.paused
		run.mu.RUnlock()
		if !paused {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// finishRun 完成运行
func (e *engine) finishRun(run *workflowRun, state RunState, errMsg string, output map[string]any) {
	e.finishRunWithResults(run, state, errMsg, output, nil)
}

// finishRunWithResults 完成运行并设置结果
func (e *engine) finishRunWithResults(run *workflowRun, state RunState, errMsg string,
	output map[string]any, results []NodeRunResult) {
	finishedAt := time.Now()
	durationMs := int64(0)
	runID := ""
	startedAt := time.Time{}

	run.mu.Lock()
	run.result.State = state
	run.result.ErrorMessage = errMsg
	run.result.FinishedAt = finishedAt
	run.result.UpdatedAt = run.result.FinishedAt
	run.result.FinalOutput = cloneAnyMap(output)
	run.result.NodeResults = append([]NodeRunResult(nil), results...)
	run.result.CurrentNodeID = ""
	run.result.CurrentTaskID = ""
	runID = run.result.RunID
	startedAt = run.result.StartedAt
	run.mu.Unlock()

	if !startedAt.IsZero() {
		durationMs = finishedAt.Sub(startedAt).Milliseconds()
	}

	if e.monitorSvc != nil {
		status := runStateToMonitorStatus(state)
		_ = e.monitorSvc.FinishRun(context.Background(), monitor.FinishRunInput{
			RunID:        runID,
			Status:       status,
			FinishedAt:   finishedAt,
			DurationMs:   durationMs,
			ErrorMessage: errMsg,
		})
	}
}

func (e *engine) emitNodeStarted(ctx context.Context, run *workflowRun, workflowID string, node Node, shared map[string]any) {
	if e.monitorSvc == nil || run == nil {
		return
	}
	_ = e.monitorSvc.AppendEvent(ctx, monitor.AppendEventInput{
		RunID:         run.result.RunID,
		TaskID:        run.taskID,
		WorkflowID:    workflowID,
		UserID:        run.userID,
		AgentID:       node.AgentID,
		NodeID:        node.ID,
		EventType:     monitor.EventTypeNodeStarted,
		Status:        monitor.StatusRunning,
		Message:       fmt.Sprintf("node %s (%s) started", node.ID, node.Type),
		InputSnapshot: shared,
	})

	if node.Type == NodeTypeTool {
		_ = e.monitorSvc.AppendEvent(ctx, monitor.AppendEventInput{
			RunID:      run.result.RunID,
			TaskID:     run.taskID,
			WorkflowID: workflowID,
			UserID:     run.userID,
			AgentID:    node.AgentID,
			NodeID:     node.ID,
			EventType:  monitor.EventTypeToolCalled,
			Status:     monitor.StatusRunning,
			Message:    fmt.Sprintf("tool node %s called", node.ID),
		})
		if node.Config != nil && strings.EqualFold(strings.TrimSpace(fmt.Sprint(node.Config["tool_name"])), "call_agent") {
			_ = e.monitorSvc.AppendEvent(ctx, monitor.AppendEventInput{
				RunID:      run.result.RunID,
				TaskID:     run.taskID,
				WorkflowID: workflowID,
				UserID:     run.userID,
				AgentID:    node.AgentID,
				NodeID:     node.ID,
				EventType:  monitor.EventTypeAgentCalled,
				Status:     monitor.StatusRunning,
				Message:    fmt.Sprintf("agent called by node %s", node.ID),
			})
		}
	}

	if node.Type == NodeTypeChatModel {
		_ = e.monitorSvc.AppendEvent(ctx, monitor.AppendEventInput{
			RunID:      run.result.RunID,
			TaskID:     run.taskID,
			WorkflowID: workflowID,
			UserID:     run.userID,
			AgentID:    node.AgentID,
			NodeID:     node.ID,
			EventType:  monitor.EventTypeModelCalled,
			Status:     monitor.StatusRunning,
			Message:    fmt.Sprintf("chat model called by node %s", node.ID),
		})
	}
}

func (e *engine) emitNodeFinished(ctx context.Context, run *workflowRun, workflowID string, node Node, nodeRes NodeRunResult, durationMs int64) {
	if e.monitorSvc == nil || run == nil {
		return
	}
	_ = e.monitorSvc.AppendEvent(ctx, monitor.AppendEventInput{
		RunID:          run.result.RunID,
		TaskID:         run.taskID,
		WorkflowID:     workflowID,
		UserID:         run.userID,
		AgentID:        node.AgentID,
		NodeID:         node.ID,
		EventType:      monitor.EventTypeNodeFinished,
		Status:         monitor.StatusSucceeded,
		Message:        fmt.Sprintf("node %s (%s) finished", node.ID, node.Type),
		OutputSnapshot: nodeRes.Output,
		DurationMs:     durationMs,
	})

	if e.monitorSvc.Rules().IsNodeSlow(durationMs) {
		_ = e.monitorSvc.TriggerAlert(ctx, monitor.TriggerAlertInput{
			RunID:       run.result.RunID,
			WorkflowID:  workflowID,
			TaskID:      run.taskID,
			UserID:      run.userID,
			AgentID:     node.AgentID,
			NodeID:      node.ID,
			AlertType:   "node_slow",
			Severity:    "medium",
			Title:       "Node execution is slow",
			Content:     fmt.Sprintf("node duration %dms exceeds threshold %dms", durationMs, e.monitorSvc.Rules().NodeSlowThresholdMs),
			Status:      "open",
			TriggeredAt: time.Now(),
		})
	}
}

func (e *engine) emitNodeFailed(ctx context.Context, run *workflowRun, workflowID string, node Node, nodeRes NodeRunResult, errMsg string, durationMs int64) {
	if e.monitorSvc == nil || run == nil {
		return
	}
	_ = e.monitorSvc.AppendEvent(ctx, monitor.AppendEventInput{
		RunID:          run.result.RunID,
		TaskID:         run.taskID,
		WorkflowID:     workflowID,
		UserID:         run.userID,
		AgentID:        node.AgentID,
		NodeID:         node.ID,
		EventType:      monitor.EventTypeNodeFailed,
		Status:         monitor.StatusFailed,
		Message:        fmt.Sprintf("node %s (%s) failed", node.ID, node.Type),
		OutputSnapshot: nodeRes.Output,
		ErrorMessage:   errMsg,
		DurationMs:     durationMs,
	})
	_ = e.monitorSvc.TriggerAlert(ctx, monitor.TriggerAlertInput{
		RunID:       run.result.RunID,
		WorkflowID:  workflowID,
		TaskID:      run.taskID,
		UserID:      run.userID,
		AgentID:     node.AgentID,
		NodeID:      node.ID,
		AlertType:   "node_failure",
		Severity:    "high",
		Title:       "Node execution failed",
		Content:     errMsg,
		Status:      "open",
		TriggeredAt: time.Now(),
	})
}

func runStateToMonitorStatus(state RunState) monitor.EventStatus {
	switch state {
	case RunStateSucceeded:
		return monitor.StatusSucceeded
	case RunStateRunning:
		return monitor.StatusRunning
	case RunStatePaused:
		return monitor.StatusPending
	case RunStateCanceled:
		return monitor.StatusFailed
	default:
		return monitor.StatusFailed
	}
}

func mapString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok {
		return ""
	}
	s := strings.TrimSpace(fmt.Sprint(v))
	if s == "<nil>" {
		return ""
	}
	return s
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// buildNextIndex 构建下一个节点索引
func buildNextIndex(wf *Workflow) map[string][]string {
	next := make(map[string][]string, len(wf.Nodes))
	for _, edge := range wf.Edges {
		next[edge.From] = append(next[edge.From], edge.To)
	}
	return next
}

// resolveTaskNext 解析任务节点的下一个节点
func resolveTaskNext(node Node, options []string) (string, error) {
	// 检查元数据
	if node.Metadata != nil {
		if to := strings.TrimSpace(node.Metadata["next_to"]); to != "" {
			return to, nil
		}
	}
	// 根据选项数量处理
	switch len(options) {
	case 0:
		return "", nil
	case 1:
		return options[0], nil
	default:
		return "", fmt.Errorf("task node %s has multiple next nodes; set metadata.next_to", node.ID)
	}
}

// resolveConditionNext 解析条件节点的下一个节点
func resolveConditionNext(node Node, options []string, matched bool) (string, error) {
	// 检查元数据
	if node.Metadata != nil {
		trueTo := strings.TrimSpace(node.Metadata["true_to"])
		falseTo := strings.TrimSpace(node.Metadata["false_to"])
		if matched && trueTo != "" {
			return trueTo, nil
		}
		if !matched && falseTo != "" {
			return falseTo, nil
		}
	}

	// 根据选项数量处理
	switch len(options) {
	case 0:
		return "", nil
	case 1:
		return options[0], nil
	case 2:
		if matched {
			return options[0], nil
		}
		return options[1], nil
	default:
		return "", fmt.Errorf("condition node %s has %d next nodes; set metadata.true_to/false_to", node.ID, len(options))
	}
}

// resolveLoopNext 解析循环节点的下一个节点
func resolveLoopNext(node Node, wf *Workflow, options []string, loopContinue bool) (string, error) {
	if wf != nil {
		if loopContinue {
			if to := resolveEdgeTargetByLabels(wf, node.ID, []string{"body", "loop", "true"}); to != "" {
				return to, nil
			}
		} else {
			if to := resolveEdgeTargetByLabels(wf, node.ID, []string{"break", "exit", "false"}); to != "" {
				return to, nil
			}
		}
	}
	if len(options) == 2 {
		if loopContinue {
			return options[0], nil
		}
		return options[1], nil
	}

	if node.LoopConfig == nil {
		return "", fmt.Errorf("loop node %s missing loop config", node.ID)
	}
	if loopContinue {
		if node.LoopConfig.ContinueTo == "" {
			return "", fmt.Errorf("loop node %s missing continue target", node.ID)
		}
		return node.LoopConfig.ContinueTo, nil
	}
	if node.LoopConfig.ExitTo == "" {
		return "", fmt.Errorf("loop node %s missing exit target", node.ID)
	}
	return node.LoopConfig.ExitTo, nil
}

// cloneRunResult 克隆运行结果
func cloneRunResult(in RunResult) RunResult {
	out := in
	out.FinalOutput = cloneAnyMap(in.FinalOutput)
	out.NodeResults = append([]NodeRunResult(nil), in.NodeResults...)
	for idx := range out.NodeResults {
		out.NodeResults[idx].Output = cloneAnyMap(out.NodeResults[idx].Output)
	}
	return out
}

// evaluateCondition 评估条件表达式
func evaluateCondition(expr string, shared map[string]any) bool {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return true
	}
	// 处理 == 操作
	if strings.Contains(expr, "==") {
		parts := strings.SplitN(expr, "==", 2)
		left := strings.TrimSpace(parts[0])
		right := strings.TrimSpace(parts[1])
		val, ok := shared[left]
		if !ok {
			return false
		}
		return fmt.Sprint(val) == right
	}
	// 处理 != 操作
	if strings.Contains(expr, "!=") {
		parts := strings.SplitN(expr, "!=", 2)
		left := strings.TrimSpace(parts[0])
		right := strings.TrimSpace(parts[1])
		val, ok := shared[left]
		if !ok {
			return false
		}
		return fmt.Sprint(val) != right
	}
	// 处理布尔值或非空检查
	val, ok := shared[expr]
	if !ok {
		return false
	}
	b, ok := val.(bool)
	if ok {
		return b
	}
	return fmt.Sprint(val) != ""
}

func evaluateConditionNode(node Node, shared map[string]any) bool {
	if node.Config != nil {
		left := resolveConditionOperand(node.Config, shared, "left")
		right := resolveConditionOperand(node.Config, shared, "right")
		op, _ := node.Config["operator"].(string)
		op = strings.ToLower(strings.TrimSpace(op))
		if op == "" {
			op = "eq"
		}

		switch op {
		case "eq", "==", "=":
			return fmt.Sprint(left) == fmt.Sprint(right)
		case "gt", ">":
			return compareOperand(left, right) > 0
		case "lt", "<":
			return compareOperand(left, right) < 0
		}
	}
	return evaluateCondition(node.Condition, shared)
}

func compareOperand(left any, right any) int {
	lf, lok := toFloat(left)
	rf, rok := toFloat(right)
	if lok && rok {
		switch {
		case lf > rf:
			return 1
		case lf < rf:
			return -1
		default:
			return 0
		}
	}
	ls := fmt.Sprint(left)
	rs := fmt.Sprint(right)
	switch {
	case ls > rs:
		return 1
	case ls < rs:
		return -1
	default:
		return 0
	}
}

func toFloat(v any) (float64, bool) {
	switch vv := v.(type) {
	case int:
		return float64(vv), true
	case int64:
		return float64(vv), true
	case float64:
		return vv, true
	case float32:
		return float64(vv), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(vv), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

func resolveConditionOperand(cfg map[string]any, shared map[string]any, side string) any {
	if side == "left" {
		return resolveConditionLeftOperand(shared)
	}

	typeKey := side + "_type"
	valueKey := side + "_value"
	t, _ := cfg[typeKey].(string)
	t = strings.ToLower(strings.TrimSpace(t))
	v, ok := cfg[valueKey]
	if !ok {
		return nil
	}
	if t == "bool" {
		return toBool(v)
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func resolveConditionLeftOperand(shared map[string]any) any {
	if out, ok := shared["latest_output"].(map[string]any); ok {
		if v, exists := out["response"]; exists {
			return v
		}
		if v, exists := out["result"]; exists {
			return v
		}
		if v, exists := out["query"]; exists {
			return v
		}
		return strings.TrimSpace(fmt.Sprint(out))
	}
	return ""
}

func updateSharedOutputState(shared map[string]any, nodeID string, output map[string]any) {
	if shared == nil || nodeID == "" || output == nil {
		return
	}

	shared["latest_output"] = cloneAnyMap(output)
	history, _ := shared["history_outputs"].([]any)
	history = append(history, map[string]any{
		"node_id": nodeID,
		"output":  cloneAnyMap(output),
	})
	shared["history_outputs"] = history
}

func seedInputQueryHistory(shared map[string]any) {
	if shared == nil {
		return
	}

	q := firstNonEmptyString(
		mapString(shared, "query"),
		mapString(shared, "text"),
		mapString(shared, "input"),
	)
	if q == "" {
		return
	}

	history, _ := shared["history_outputs"].([]any)
	if len(history) > 0 {
		if first, ok := history[0].(map[string]any); ok {
			if nodeID, _ := first["node_id"].(string); nodeID == "__input__" {
				return
			}
		}
	}

	entry := map[string]any{
		"node_id": "__input__",
		"output":  map[string]any{"query": q},
	}
	history = append([]any{entry}, history...)
	shared["history_outputs"] = history
}

func selectNodeInputText(node Node, shared map[string]any) string {
	if node.Type == NodeTypeLoop {
		return ""
	}

	source := "previous"
	if node.Type == NodeTypeCondition {
		source = "previous"
	} else if node.Config != nil {
		if v, ok := node.Config["input_source"].(string); ok {
			s := strings.ToLower(strings.TrimSpace(v))
			if s == "history" {
				source = "history"
			}
		}
	}

	if source == "history" {
		history, _ := shared["history_outputs"].([]any)
		if len(history) == 0 {
			return ""
		}
		parts := make([]string, 0, len(history))
		for _, item := range history {
			entry, ok := item.(map[string]any)
			if !ok {
				continue
			}
			out, ok := entry["output"].(map[string]any)
			if !ok {
				continue
			}
			parts = append(parts, strings.TrimSpace(fmt.Sprint(out)))
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	}

	if out, ok := shared["latest_output"].(map[string]any); ok {
		return strings.TrimSpace(fmt.Sprint(out))
	}

	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func resolveSharedPath(shared map[string]any, path string) (any, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, false
	}
	if v, ok := shared[path]; ok {
		return v, true
	}
	parts := strings.Split(path, ".")
	if len(parts) < 2 {
		return nil, false
	}
	cur, ok := shared[parts[0]]
	if !ok {
		return nil, false
	}
	for i := 1; i < len(parts); i++ {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		next, ok := m[parts[i]]
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

func toBool(v any) bool {
	switch vv := v.(type) {
	case bool:
		return vv
	case string:
		s := strings.ToLower(strings.TrimSpace(vv))
		return s == "true" || s == "1" || s == "yes"
	case int:
		return vv != 0
	case float64:
		return vv != 0
	default:
		return strings.TrimSpace(fmt.Sprint(v)) != ""
	}
}

func resolveEdgeTargetByLabels(wf *Workflow, from string, labels []string) string {
	for _, label := range labels {
		for _, edge := range wf.Edges {
			if edge.From == from && strings.EqualFold(strings.TrimSpace(edge.Label), label) {
				return edge.To
			}
		}
	}
	return ""
}

func composeNodeInput(preInput string, source string) string {
	left := strings.TrimSpace(preInput)
	right := strings.TrimSpace(source)
	if left == "" {
		return right
	}
	if right == "" {
		return left
	}
	return left + "\n" + right
}

func extractQueryFromNodeOutput(output map[string]any) string {
	if output == nil {
		return ""
	}
	if v, ok := output["query"]; ok {
		if s := strings.TrimSpace(fmt.Sprint(v)); s != "" {
			return s
		}
	}
	if v, ok := output["response"]; ok {
		if s := strings.TrimSpace(fmt.Sprint(v)); s != "" {
			return s
		}
	}
	if v, ok := output["result"]; ok {
		if s := strings.TrimSpace(fmt.Sprint(v)); s != "" {
			return s
		}
	}
	return ""
}
