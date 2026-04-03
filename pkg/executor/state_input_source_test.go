package executor

import (
	"strings"
	"testing"

	"ai/pkg/orchestrator"
)

func TestUpdateSharedOutputStateAndInputSelection(t *testing.T) {
	shared := map[string]any{}

	updateSharedOutputState(shared, "n1", map[string]any{"response": "alpha"})
	updateSharedOutputState(shared, "n2", map[string]any{"response": "beta"})

	latest, ok := shared["latest_output"].(map[string]any)
	if !ok {
		t.Fatalf("latest_output missing or invalid type: %T", shared["latest_output"])
	}
	if got := latest["response"]; got != "beta" {
		t.Fatalf("latest_output.response = %v, want beta", got)
	}

	history, ok := shared["history_outputs"].([]any)
	if !ok || len(history) != 2 {
		t.Fatalf("history_outputs invalid: type=%T len=%d", shared["history_outputs"], len(history))
	}

	prevNode := orchestrator.Node{ID: "n3", Config: map[string]any{"input_source": "previous"}}
	if got := selectNodeInputText(prevNode, shared); !strings.Contains(got, "response:beta") {
		t.Fatalf("previous input = %q, want map content including response:beta", got)
	}

	historyNode := orchestrator.Node{ID: "n4", Config: map[string]any{"input_source": "history"}}
	gotHistory := selectNodeInputText(historyNode, shared)
	if gotHistory == "" {
		t.Fatalf("history input should not be empty")
	}
	if !strings.Contains(gotHistory, "response:alpha") || !strings.Contains(gotHistory, "response:beta") {
		t.Fatalf("history input should include full outputs, got: %q", gotHistory)
	}
}

func TestResolveConditionValueSupportsOnlyStringAndBool(t *testing.T) {
	shared := map[string]any{}

	if v := resolveConditionValue(map[string]any{"right_type": "bool", "right_value": "true"}, shared, "right"); v != true {
		t.Fatalf("bool conversion failed: %v", v)
	}

	v := resolveConditionValue(map[string]any{"right_type": "string", "right_value": 123}, shared, "right")
	if v != "123" {
		t.Fatalf("string conversion failed: %v", v)
	}
}

func TestResolveConditionLeftValueUsesLatestOutput(t *testing.T) {
	shared := map[string]any{
		"latest_output": map[string]any{"response": true},
	}

	v := resolveConditionValue(map[string]any{"left_type": "string", "left_value": "anything"}, shared, "left")
	if vb, ok := v.(bool); !ok || !vb {
		t.Fatalf("left operand should come from latest_output.response, got %v (%T)", v, v)
	}
}

func TestSeedInputQueryHistory(t *testing.T) {
	shared := map[string]any{"query": "帮我搜索重庆邮电大学"}

	seedInputQueryHistory(shared)
	seedInputQueryHistory(shared)

	history, ok := shared["history_outputs"].([]any)
	if !ok || len(history) != 1 {
		t.Fatalf("history_outputs invalid: type=%T len=%d", shared["history_outputs"], len(history))
	}
	entry, ok := history[0].(map[string]any)
	if !ok {
		t.Fatalf("history entry type invalid: %T", history[0])
	}
	if got, _ := entry["node_id"].(string); got != "__input__" {
		t.Fatalf("node_id = %q, want __input__", got)
	}
	out, ok := entry["output"].(map[string]any)
	if !ok {
		t.Fatalf("history output type invalid: %T", entry["output"])
	}
	if got := out["query"]; got != "帮我搜索重庆邮电大学" {
		t.Fatalf("output.query = %v, want initial query", got)
	}
}

func TestUpdateSharedOutputStateKeepsNonExtractableOutputInHistory(t *testing.T) {
	shared := map[string]any{}

	updateSharedOutputState(shared, "tavily", map[string]any{"status_code": 200, "body": "{\"results\":[]}"})

	history, ok := shared["history_outputs"].([]any)
	if !ok || len(history) != 1 {
		t.Fatalf("history_outputs invalid: type=%T len=%d", shared["history_outputs"], len(history))
	}
	entry, ok := history[0].(map[string]any)
	if !ok {
		t.Fatalf("history entry type invalid: %T", history[0])
	}
	out, ok := entry["output"].(map[string]any)
	if !ok {
		t.Fatalf("history output type invalid: %T", entry["output"])
	}
	if got := out["status_code"]; got != 200 {
		t.Fatalf("status_code = %v, want 200", got)
	}
}
