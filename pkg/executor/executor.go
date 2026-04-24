package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"ai/pkg/logger"
	"ai/pkg/monitor"
	"ai/pkg/orchestrator"
	"ai/pkg/tools"
)

// ExecutionConfig 执行器配置
type ExecutionConfig struct {
	DefaultTimeoutSec int // 默认超时时间（秒）
	MaxIterations     int // 最大迭代次数
}

// InterpretiveExecutor 解释型执行器
type InterpretiveExecutor struct {
	config       ExecutionConfig               // 执行器配置
	toolRegistry *tools.ToolRegistry           // 工具注册表
	orchestrator orchestrator.Engine           // 编排引擎
	monitorSvc   *monitor.Service              // 监控服务
	nodeHandlers map[string]NodeHandler        // 节点处理器映射
	mu           sync.RWMutex                  // 读写锁
	runningCtx   map[string]context.CancelFunc // 运行中的上下文
}

// NewInterpretiveExecutor 创建新的解释型执行器
// 参数:
//
//	config - 执行器配置
//	toolRegistry - 工具注册表
//
// 返回值:
//
//	新创建的解释型执行器实例
func NewInterpretiveExecutor(config ExecutionConfig, toolRegistry *tools.ToolRegistry) *InterpretiveExecutor {
	// 设置默认配置值
	if config.DefaultTimeoutSec <= 0 {
		config.DefaultTimeoutSec = 600
	}
	if config.MaxIterations <= 0 {
		config.MaxIterations = 100
	}

	e := &InterpretiveExecutor{
		config:       config,
		toolRegistry: toolRegistry,
		orchestrator: orchestrator.NewEngine(orchestrator.Config{
			DefaultTaskTimeoutSec: config.DefaultTimeoutSec,
			RetryMaxAttempts:      3,
			RetryBaseBackoffMs:    200,
			RetryMaxBackoffMs:     5000,
		}, orchestrator.NewInMemoryAgentRegistry()),
		runningCtx: make(map[string]context.CancelFunc),
	}

	// 注册节点处理器
	e.nodeHandlers = map[string]NodeHandler{
		"start":      e.handleStartNode,
		"end":        e.handleEndNode,
		"pre_input":  e.handlePreInputNode,
		"tool":       e.handleToolNode,
		"chat_model": e.handleChatModelNode,
		"condition":  e.handleConditionNode,
		"loop":       e.handleLoopNode,
	}

	return e
}

