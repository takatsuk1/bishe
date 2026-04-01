package executor

import (
	"ai/config"
	"ai/pkg/llm"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"ai/pkg/logger"
	"ai/pkg/orchestrator"
	"ai/pkg/tools"
)

type NodeHandler func(ctx context.Context, wf *orchestrator.Workflow, node orchestrator.Node, shared map[string]any) (NodeExecutionResult, string, error)

func (e *InterpretiveExecutor) handleStartNode(ctx context.Context, wf *orchestrator.Workflow, node orchestrator.Node, shared map[string]any) (NodeExecutionResult, string, error) {
	start := time.Now()
	logger.Infof("[Executor] handleStartNode nodeId=%s", node.ID)

	nextNodeID := e.getNextNode(wf, node.ID, "")

	result := NodeExecutionResult{
		NodeID:   node.ID,
		NodeType: "start",
		State:    ExecutionStateSucceeded,
		Duration: time.Since(start).Milliseconds(),
	}

	shared[node.ID] = map[string]any{"started": true}

	return result, nextNodeID, nil
}

func (e *InterpretiveExecutor) handleEndNode(ctx context.Context, wf *orchestrator.Workflow, node orchestrator.Node, shared map[string]any) (NodeExecutionResult, string, error) {
	start := time.Now()
	logger.Infof("[Executor] handleEndNode nodeId=%s", node.ID)

	result := NodeExecutionResult{
		NodeID:   node.ID,
		NodeType: "end",
		State:    ExecutionStateSucceeded,
		Duration: time.Since(start).Milliseconds(),
	}

	shared[node.ID] = map[string]any{"ended": true}

	return result, "", nil
}

func (e *InterpretiveExecutor) handlePreInputNode(ctx context.Context, wf *orchestrator.Workflow, node orchestrator.Node, shared map[string]any) (NodeExecutionResult, string, error) {
	start := time.Now()
	_ = ctx
	query := strings.TrimSpace(composePreInputQuery(node.PreInput, shared))
	if query == "" {
		query = firstNonEmptyString(
			stringify(shared["query"]),
			stringify(shared["text"]),
			stringify(shared["input"]),
		)
	}
	if query != "" {
		shared["query"] = query
	}
	output := map[string]any{"query": query}
	shared[node.ID] = output
	result := NodeExecutionResult{
		NodeID:   node.ID,
		NodeType: "pre_input",
		State:    ExecutionStateSucceeded,
		Output:   output,
		Duration: time.Since(start).Milliseconds(),
	}
	nextNodeID := e.getNextNode(wf, node.ID, "")
	return result, nextNodeID, nil
}

func (e *InterpretiveExecutor) handleToolNode(ctx context.Context, wf *orchestrator.Workflow, node orchestrator.Node, shared map[string]any) (NodeExecutionResult, string, error) {
	start := time.Now()
	logger.Infof("[Executor] handleToolNode nodeId=%s agentId=%s", node.ID, node.AgentID)

	result := NodeExecutionResult{
		NodeID:   node.ID,
		NodeType: "tool",
	}

	if s := strings.TrimSpace(node.PreInput); s != "" {
		shared["query"] = composePreInputQuery(s, shared)
	}

	toolName := ""
	if node.Config != nil {
		if name, ok := node.Config["tool_name"].(string); ok {
			toolName = strings.TrimSpace(name)
		}
	}

	if toolName == "" {
		err := fmt.Errorf("tool node %s missing config.tool_name", node.ID)
		result.State = ExecutionStateFailed
		result.Error = err.Error()
		result.Duration = time.Since(start).Milliseconds()
		return result, "", err
	}

	tool, err := e.toolRegistry.Get(toolName)
	if err != nil {
		err = fmt.Errorf("tool %s not found: %w", toolName, err)
		result.State = ExecutionStateFailed
		result.Error = err.Error()
		result.Duration = time.Since(start).Milliseconds()
		return result, "", err
	}

	params := e.buildToolParams(node, shared)
	params = normalizeToolParamsByDefinition(params, tool.Info().Parameters)
	logger.Infof("[Executor] tool params prepared nodeId=%s tool=%s params=%v", node.ID, toolName, summarizeMapTypes(params))

	output, err := tool.Execute(ctx, params)
	if err != nil {
		result.State = ExecutionStateFailed
		result.Error = err.Error()
		result.Duration = time.Since(start).Milliseconds()
		return result, "", err
	}

	result.State = ExecutionStateSucceeded
	result.Output = output
	result.Duration = time.Since(start).Milliseconds()

	shared[node.ID] = output
	if q := extractQueryFromOutput(output); q != "" {
		shared["query"] = q
	}

	nextNodeID := e.getNextNode(wf, node.ID, "")

	return result, nextNodeID, nil
}

