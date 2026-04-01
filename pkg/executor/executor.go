package executor

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"ai/pkg/logger"
	"ai/pkg/monitor"
	"ai/pkg/orchestrator"
	"ai/pkg/tools"
)

type ExecutionConfig struct {
	DefaultTimeoutSec int
	MaxIterations     int
}

type InterpretiveExecutor struct {
	config       ExecutionConfig
	toolRegistry *tools.ToolRegistry
	orchestrator orchestrator.Engine
	monitorSvc   *monitor.Service
	nodeHandlers map[string]NodeHandler
	mu           sync.RWMutex
	runningCtx   map[string]context.CancelFunc
}

func NewInterpretiveExecutor(config ExecutionConfig, toolRegistry *tools.ToolRegistry) *InterpretiveExecutor {
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

func (e *InterpretiveExecutor) ExecuteWorkflow(ctx context.Context, wf *orchestrator.Workflow, input map[string]any) (*ExecutionResult, error) {
	logger.Infof("[Executor] ExecuteWorkflow start workflowId=%s", wf.ID)

	runID := fmt.Sprintf("%s:run:%d", wf.ID, generateRunID())
	startedAt := time.Now()
	userID := firstNonEmptyString(stringValueFromAny(input["user_id"]), stringValueFromAny(input["userId"]))
	taskID := firstNonEmptyString(stringValueFromAny(input["task_id"]), stringValueFromAny(input["taskId"]))
	sourceAgentID := firstNonEmptyString(
		stringValueFromAny(input["source_agent_id"]),
		stringValueFromAny(input["sourceAgentId"]),
		stringValueFromAny(input["agent_id"]),
		stringValueFromAny(input["agentId"]),
	)

	ctx, cancel := context.WithCancel(ctx)
	e.mu.Lock()
	e.runningCtx[runID] = cancel
	e.mu.Unlock()

	defer func() {
		e.mu.Lock()
		delete(e.runningCtx, runID)
		e.mu.Unlock()
	}()

	shared := cloneMap(input)
	if shared == nil {
		shared = make(map[string]any)
	}

	result := &ExecutionResult{
		RunID:       runID,
		WorkflowID:  wf.ID,
		State:       ExecutionStateRunning,
		NodeResults: make([]NodeExecutionResult, 0),
	}

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

	currentNodeID := wf.StartNodeID
	iterations := 0

	for currentNodeID != "" && iterations < e.config.MaxIterations {
		iterations++

		if ctx.Err() != nil {
			result.State = ExecutionStateCanceled
			result.Error = ctx.Err().Error()
			return result, nil
		}

		node, ok := wf.Nodes[currentNodeID]
		if !ok {
			result.State = ExecutionStateFailed
			result.Error = fmt.Sprintf("node %s not found", currentNodeID)
			return result, nil
		}

		handler, ok := e.nodeHandlers[string(node.Type)]
		if !ok {
			result.State = ExecutionStateFailed
			result.Error = fmt.Sprintf("node type %s not implemented", node.Type)
			return result, nil
		}

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

		nodeResult, nextNodeID, err := handler(ctx, wf, node, shared)
		if err != nil {
			result.State = ExecutionStateFailed
			result.Error = err.Error()
			result.NodeResults = append(result.NodeResults, nodeResult)
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

		result.NodeResults = append(result.NodeResults, nodeResult)
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

	if iterations >= e.config.MaxIterations {
		result.State = ExecutionStateFailed
		result.Error = "max iterations exceeded"
		return result, nil
	}

	result.State = ExecutionStateSucceeded
	result.Output = shared
	logger.Infof("[Executor] ExecuteWorkflow done runId=%s state=%s", runID, result.State)

	return result, nil
}

func (e *InterpretiveExecutor) SetMonitorService(service *monitor.Service) {
	e.monitorSvc = service
}

func (e *InterpretiveExecutor) finalizeMonitorRun(ctx context.Context, startedAt time.Time, result *ExecutionResult) {
	if e.monitorSvc == nil || result == nil {
		return
	}
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

func (e *InterpretiveExecutor) emitToolInvocationEvents(ctx context.Context, runID, workflowID, userID, taskID, sourceAgentID string, node orchestrator.Node) {
	if e.monitorSvc == nil || node.Type != orchestrator.NodeTypeTool {
		return
	}
	toolName := ""
	if node.Config != nil {
		toolName = strings.TrimSpace(fmt.Sprint(node.Config["tool_name"]))
	}
	if toolName == "" {
		toolName = node.AgentID
	}
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

func stringValueFromAny(v any) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func (e *InterpretiveExecutor) ExecuteWorkflowFromDefinition(ctx context.Context, def *WorkflowDefinition, input map[string]any) (*ExecutionResult, error) {
	wf, err := DefinitionToWorkflow(def)
	if err != nil {
		return nil, fmt.Errorf("convert definition to workflow: %w", err)
	}
	return e.ExecuteWorkflow(ctx, wf, input)
}

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

func (e *InterpretiveExecutor) RegisterTool(tool tools.Tool) error {
	return e.toolRegistry.Register(tool)
}

func (e *InterpretiveExecutor) GetToolRegistry() *tools.ToolRegistry {
	return e.toolRegistry
}

type ExecutionState string

const (
	ExecutionStateRunning   ExecutionState = "running"
	ExecutionStateSucceeded ExecutionState = "succeeded"
	ExecutionStateFailed    ExecutionState = "failed"
	ExecutionStateCanceled  ExecutionState = "canceled"
)

type ExecutionResult struct {
	RunID       string                `json:"runId"`
	WorkflowID  string                `json:"workflowId"`
	State       ExecutionState        `json:"state"`
	Output      map[string]any        `json:"output,omitempty"`
	Error       string                `json:"error,omitempty"`
	NodeResults []NodeExecutionResult `json:"nodeResults"`
}

type NodeExecutionResult struct {
	NodeID   string         `json:"nodeId"`
	NodeType string         `json:"nodeType"`
	State    ExecutionState `json:"state"`
	Output   map[string]any `json:"output,omitempty"`
	Error    string         `json:"error,omitempty"`
	Duration int64          `json:"duration"`
}

type WorkflowDefinition struct {
	WorkflowID  string    `json:"workflowId"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	StartNodeID string    `json:"startNodeId"`
	Nodes       []NodeDef `json:"nodes"`
	Edges       []EdgeDef `json:"edges"`
}

type NodeDef struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	Config     map[string]any    `json:"config,omitempty"`
	AgentID    string            `json:"agentId,omitempty"`
	TaskType   string            `json:"taskType,omitempty"`
	Condition  string            `json:"condition,omitempty"`
	PreInput   string            `json:"preInput,omitempty"`
	LoopConfig map[string]any    `json:"loopConfig,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

type EdgeDef struct {
	From    string         `json:"from"`
	To      string         `json:"to"`
	Label   string         `json:"label,omitempty"`
	Mapping map[string]any `json:"mapping,omitempty"`
}

func DefinitionToWorkflow(def *WorkflowDefinition) (*orchestrator.Workflow, error) {
	wf, err := orchestrator.NewWorkflow(def.WorkflowID, def.Name)
	if err != nil {
		return nil, err
	}

	wf.StartNodeID = def.StartNodeID
	if wf.StartNodeID == "" && len(def.Nodes) > 0 {
		wf.StartNodeID = def.Nodes[0].ID
	}

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

		if nodeDef.LoopConfig != nil {
			node.LoopConfig = &orchestrator.LoopConfig{
				MaxIterations: getIntFromMap(nodeDef.LoopConfig, "max_iterations", 10),
				ContinueTo:    getStringFromMap(nodeDef.LoopConfig, "continue_to"),
				ExitTo:        getStringFromMap(nodeDef.LoopConfig, "exit_to"),
			}
		}

		if err := wf.AddNode(node); err != nil {
			return nil, fmt.Errorf("add node %s: %w", nodeDef.ID, err)
		}
	}

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

func generateRunID() uint64 {
	return uint64(time.Now().UnixNano())
}

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

func getStringFromMap(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