// ExecuteWorkflow 执行工作流
// 参数:
//
//	ctx - 上下文
//	wf - 工作流定义
//	input - 输入参数
//
// 返回值:
//
//	执行结果和错误
func (e *InterpretiveExecutor) ExecuteWorkflow(ctx context.Context, wf *orchestrator.Workflow, input map[string]any) (*ExecutionResult, error) {
	logger.Infof("[Executor] ExecuteWorkflow start workflowId=%s", wf.ID)

	// 生成运行ID
	runID := fmt.Sprintf("%s:run:%d", wf.ID, generateRunID())
	startedAt := time.Now()
	// 提取用户ID、任务ID和源代理ID
	userID := firstNonEmptyString(stringValueFromAny(input["user_id"]), stringValueFromAny(input["userId"]))
	taskID := firstNonEmptyString(stringValueFromAny(input["task_id"]), stringValueFromAny(input["taskId"]))
	sourceAgentID := firstNonEmptyString(
		stringValueFromAny(input["source_agent_id"]),
		stringValueFromAny(input["sourceAgentId"]),
		stringValueFromAny(input["agent_id"]),
		stringValueFromAny(input["agentId"]),
	)

	// 创建可取消的上下文
	ctx, cancel := context.WithCancel(ctx)
	e.mu.Lock()
	e.runningCtx[runID] = cancel
	e.mu.Unlock()

	defer func() {
		e.mu.Lock()
		delete(e.runningCtx, runID)
		e.mu.Unlock()
	}()

	// 克隆输入数据
	shared := cloneMap(input)
	if shared == nil {
		shared = make(map[string]any)
	}
	// 初始化输入查询历史
	seedInputQueryHistory(shared)
	testDebugEnabled := boolValueFromAny(shared["_debug_test_exec"])
	testDebugRequestID := stringValueFromAny(shared["_debug_test_request_id"])
	if testDebugEnabled {
		logger.Infof("[Executor][TestDebug] run_start runId=%s workflowId=%s startNode=%s sharedKeys=%v requestId=%s", runID, wf.ID, wf.StartNodeID, mapKeysAny(shared), testDebugRequestID)
	}

	// 初始化执行结果
	result := &ExecutionResult{
		RunID:       runID,
		WorkflowID:  wf.ID,
		State:       ExecutionStateRunning,
		NodeResults: make([]NodeExecutionResult, 0),
	}

	// 创建监控运行记录
	if e.monitorSvc != nil {
		_ = e.monitorSvc.CreateRun(ctx, monitor.CreateRunInput{
			RunID:         runID,
			WorkflowID:    wf.ID,
			UserID:        userID,
			SourceAgentID: sourceAgentID,
			TaskID:        taskID,
			Status:        monitor.StatusRunning,
			StartedAt:     startedAt,
		})
	}

	defer e.finalizeMonitorRun(ctx, startedAt, result)

	// 开始执行节点
	currentNodeID := wf.StartNodeID
	iterations := 0

	// 循环执行节点直到结束或达到最大迭代次数
	for currentNodeID != "" && iterations < e.config.MaxIterations {
		iterations++

		// 检查上下文是否已取消
		if ctx.Err() != nil {
			result.State = ExecutionStateCanceled
			result.Error = ctx.Err().Error()
			return result, nil
		}

		// 获取当前节点
		node, ok := wf.Nodes[currentNodeID]
		if !ok {
			result.State = ExecutionStateFailed
			result.Error = fmt.Sprintf("node %s not found", currentNodeID)
			return result, nil
		}

		// 获取节点处理器
		handler, ok := e.nodeHandlers[string(node.Type)]
		if !ok {
			result.State = ExecutionStateFailed
			result.Error = fmt.Sprintf("node type %s not implemented", node.Type)
			return result, nil
		}

		if testDebugEnabled {
			logger.Infof("[Executor][TestDebug] node_start runId=%s requestId=%s step=%d nodeId=%s nodeType=%s sharedKeys=%v", runID, testDebugRequestID, iterations, node.ID, node.Type, mapKeysAny(shared))
			logger.Infof("[Executor][TestDebug] node_input runId=%s requestId=%s step=%d nodeId=%s input=%s", runID, testDebugRequestID, iterations, node.ID, snapshotForLog(shared, 3000))
		}

		// 更新监控状态
		if e.monitorSvc != nil {
			_ = e.monitorSvc.UpdateCurrentNode(ctx, runID, node.ID)
			_ = e.monitorSvc.AppendEvent(ctx, monitor.AppendEventInput{
				RunID:         runID,
				TaskID:        taskID,
				WorkflowID:    wf.ID,
				UserID:        userID,
				AgentID:       sourceAgentID,
				NodeID:        node.ID,
				EventType:     monitor.EventTypeNodeStarted,
				Status:        monitor.StatusRunning,
				Message:       fmt.Sprintf("node %s started", node.ID),
				InputSnapshot: shared,
			})
			e.emitToolInvocationEvents(ctx, runID, wf.ID, userID, taskID, sourceAgentID, node)
		}

		// 执行节点
		nodeResult, nextNodeID, err := handler(ctx, wf, node, shared)
		if err != nil {
			if testDebugEnabled {
				logger.Warnf("[Executor][TestDebug] node_failed runId=%s requestId=%s step=%d nodeId=%s nodeType=%s durationMs=%d next=%s err=%v", runID, testDebugRequestID, iterations, node.ID, node.Type, nodeResult.Duration, nextNodeID, err)
				logger.Warnf("[Executor][TestDebug] node_output runId=%s requestId=%s step=%d nodeId=%s output=%s", runID, testDebugRequestID, iterations, node.ID, snapshotForLog(nodeResult.Output, 3000))
			}
			result.State = ExecutionStateFailed
			result.Error = err.Error()
			result.NodeResults = append(result.NodeResults, nodeResult)
			// 记录失败事件和触发告警
			if e.monitorSvc != nil {
				_ = e.monitorSvc.AppendEvent(ctx, monitor.AppendEventInput{
					RunID:          runID,
					TaskID:         taskID,
					WorkflowID:     wf.ID,
					UserID:         userID,
					AgentID:        sourceAgentID,
					NodeID:         node.ID,
					EventType:      monitor.EventTypeNodeFailed,
					Status:         monitor.StatusFailed,
					Message:        fmt.Sprintf("node %s failed", node.ID),
					OutputSnapshot: nodeResult.Output,
					ErrorMessage:   err.Error(),
					DurationMs:     nodeResult.Duration,
				})
				_ = e.monitorSvc.TriggerAlert(ctx, monitor.TriggerAlertInput{
					RunID:       runID,
					WorkflowID:  wf.ID,
					TaskID:      taskID,
					UserID:      userID,
					AgentID:     sourceAgentID,
					NodeID:      node.ID,
					AlertType:   "node_failure",
					Severity:    "high",
					Title:       "Node execution failed",
					Content:     err.Error(),
					Status:      "open",
					TriggeredAt: time.Now(),
				})
			}
			return result, nil
		}
		if testDebugEnabled {
			logger.Infof("[Executor][TestDebug] node_done runId=%s requestId=%s step=%d nodeId=%s nodeType=%s state=%s durationMs=%d next=%s outputKeys=%v", runID, testDebugRequestID, iterations, node.ID, node.Type, nodeResult.State, nodeResult.Duration, nextNodeID, mapKeysAny(nodeResult.Output))
			logger.Infof("[Executor][TestDebug] node_output runId=%s requestId=%s step=%d nodeId=%s output=%s", runID, testDebugRequestID, iterations, node.ID, snapshotForLog(nodeResult.Output, 3000))
		}
		// 更新共享输出状态
		updateSharedOutputState(shared, node.ID, nodeResult.Output)

		result.NodeResults = append(result.NodeResults, nodeResult)
		// 记录节点完成事件
		if e.monitorSvc != nil {
			_ = e.monitorSvc.AppendEvent(ctx, monitor.AppendEventInput{
				RunID:          runID,
				TaskID:         taskID,
				WorkflowID:     wf.ID,
				UserID:         userID,
				AgentID:        sourceAgentID,
				NodeID:         node.ID,
				EventType:      monitor.EventTypeNodeFinished,
				Status:         monitor.StatusSucceeded,
				Message:        fmt.Sprintf("node %s finished", node.ID),
				OutputSnapshot: nodeResult.Output,
				DurationMs:     nodeResult.Duration,
			})
			// 检查节点执行是否缓慢
			if e.monitorSvc.Rules().IsNodeSlow(nodeResult.Duration) {
				_ = e.monitorSvc.TriggerAlert(ctx, monitor.TriggerAlertInput{
					RunID:       runID,
					WorkflowID:  wf.ID,
					TaskID:      taskID,
					UserID:      userID,
					AgentID:     sourceAgentID,
					NodeID:      node.ID,
					AlertType:   "node_slow",
					Severity:    "medium",
					Title:       "Node execution is slow",
					Content:     fmt.Sprintf("node duration %dms exceeds threshold %dms", nodeResult.Duration, e.monitorSvc.Rules().NodeSlowThresholdMs),
					Status:      "open",
					TriggeredAt: time.Now(),
				})
			}
		}
		currentNodeID = nextNodeID
	}

	// 检查是否超过最大迭代次数
	if iterations >= e.config.MaxIterations {
		result.State = ExecutionStateFailed
		result.Error = "max iterations exceeded"
		if testDebugEnabled {
			logger.Warnf("[Executor][TestDebug] run_failed runId=%s requestId=%s reason=max_iterations_exceeded maxIterations=%d", runID, testDebugRequestID, e.config.MaxIterations)
		}
		return result, nil
	}

	// 执行成功
	result.State = ExecutionStateSucceeded
	result.Output = shared
	logger.Infof("[Executor] ExecuteWorkflow done runId=%s state=%s", runID, result.State)
	if testDebugEnabled {
		logger.Infof("[Executor][TestDebug] run_done runId=%s requestId=%s state=%s nodeResults=%d", runID, testDebugRequestID, result.State, len(result.NodeResults))
	}

	return result, nil
}