func (e *InterpretiveExecutor) handleChatModelNode(ctx context.Context, wf *orchestrator.Workflow, node orchestrator.Node, shared map[string]any) (NodeExecutionResult, string, error) {
	start := time.Now()
	logger.Infof("[Executor] handleChatModelNode nodeId=%s", node.ID)

	result := NodeExecutionResult{
		NodeID:   node.ID,
		NodeType: "chat_model",
	}

	if s := strings.TrimSpace(node.PreInput); s != "" {
		shared["query"] = composePreInputQuery(s, shared)
	}

	cfg := config.GetMainConfig()
	baseURL := cfg.LLM.URL
	apiKey := cfg.LLM.APIKey
	model := cfg.LLM.ChatModel

	if node.Config != nil {
		if s, ok := node.Config["url"].(string); ok && strings.TrimSpace(s) != "" {
			baseURL = s
		}
		if s, ok := node.Config["apikey"].(string); ok && strings.TrimSpace(s) != "" {
			apiKey = s
		}
		if s, ok := node.Config["model"].(string); ok && strings.TrimSpace(s) != "" {
			model = s
		}
	}

	query := firstNonEmptyString(
		stringify(shared["query"]),
		stringify(shared["text"]),
		stringify(shared["input"]),
	)
	if query == "" {
		query = "请根据上下文给出简洁回答。"
	}

	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(model) == "" {
		err := fmt.Errorf("chat_model config missing url/model")
		result.State = ExecutionStateFailed
		result.Error = err.Error()
		result.Duration = time.Since(start).Milliseconds()
		return result, "", err
	}

	client := llm.NewClient(baseURL, apiKey)
	resp, err := client.ChatCompletion(ctx, model, []llm.Message{{Role: "user", Content: query}}, intPtr(512), floatPtr(0.3))
	if err != nil {
		result.State = ExecutionStateFailed
		result.Error = err.Error()
		result.Duration = time.Since(start).Milliseconds()
		return result, "", err
	}

	outputType := "string"
	if node.Config != nil {
		if s, ok := node.Config["output_type"].(string); ok && strings.TrimSpace(s) != "" {
			outputType = strings.ToLower(strings.TrimSpace(s))
		}
	}

	output := map[string]any{"model": model}
	if outputType == "bool" {
		normalized := strings.ToLower(strings.TrimSpace(resp))
		parsed := normalized == "true"
		output["response"] = parsed
		output["raw_response"] = resp
	} else {
		output["response"] = resp
	}

	result.State = ExecutionStateSucceeded
	result.Output = output
	result.Duration = time.Since(start).Milliseconds()

	shared[node.ID] = output
	if q := extractQueryFromOutput(output); q != "" {
		shared["query"] = q
	}

	nextNodeID := e.getNextNode(wf, node.ID, "")

	return result, nextNodeID, nil
}

func stringify(v any) string {
	if v == nil {
		return ""
	}
	s := strings.TrimSpace(fmt.Sprintf("%v", v))
	if s == "<nil>" {
		return ""
	}
	return s
}

