package codegen

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
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

func TestGenerateAgentCode_ToolInputMappingResolvesPayloadState(t *testing.T) {
	g := NewCodeGenerator(GeneratorConfig{})
	req := &AgentGenerateRequest{
		AgentID: "demo_agent",
		Name:    "Demo",
		WorkflowDef: &executor.WorkflowDefinition{
			WorkflowID:  "wf_mapping",
			Name:        "wf_mapping",
			StartNodeID: "start",
			Nodes: []executor.NodeDef{
				{ID: "start", Type: "start"},
				{ID: "N_chat", Type: "chat_model"},
				{ID: "N_tool", Type: "tool", Config: map[string]any{
					"tool_name": "tavily",
					"input_mapping": map[string]any{
						"query": "N_chat",
					},
				}},
				{ID: "end", Type: "end"},
			},
			Edges: []executor.EdgeDef{
				{From: "start", To: "N_chat"},
				{From: "N_chat", To: "N_tool"},
				{From: "N_tool", To: "end"},
			},
		},
		Tools: []ToolDefinition{{
			ToolID:      "tavily",
			Name:        "tavily",
			Description: "demo",
			ToolType:    tools.ToolTypeHTTP,
			Config: map[string]any{
				"method": "POST",
				"url":    "https://example.com",
			},
			Parameters: []tools.ToolParameter{
				{Name: "query", Type: tools.ParamTypeString, Required: true},
			},
		}},
	}

	code := g.generateAgentCode(req)

	if !strings.Contains(code, "callTool(ctx, taskID, query, req.NodeConfig, req.Payload)") {
		t.Fatalf("generated worker should pass payload into callTool, got: %s", code)
	}
	if !strings.Contains(code, "resolveGeneratedPayloadValue(payload, src)") {
		t.Fatalf("generated callTool should resolve payload-backed input mappings, got: %s", code)
	}
	if !strings.Contains(code, "if normalized, ok := extractGeneratedStringCandidate(val, targetKey); ok {") {
		t.Fatalf("generated callTool should extract response text from mapped node outputs before fallback, got: %s", code)
	}
	if !strings.Contains(code, "normalizeGeneratedToolParams(params, tool.Info().Parameters)") {
		t.Fatalf("generated callTool should normalize mapped tool params, got: %s", code)
	}
}

func TestToolParametersExpr_PreservesDefaultAndEnum(t *testing.T) {
	expr := toolParametersExpr([]tools.ToolParameter{{
		Name:        "search_depth",
		Type:        tools.ParamTypeString,
		Required:    false,
		Description: "depth",
		Default:     "basic",
		Enum:        []any{"basic", "advanced"},
	}})

	if !strings.Contains(expr, `Default: "basic"`) {
		t.Fatalf("toolParametersExpr should emit default values, got: %s", expr)
	}
	if !strings.Contains(expr, `Enum: []any{"basic", "advanced"}`) {
		t.Fatalf("toolParametersExpr should emit enum values, got: %s", expr)
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

func TestGenerateAgent_DoesNotRewriteExistingToolFile(t *testing.T) {
	tmp := t.TempDir()
	g := NewCodeGenerator(GeneratorConfig{OutputDir: tmp})

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
			Edges: []executor.EdgeDef{{From: "start", To: "end"}},
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

	if _, err := g.GenerateAgent(req); err != nil {
		t.Fatalf("first generate should succeed: %v", err)
	}

	toolFile := g.ToolFilePath("my-tool")
	const sentinel = "// sentinel: do-not-overwrite\n"
	if err := os.WriteFile(toolFile, []byte(sentinel), 0644); err != nil {
		t.Fatalf("write sentinel file failed: %v", err)
	}

	if _, err := g.GenerateAgent(req); err != nil {
		t.Fatalf("second generate should succeed: %v", err)
	}

	b, err := os.ReadFile(toolFile)
	if err != nil {
		t.Fatalf("read tool file failed: %v", err)
	}
	if string(b) != sentinel {
		t.Fatalf("existing tool file should not be rewritten, got: %s", string(b))
	}
}

func TestGenerateAgent_RespectsSkipFileGeneration(t *testing.T) {
	tmp := t.TempDir()
	g := NewCodeGenerator(GeneratorConfig{OutputDir: tmp})

	req := &AgentGenerateRequest{
		AgentID: "demo_agent_skip",
		Name:    "Demo",
		WorkflowDef: &executor.WorkflowDefinition{
			WorkflowID:  "wf_1",
			Name:        "wf",
			StartNodeID: "start",
			Nodes: []executor.NodeDef{
				{ID: "start", Type: "start"},
				{ID: "end", Type: "end"},
			},
			Edges: []executor.EdgeDef{{From: "start", To: "end"}},
		},
		Tools: []ToolDefinition{{
			ToolID:             "system-tool",
			Name:               "system-tool",
			Description:        "demo",
			ToolType:           tools.ToolTypeHTTP,
			SkipFileGeneration: true,
			Config: map[string]any{
				"method": "GET",
				"url":    "https://example.com",
			},
		}},
	}

	if _, err := g.GenerateAgent(req); err != nil {
		t.Fatalf("generate should succeed: %v", err)
	}

	toolFile := filepath.Join(tmp, "pkg", "tools", "user_generated_system_tool.go")
	if _, err := os.Stat(toolFile); err == nil {
		t.Fatalf("tool file should not be generated when SkipFileGeneration=true")
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected stat error: %v", err)
	}
}