// SetMonitorService 设置监控服务
// 参数:
//
//	service - 监控服务实例
func (e *InterpretiveExecutor) SetMonitorService(service *monitor.Service) {
	e.monitorSvc = service
}

// finalizeMonitorRun 完成监控运行记录
// 参数:
//
//	ctx - 上下文
//	startedAt - 开始时间
//	result - 执行结果
func (e *InterpretiveExecutor) finalizeMonitorRun(ctx context.Context, startedAt time.Time, result *ExecutionResult) {
	if e.monitorSvc == nil || result == nil {
		return
	}
	// 根据执行状态确定监控状态
	status := monitor.StatusFailed
	switch result.State {
	case ExecutionStateSucceeded:
		status = monitor.StatusSucceeded
	case ExecutionStateRunning:
		status = monitor.StatusRunning
	}
	_ = e.monitorSvc.FinishRun(ctx, monitor.FinishRunInput{
		RunID:        result.RunID,
		Status:       status,
		FinishedAt:   time.Now(),
		DurationMs:   time.Since(startedAt).Milliseconds(),
		ErrorMessage: result.Error,
	})
}

// emitToolInvocationEvents 发出工具调用事件
// 参数:
//
//	ctx - 上下文
//	runID - 运行ID
//	workflowID - 工作流ID
//	userID - 用户ID
//	taskID - 任务ID
//	sourceAgentID - 源代理ID
//	node - 节点
func (e *InterpretiveExecutor) emitToolInvocationEvents(ctx context.Context, runID, workflowID, userID, taskID, sourceAgentID string, node orchestrator.Node) {
	if e.monitorSvc == nil || node.Type != orchestrator.NodeTypeTool {
		return
	}
	// 获取工具名称
	toolName := ""
	if node.Config != nil {
		toolName = strings.TrimSpace(fmt.Sprint(node.Config["tool_name"]))
	}
	if toolName == "" {
		toolName = node.AgentID
	}
	// 发出工具调用事件
	_ = e.monitorSvc.AppendEvent(ctx, monitor.AppendEventInput{
		RunID:      runID,
		TaskID:     taskID,
		WorkflowID: workflowID,
		UserID:     userID,
		AgentID:    sourceAgentID,
		NodeID:     node.ID,
		EventType:  monitor.EventTypeToolCalled,
		Status:     monitor.StatusRunning,
		Message:    fmt.Sprintf("tool called: %s", toolName),
	})
	// 如果是调用代理工具，发出代理调用事件
	if toolName == "call_agent" {
		_ = e.monitorSvc.AppendEvent(ctx, monitor.AppendEventInput{
			RunID:      runID,
			TaskID:     taskID,
			WorkflowID: workflowID,
			UserID:     userID,
			AgentID:    sourceAgentID,
			NodeID:     node.ID,
			EventType:  monitor.EventTypeAgentCalled,
			Status:     monitor.StatusRunning,
			Message:    "agent called via call_agent tool",
		})
	}
}