func firstNonEmptyString(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func intPtr(v int) *int           { return &v }
func floatPtr(v float64) *float64 { return &v }

func (e *InterpretiveExecutor) handleConditionNode(ctx context.Context, wf *orchestrator.Workflow, node orchestrator.Node, shared map[string]any) (NodeExecutionResult, string, error) {
	start := time.Now()
	logger.Infof("[Executor] handleConditionNode nodeId=%s condition=%s", node.ID, node.Condition)

	result := NodeExecutionResult{
		NodeID:   node.ID,
		NodeType: "condition",
	}

	if s := strings.TrimSpace(node.PreInput); s != "" {
		shared["query"] = composePreInputQuery(s, shared)
	}

	matched := e.evaluateConditionNode(node, shared)

	result.State = ExecutionStateSucceeded
	result.Output = map[string]any{"matched": matched}
	result.Duration = time.Since(start).Milliseconds()

	shared[node.ID] = result.Output

	nextNodeID := e.getConditionNextNode(wf, node.ID, matched)

	return result, nextNodeID, nil
}

func (e *InterpretiveExecutor) handleLoopNode(ctx context.Context, wf *orchestrator.Workflow, node orchestrator.Node, shared map[string]any) (NodeExecutionResult, string, error) {
	start := time.Now()
	logger.Infof("[Executor] handleLoopNode nodeId=%s", node.ID)

	result := NodeExecutionResult{
		NodeID:   node.ID,
		NodeType: "loop",
	}

	iterKey := fmt.Sprintf("__loop_iter_%s", node.ID)
	iter := 0
	if v, ok := shared[iterKey].(int); ok {
		iter = v
	}
	iter++
	shared[iterKey] = iter

	maxIter := 10
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

	loopContinue := iter < maxIter

	result.State = ExecutionStateSucceeded
	result.Output = map[string]any{"iteration": iter, "continue": loopContinue}
	result.Duration = time.Since(start).Milliseconds()

	shared[node.ID] = result.Output

	nextNodeID := ""
	if loopContinue {
		nextNodeID = resolveLabeledEdge(wf, node.ID, []string{"body", "loop", "true"})
	} else {
		nextNodeID = resolveLabeledEdge(wf, node.ID, []string{"break", "exit", "false"})
	}
	if nextNodeID == "" {
		if loopContinue && node.LoopConfig != nil {
			nextNodeID = node.LoopConfig.ContinueTo
		} else if node.LoopConfig != nil {
			nextNodeID = node.LoopConfig.ExitTo
		}
	}

	return result, nextNodeID, nil
}

func (e *InterpretiveExecutor) getNextNode(wf *orchestrator.Workflow, nodeID string, label string) string {
	for _, edge := range wf.Edges {
		if edge.From == nodeID {
			if label == "" || edge.Label == "" || edge.Label == label {
				return edge.To
			}
		}
	}
	return ""
}

func (e *InterpretiveExecutor) getConditionNextNode(wf *orchestrator.Workflow, nodeID string, matched bool) string {
	targetLabel := "false"
	if matched {
		targetLabel = "true"
	}

	for _, edge := range wf.Edges {
		if edge.From == nodeID {
			if strings.EqualFold(edge.Label, targetLabel) {
				return edge.To
			}
		}
	}

	for _, edge := range wf.Edges {
		if edge.From == nodeID && edge.Label == "" {
			return edge.To
		}
	}

	return ""
}

func (e *InterpretiveExecutor) buildToolParams(node orchestrator.Node, shared map[string]any) map[string]any {
	params := make(map[string]any)

	if node.Config != nil {
		if inputMapping, ok := node.Config["input_mapping"].(map[string]any); ok {
			for targetKey, sourceKey := range inputMapping {
				if sk, ok := sourceKey.(string); ok {
					if val, exists := resolveSharedValue(shared, sk); exists {
						if _, isMap := val.(map[string]any); isMap {
							if fallback, fallbackOK := shared[targetKey]; fallbackOK {
								if _, fallbackIsMap := fallback.(map[string]any); !fallbackIsMap {
									logger.Infof("[Executor] tool mapping fallback nodeId=%s target=%s source=%s fallbackType=%T", node.ID, targetKey, sk, fallback)
									val = fallback
								}
							}
						}
						logger.Infof("[Executor] tool mapping nodeId=%s target=%s source=%s sourceType=%T", node.ID, targetKey, sk, val)
						params[targetKey] = val
					} else {
						logger.Warnf("[Executor] tool mapping miss nodeId=%s target=%s source=%s", node.ID, targetKey, sk)
					}
				}
			}
		}

		if staticParams, ok := node.Config["params"].(map[string]any); ok {
			for k, v := range staticParams {
				params[k] = v
			}
		}
	}

	return params
}

func resolveSharedValue(shared map[string]any, sourceKey string) (any, bool) {
	trimmed := strings.TrimSpace(sourceKey)
	if trimmed == "" {
		return nil, false
	}

	if v, ok := shared[trimmed]; ok {
		return v, true
	}

	parts := strings.Split(trimmed, ".")
	if len(parts) <= 1 {
		return nil, false
	}

	current, ok := shared[parts[0]]
	if !ok {
		return nil, false
	}

	for i := 1; i < len(parts); i++ {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		next, ok := m[parts[i]]
		if !ok {
			return nil, false
		}
		current = next
	}

	return current, true
}

func normalizeToolParamsByDefinition(params map[string]any, defs []tools.ToolParameter) map[string]any {
	if len(params) == 0 || len(defs) == 0 {
		return params
	}

	for _, def := range defs {
		if def.Type != tools.ParamTypeString {
			continue
		}
		val, exists := params[def.Name]
		if !exists || val == nil {
			continue
		}
		if _, ok := val.(string); ok {
			continue
		}
		if normalized, ok := extractStringCandidate(val, def.Name); ok {
			params[def.Name] = normalized
			logger.Infof("[Executor] normalize tool param key=%s from=%T", def.Name, val)
		}
	}

	return params
}

func extractStringCandidate(v any, preferredKey string) (string, bool) {
	if s, ok := v.(string); ok {
		return s, true
	}
	if b, ok := v.([]byte); ok {
		return string(b), true
	}

	m, ok := v.(map[string]any)
	if !ok {
		return "", false
	}

	candidateKeys := []string{
		strings.TrimSpace(preferredKey),
		"query",
		"text",
		"content",
		"result",
		"response",
		"body",
		"value",
	}

	for _, k := range candidateKeys {
		if k == "" {
			continue
		}
		if val, exists := m[k]; exists {
			if s, ok := val.(string); ok {
				return s, true
			}
		}
	}

	for _, k := range candidateKeys {
		if k == "" {
			continue
		}
		if val, exists := m[k]; exists {
			if nested, ok := val.(map[string]any); ok {
				if s, ok := nested[preferredKey].(string); ok {
					return s, true
				}
				if s, ok := nested["value"].(string); ok {
					return s, true
				}
			}
		}
	}

	return "", false
}

func summarizeMapTypes(m map[string]any) map[string]string {
	if len(m) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = fmt.Sprintf("%T", v)
	}
	return out
}

