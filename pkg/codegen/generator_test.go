package codegen

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"ai/pkg/executor"
	"ai/pkg/tools"
)

func TestGenerateWorkflowBuilder_UsesStringMappingForEdges(t *testing.T) {
	g := NewCodeGenerator(GeneratorConfig{})
	req := &AgentGenerateRequest{
		AgentID: "demo_agent",
		Name:    "Demo",
		WorkflowDef: &executor.WorkflowDefinition{
			WorkflowID:  "wf_1",
			Name:        "wf",
			StartNodeID: "start",
			Nodes: []executor.NodeDef{
				{ID: "start", Type: "start"},
				{ID: "end", Type: "end"},
			},
			Edges: []executor.EdgeDef{
				{
					From:  "start",
					To:    "end",
					Label: "next",
					Mapping: map[string]any{
						"a": 1,
						"b": true,
					},
				},
			},
		},
	}

	code := g.generateWorkflowBuilder(req)

	if strings.Contains(code, "map[string]any{") {
		t.Fatalf("generated workflow builder should not pass map[string]any to AddEdgeWithLabel: %s", code)
	}
	if !strings.Contains(code, "map[string]string{") {
		t.Fatalf("generated workflow builder should pass map[string]string to AddEdgeWithLabel: %s", code)
	}
}

func TestToolParametersExpr_UsesQualifiedParameterType(t *testing.T) {
	expr := toolParametersExpr([]tools.ToolParameter{{
		Name:        "q",
		Type:        tools.ParamTypeString,
		Required:    true,
		Description: "query",
	}})

	if !strings.Contains(expr, "tools.ParamTypeString") {
		t.Fatalf("toolParametersExpr should emit qualified parameter type constants, got: %s", expr)
	}
}

func TestGenerateAgentCode_WithHyphenIDs_IsValidGoSyntax(t *testing.T) {
	g := NewCodeGenerator(GeneratorConfig{})
	req := &AgentGenerateRequest{
		AgentID: "my-agent",
		Name:    "My Agent",
		WorkflowDef: &executor.WorkflowDefinition{
			WorkflowID:  "wf_1",
			Name:        "wf",
			StartNodeID: "start",
			Nodes: []executor.NodeDef{
				{ID: "start", Type: "start"},
				{ID: "tool_1", Type: "tool", Config: map[string]any{"tool_name": "my-tool"}},
				{ID: "end", Type: "end"},
			},
			Edges: []executor.EdgeDef{
				{From: "start", To: "tool_1"},
				{From: "tool_1", To: "end"},
			},
		},
		Tools: []ToolDefinition{{
			ToolID:      "my-tool",
			Name:        "my-tool",
			Description: "demo",
			ToolType:    tools.ToolTypeHTTP,
			Config: map[string]any{
				"method": "GET",
				"url":    "https://example.com",
			},
		}},
	}

	code := g.generateAgentCode(req)

	if strings.Contains(code, "my-agentWorkflowID") || strings.Contains(code, "my-toolTool") {
		t.Fatalf("generated code still contains invalid identifier fragments: %s", code)
	}

	fs := token.NewFileSet()
	if _, err := parser.ParseFile(fs, "agent.go", code, parser.AllErrors); err != nil {
		t.Fatalf("generated agent code should be valid Go syntax, got parse error: %v", err)
	}
}

func TestGenerateWorkflowBuilder_PreservesInputSourceConfig(t *testing.T) {
	g := NewCodeGenerator(GeneratorConfig{})
	req := &AgentGenerateRequest{
		AgentID: "demo_agent",
		Name:    "Demo",
		WorkflowDef: &executor.WorkflowDefinition{
			WorkflowID:  "wf_cfg",
			Name:        "wf_cfg",
			StartNodeID: "start",
			Nodes: []executor.NodeDef{
				{ID: "start", Type: "start"},
				{ID: "judge", Type: "chat_model", Config: map[string]any{"input_source": "history", "output_type": "bool"}},
				{ID: "end", Type: "end"},
			},
			Edges: []executor.EdgeDef{
				{From: "start", To: "judge"},
				{From: "judge", To: "end"},
			},
		},
	}

	code := g.generateWorkflowBuilder(req)
	if !strings.Contains(code, "\"input_source\": \"history\"") {
		t.Fatalf("generated workflow should preserve input_source config, got: %s", code)
	}
}

func TestGenerateWorkflowBuilder_DerivesConditionRoutesFromEdgeLabels(t *testing.T) {
	g := NewCodeGenerator(GeneratorConfig{})
	req := &AgentGenerateRequest{
		AgentID: "demo_agent",
		Name:    "Demo",
		WorkflowDef: &executor.WorkflowDefinition{
			WorkflowID:  "wf_condition",
			Name:        "wf_condition",
			StartNodeID: "start",
			Nodes: []executor.NodeDef{
				{ID: "start", Type: "start"},
				{ID: "judge", Type: "condition", Config: map[string]any{"operator": "eq", "right_type": "bool", "right_value": "true"}},
				{ID: "extract", Type: "chat_model"},
				{ID: "final", Type: "chat_model"},
				{ID: "end", Type: "end"},
			},
			Edges: []executor.EdgeDef{
				{From: "start", To: "judge"},
				// Intentionally put false edge before true edge to verify routing does not depend on insertion order.
				{From: "judge", To: "extract", Label: "false"},
				{From: "judge", To: "final", Label: "true"},
				{From: "extract", To: "end"},
				{From: "final", To: "end"},
			},
		},
	}

	code := g.generateWorkflowBuilder(req)
	if !strings.Contains(code, "Metadata: map[string]string{\"false_to\": \"extract\", \"true_to\": \"final\"}") {
		t.Fatalf("generated workflow should include explicit condition routes, got: %s", code)
	}
}