// stringValueFromAny 从任意类型转换为字符串
// 参数:
//
//	v - 任意类型的值
//
// 返回值:
//
//	字符串值
func stringValueFromAny(v any) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

// boolValueFromAny 从任意类型转换为布尔值
// 参数:
//
//	v - 任意类型的值
//
// 返回值:
//
//	布尔值
func boolValueFromAny(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		s := strings.ToLower(strings.TrimSpace(x))
		return s == "1" || s == "true" || s == "yes" || s == "on"
	case int:
		return x != 0
	case int64:
		return x != 0
	case float64:
		return x != 0
	default:
		return false
	}
}

// mapKeysAny 获取map的所有键
// 参数:
//
//	m - map对象
//
// 返回值:
//
//	键列表
func mapKeysAny(m map[string]any) []string {
	if len(m) == 0 {
		return []string{}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// snapshotForLog 生成用于日志的快照
// 参数:
//
//	v - 任意类型的值
//	maxLen - 最大长度
//
// 返回值:
//
//	快照字符串
func snapshotForLog(v any, maxLen int) string {
	if v == nil {
		return "null"
	}
	b, err := json.Marshal(v)
	if err != nil {
		s := strings.TrimSpace(fmt.Sprintf("%v", v))
		if s == "" {
			return "<empty>"
		}
		if maxLen > 0 && len(s) > maxLen {
			return s[:maxLen] + "...<truncated>"
		}
		return s
	}
	s := string(b)
	if maxLen > 0 && len(s) > maxLen {
		return s[:maxLen] + "...<truncated>"
	}
	return s
}

// ExecuteWorkflowFromDefinition 从工作流定义执行工作流
// 参数:
//
//	ctx - 上下文
//	def - 工作流定义
//	input - 输入参数
//
// 返回值:
//
//	执行结果和错误
func (e *InterpretiveExecutor) ExecuteWorkflowFromDefinition(ctx context.Context, def *WorkflowDefinition, input map[string]any) (*ExecutionResult, error) {
	wf, err := DefinitionToWorkflow(def)
	if err != nil {
		return nil, fmt.Errorf("convert definition to workflow: %w", err)
	}
	return e.ExecuteWorkflow(ctx, wf, input)
}

// Cancel 取消运行中的工作流
// 参数:
//
//	runID - 运行ID
//
// 返回值:
//
//	错误
func (e *InterpretiveExecutor) Cancel(runID string) error {
	e.mu.Lock()
	cancel, ok := e.runningCtx[runID]
	e.mu.Unlock()

	if !ok {
		return fmt.Errorf("run %s not found", runID)
	}

	cancel()
	return nil
}

// RegisterTool 注册工具
// 参数:
//
//	tool - 工具实例
//
// 返回值:
//
//	错误
func (e *InterpretiveExecutor) RegisterTool(tool tools.Tool) error {
	return e.toolRegistry.Register(tool)
}

// GetToolRegistry 获取工具注册表
// 返回值:
//
//	工具注册表实例
func (e *InterpretiveExecutor) GetToolRegistry() *tools.ToolRegistry {
	return e.toolRegistry
}

// ExecutionState 执行状态类型
type ExecutionState string

const (
	ExecutionStateRunning   ExecutionState = "running"   // 运行中
	ExecutionStateSucceeded ExecutionState = "succeeded" // 成功
	ExecutionStateFailed    ExecutionState = "failed"    // 失败
	ExecutionStateCanceled  ExecutionState = "canceled"  // 已取消
)

// ExecutionResult 执行结果
type ExecutionResult struct {
	RunID       string                `json:"runId"`            // 运行ID
	WorkflowID  string                `json:"workflowId"`       // 工作流ID
	State       ExecutionState        `json:"state"`            // 执行状态
	Output      map[string]any        `json:"output,omitempty"` // 输出数据
	Error       string                `json:"error,omitempty"`  // 错误信息
	NodeResults []NodeExecutionResult `json:"nodeResults"`      // 节点执行结果列表
}

// NodeExecutionResult 节点执行结果
type NodeExecutionResult struct {
	NodeID   string         `json:"nodeId"`           // 节点ID
	NodeType string         `json:"nodeType"`         // 节点类型
	State    ExecutionState `json:"state"`            // 执行状态
	Output   map[string]any `json:"output,omitempty"` // 输出数据
	Error    string         `json:"error,omitempty"`  // 错误信息
	Duration int64          `json:"duration"`         // 执行时长（毫秒）
}

// WorkflowDefinition 工作流定义
type WorkflowDefinition struct {
	WorkflowID  string    `json:"workflowId"`  // 工作流ID
	Name        string    `json:"name"`        // 工作流名称
	Description string    `json:"description"` // 工作流描述
	StartNodeID string    `json:"startNodeId"` // 起始节点ID
	Nodes       []NodeDef `json:"nodes"`       // 节点列表
	Edges       []EdgeDef `json:"edges"`       // 边列表
}

// NodeDef 节点定义
type NodeDef struct {
	ID         string            `json:"id"`                   // 节点ID
	Type       string            `json:"type"`                 // 节点类型
	Config     map[string]any    `json:"config,omitempty"`     // 配置参数
	AgentID    string            `json:"agentId,omitempty"`    // 代理ID
	TaskType   string            `json:"taskType,omitempty"`   // 任务类型
	InputType  string            `json:"inputType,omitempty"`  // 输入类型
	OutputType string            `json:"outputType,omitempty"` // 输出类型
	Condition  string            `json:"condition,omitempty"`  // 条件表达式
	PreInput   string            `json:"preInput,omitempty"`   // 预输入
	LoopConfig map[string]any    `json:"loopConfig,omitempty"` // 循环配置
	Metadata   map[string]string `json:"metadata,omitempty"`   // 元数据
}

// EdgeDef 边定义
type EdgeDef struct {
	From    string         `json:"from"`              // 源节点ID
	To      string         `json:"to"`                // 目标节点ID
	Label   string         `json:"label,omitempty"`   // 边标签
	Mapping map[string]any `json:"mapping,omitempty"` // 数据映射
}

// DefinitionToWorkflow 将工作流定义转换为工作流对象
// 参数:
//
//	def - 工作流定义
//
// 返回值:
//
//	工作流对象和错误
func DefinitionToWorkflow(def *WorkflowDefinition) (*orchestrator.Workflow, error) {
	wf, err := orchestrator.NewWorkflow(def.WorkflowID, def.Name)
	if err != nil {
		return nil, err
	}

	// 设置起始节点ID
	wf.StartNodeID = def.StartNodeID
	if wf.StartNodeID == "" && len(def.Nodes) > 0 {
		wf.StartNodeID = def.Nodes[0].ID
	}

	// 添加节点
	for _, nodeDef := range def.Nodes {
		node := orchestrator.Node{
			ID:       nodeDef.ID,
			Type:     orchestrator.NodeType(nodeDef.Type),
			Config:   nodeDef.Config,
			AgentID:  nodeDef.AgentID,
			TaskType: nodeDef.TaskType,
			PreInput: nodeDef.PreInput,
			Metadata: nodeDef.Metadata,
		}

		if nodeDef.Condition != "" {
			node.Condition = nodeDef.Condition
		}

		// 设置循环配置
		if nodeDef.LoopConfig != nil {
			node.LoopConfig = &orchestrator.LoopConfig{
				MaxIterations: getIntFromMapMulti(nodeDef.LoopConfig, []string{"max_iterations", "maxIterations"}, 10),
				ContinueTo:    getStringFromMapMulti(nodeDef.LoopConfig, []string{"continue_to", "continueTo"}),
				ExitTo:        getStringFromMapMulti(nodeDef.LoopConfig, []string{"exit_to", "exitTo"}),
			}
		}

		if err := wf.AddNode(node); err != nil {
			return nil, fmt.Errorf("add node %s: %w", nodeDef.ID, err)
		}
	}

	// 添加边
	for _, edgeDef := range def.Edges {
		var mapping map[string]string
		if len(edgeDef.Mapping) > 0 {
			mapping = make(map[string]string, len(edgeDef.Mapping))
			for k, v := range edgeDef.Mapping {
				mapping[k] = fmt.Sprint(v)
			}
		}
		if err := wf.AddEdgeWithLabel(edgeDef.From, edgeDef.To, edgeDef.Label, mapping); err != nil {
			return nil, fmt.Errorf("add edge %s->%s: %w", edgeDef.From, edgeDef.To, err)
		}
	}

	return wf, nil
}

// generateRunID 生成运行ID
// 返回值:
//
//	运行ID
func generateRunID() uint64 {
	return uint64(time.Now().UnixNano())
}

// cloneMap 克隆map
// 参数:
//
//	m - 原始map
//
// 返回值:
//
//	克隆后的map
func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	result := make(map[string]any, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}

// seedInputQueryHistory 初始化输入查询历史
// 参数:
//
//	shared - 共享数据map
func seedInputQueryHistory(shared map[string]any) {
	if shared == nil {
		return
	}

	// 获取查询文本
	q := firstNonEmptyString(
		stringify(shared["query"]),
		stringify(shared["text"]),
		stringify(shared["input"]),
	)
	if q == "" {
		return
	}

	// 检查历史记录是否已存在输入节点
	history, _ := shared["history_outputs"].([]any)
	if len(history) > 0 {
		if first, ok := history[0].(map[string]any); ok {
			if nodeID, _ := first["node_id"].(string); nodeID == "__input__" {
				return
			}
		}
	}

	// 创建输入历史条目
	entry := map[string]any{
		"node_id": "__input__",
		"output":  map[string]any{"query": q},
	}
	history = append([]any{entry}, history...)
	shared["history_outputs"] = history
}

// updateSharedOutputState 更新共享输出状态
// 参数:
//
//	shared - 共享数据map
//	nodeID - 节点ID
//	output - 输出数据
func updateSharedOutputState(shared map[string]any, nodeID string, output map[string]any) {
	if shared == nil || nodeID == "" || output == nil {
		return
	}

	// 更新最新输出
	shared["latest_output"] = cloneAny(output)
	// 添加到历史记录
	history, _ := shared["history_outputs"].([]any)
	history = append(history, map[string]any{
		"node_id": nodeID,
		"output":  cloneAny(output),
	})
	shared["history_outputs"] = history
}

// cloneAny 克隆任意类型的值
// 参数:
//
//	v - 任意类型的值
//
// 返回值:
//
//	克隆后的值
func cloneAny(v any) any {
	switch vv := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(vv))
		for k, item := range vv {
			m[k] = cloneAny(item)
		}
		return m
	case []any:
		arr := make([]any, len(vv))
		for i := range vv {
			arr[i] = cloneAny(vv[i])
		}
		return arr
	default:
		return vv
	}
}