func (e *InterpretiveExecutor) evaluateCondition(expr string, shared map[string]any) bool {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return true
	}

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

	if strings.Contains(expr, "!=") {
		parts := strings.SplitN(expr, "!=", 2)
		left := strings.TrimSpace(parts[0])
		right := strings.TrimSpace(parts[1])
		val, ok := shared[left]
		if !ok {
			return true
		}
		return fmt.Sprint(val) != right
	}

	val, ok := shared[expr]
	if !ok {
		return false
	}
	if b, ok := val.(bool); ok {
		return b
	}
	return fmt.Sprint(val) != ""
}

func (e *InterpretiveExecutor) evaluateConditionNode(node orchestrator.Node, shared map[string]any) bool {
	if node.Config != nil {
		left := resolveConditionValue(node.Config, shared, "left")
		right := resolveConditionValue(node.Config, shared, "right")
		op, _ := node.Config["operator"].(string)
		op = strings.ToLower(strings.TrimSpace(op))
		if op == "" {
			op = "eq"
		}

		switch op {
		case "eq", "==", "=":
			return fmt.Sprint(left) == fmt.Sprint(right)
		case "gt", ">":
			return compareConditionOperand(left, right) > 0
		case "lt", "<":
			return compareConditionOperand(left, right) < 0
		}
	}
	return e.evaluateCondition(node.Condition, shared)
}

