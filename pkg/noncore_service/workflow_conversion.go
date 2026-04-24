package noncore_service

import (
	"strings"

	"ai/pkg/executor"
	"ai/pkg/storage"
)

func NormalizeStorageNodeDefinitions(nodes []storage.NodeDef) []storage.NodeDef {
	if len(nodes) == 0 {
		return nodes
	}
	out := make([]storage.NodeDef, 0, len(nodes))
	for _, n := range nodes {
		nn := n
		nn.LoopConfig = normalizeLoopConfigMap(nn.LoopConfig)
		if isToolNodeType(nn.Type) {
			nn.AgentID = ""
			nn.TaskType = ""
		}
		out = append(out, nn)
	}
	return out
}

func ConvertStorageToExecutorDef(storageDef *storage.WorkflowDefinition) *executor.WorkflowDefinition {
	nodes := make([]executor.NodeDef, 0, len(storageDef.Nodes))
	for _, n := range storageDef.Nodes {
		agentID := n.AgentID
		taskType := n.TaskType
		if strings.EqualFold(strings.TrimSpace(n.Type), "tool") {
			agentID = ""
			taskType = ""
		}

		var loopConfig map[string]any
		if n.LoopConfig != nil {
			loopConfig = NormalizeExecutorLoopConfig(n.LoopConfig)
		}

		nodes = append(nodes, executor.NodeDef{
			ID:         n.ID,
			Type:       n.Type,
			Config:     n.Config,
			AgentID:    agentID,
			TaskType:   taskType,
			Condition:  n.Condition,
			PreInput:   n.PreInput,
			LoopConfig: loopConfig,
			Metadata:   n.Metadata,
		})
	}

	edges := make([]executor.EdgeDef, 0, len(storageDef.Edges))
	for _, e := range storageDef.Edges {
		var mapping map[string]any
		if e.Mapping != nil {
			mapping = make(map[string]any, len(e.Mapping))
			for k, v := range e.Mapping {
				mapping[k] = v
			}
		}

		edges = append(edges, executor.EdgeDef{
			From:    e.From,
			To:      e.To,
			Label:   e.Label,
			Mapping: mapping,
		})
	}

	return &executor.WorkflowDefinition{
		WorkflowID:  storageDef.WorkflowID,
		Name:        storageDef.Name,
		Description: storageDef.Description,
		StartNodeID: storageDef.StartNodeID,
		Nodes:       nodes,
		Edges:       edges,
	}
}

func NormalizeExecutorLoopConfig(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	if v, ok := out["maxIterations"]; ok {
		out["max_iterations"] = v
	}
	if v, ok := out["continueTo"]; ok {
		out["continue_to"] = v
	}
	if v, ok := out["exitTo"]; ok {
		out["exit_to"] = v
	}
	return out
}
