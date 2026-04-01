package orchestrator

import (
	"strings"

	"ai/pkg/storage"
)

func isToolNodeType(nodeType string) bool {
	return strings.EqualFold(strings.TrimSpace(nodeType), "tool")
}

func normalizeAPINodeDefinitions(nodes []NodeDefinition) []NodeDefinition {
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

func normalizeStorageNodeDefinitions(nodes []storage.NodeDef) []storage.NodeDef {
	if len(nodes) == 0 {
		return nodes
	}
	out := make([]storage.NodeDef, 0, len(nodes))
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