func compareConditionOperand(left any, right any) int {
	lf, lok := toFloatValue(left)
	rf, rok := toFloatValue(right)
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

func toFloatValue(v any) (float64, bool) {
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

func resolveConditionValue(cfg map[string]any, shared map[string]any, side string) any {
	typeKey := side + "_type"
	valueKey := side + "_value"
	t, _ := cfg[typeKey].(string)
	t = strings.ToLower(strings.TrimSpace(t))
	v, ok := cfg[valueKey]
	if !ok {
		return nil
	}
	if t == "path" {
		if path, ok := v.(string); ok {
			if rv, exists := resolveSharedValue(shared, path); exists {
				return rv
			}
		}
		return nil
	}
	if t == "bool" {
		return coerceBool(v)
	}
	return v
}

func coerceBool(v any) bool {
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

func resolveLabeledEdge(wf *orchestrator.Workflow, from string, labels []string) string {
	if wf == nil {
		return ""
	}
	for _, label := range labels {
		for _, edge := range wf.Edges {
			if edge.From == from && strings.EqualFold(strings.TrimSpace(edge.Label), label) {
				return edge.To
			}
		}
	}
	return ""
}

func composePreInputQuery(preInput string, shared map[string]any) string {
	prefix := sanitizePreInput(preInput)
	base := buildSharedBaseQuery(shared)
	if prefix == "" {
		return base
	}
	if base == "" {
		return prefix
	}
	return prefix + "\n" + base
}

func sanitizePreInput(preInput string) string {
	trimmed := strings.TrimSpace(preInput)
	if trimmed == "" {
		return ""
	}
	for {
		start := strings.Index(trimmed, "{{")
		if start < 0 {
			break
		}
		end := strings.Index(trimmed[start+2:], "}}")
		if end < 0 {
			break
		}
		trimmed = trimmed[:start] + trimmed[start+2+end+2:]
	}
	return strings.TrimSpace(trimmed)
}

func buildSharedBaseQuery(shared map[string]any) string {
	userText := stringify(shared["text"])
	prevQuery := stringify(shared["query"])
	inputText := stringify(shared["input"])

	parts := make([]string, 0, 3)
	if userText != "" {
		parts = append(parts, userText)
	}
	if prevQuery != "" && prevQuery != userText {
		parts = append(parts, prevQuery)
	}
	if inputText != "" && inputText != userText && inputText != prevQuery {
		parts = append(parts, inputText)
	}

	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func extractQueryFromOutput(output map[string]any) string {
	if output == nil {
		return ""
	}
	if v, ok := output["query"]; ok {
		if s := stringify(v); s != "" {
			return s
		}
	}
	if v, ok := output["response"]; ok {
		if s := stringify(v); s != "" {
			return s
		}
	}
	if v, ok := output["result"]; ok {
		if s := stringify(v); s != "" {
			return s
		}
	}
	return ""
}

func ConvertStorageToExecutorDef(storageDef *WorkflowDefinition) *WorkflowDefinition {
	nodes := make([]NodeDef, 0, len(storageDef.Nodes))
	for _, n := range storageDef.Nodes {
		nodes = append(nodes, NodeDef{
			ID:         n.ID,
			Type:       n.Type,
			Config:     n.Config,
			AgentID:    n.AgentID,
			TaskType:   n.TaskType,
			Condition:  n.Condition,
			PreInput:   n.PreInput,
			LoopConfig: n.LoopConfig,
			Metadata:   n.Metadata,
		})
	}

	edges := make([]EdgeDef, 0, len(storageDef.Edges))
	for _, e := range storageDef.Edges {
		edges = append(edges, EdgeDef{
			From:    e.From,
			To:      e.To,
			Label:   e.Label,
			Mapping: e.Mapping,
		})
	}

	return &WorkflowDefinition{
		WorkflowID:  storageDef.WorkflowID,
		Name:        storageDef.Name,
		Description: storageDef.Description,
		StartNodeID: storageDef.StartNodeID,
		Nodes:       nodes,
		Edges:       edges,
	}
}

func ConvertToolParams(params []tools.ToolParameter) []tools.ToolParameter {
	result := make([]tools.ToolParameter, 0, len(params))
	for _, p := range params {
		result = append(result, tools.ToolParameter{
			Name:        p.Name,
			Type:        p.Type,
			Required:    p.Required,
			Description: p.Description,
			Default:     p.Default,
			Enum:        p.Enum,
		})
	}
	return result
}
