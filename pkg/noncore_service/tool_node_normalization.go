package noncore_service

import (
	"strings"
)

func isToolNodeType(nodeType string) bool {
	return strings.EqualFold(strings.TrimSpace(nodeType), "tool")
}

func NormalizeAPINodeDefinitions(nodes []NodeDefinition) []NodeDefinition {
	if len(nodes) == 0 {
		return nodes
	}
	out := make([]NodeDefinition, 0, len(nodes))
	for _, n := range nodes {
		nn := n
		if isToolNodeType(nn.Type) {
			nn.AgentID = ""
			nn.TaskType = ""
		}
		out = append(out, nn)
	}
	return out
}

func normalizeLoopConfigMap(in map[string]interface{}) map[string]interface{} {
	if in == nil {
		return nil
	}
	out := make(map[string]interface{}, len(in))
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