// getIntFromMap 从map中获取整数值
// 参数:
//
//	m - map对象
//	key - 键
//	defaultValue - 默认值
//
// 返回值:
//
//	整数值
func getIntFromMap(m map[string]any, key string, defaultValue int) int {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case int:
			return n
		case int64:
			return int(n)
		case float64:
			return int(n)
		}
	}
	return defaultValue
}

// getStringFromMap 从map中获取字符串值
// 参数:
//
//	m - map对象
//	key - 键
//
// 返回值:
//
//	字符串值
func getStringFromMap(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// getIntFromMapMulti 从map中获取多个键的整数值
// 参数:
//
//	m - map对象
//	keys - 键列表
//	defaultValue - 默认值
//
// 返回值:
//
//	整数值
func getIntFromMapMulti(m map[string]any, keys []string, defaultValue int) int {
	for _, key := range keys {
		if v := getIntFromMap(m, key, defaultValue); v != defaultValue {
			return v
		}
	}
	for _, key := range keys {
		if _, ok := m[key]; ok {
			return getIntFromMap(m, key, defaultValue)
		}
	}
	return defaultValue
}

// getStringFromMapMulti 从map中获取多个键的字符串值
// 参数:
//
//	m - map对象
//	keys - 键列表
//
// 返回值:
//
//	字符串值
func getStringFromMapMulti(m map[string]any, keys []string) string {
	for _, key := range keys {
		if v := getStringFromMap(m, key); v != "" {
			return v
		}
	}
	return ""
}
