package bazihelper

import (
	"strings"
	"testing"
)

func TestExtractBaziToolPlanFallsBack(t *testing.T) {
	plan := extractBaziToolPlan("", "今天黄历怎么样")
	if len(plan.Calls) == 0 {
		t.Fatalf("expected fallback call")
	}
	if got := plan.Calls[0].ToolName; got != "getChineseCalendar" {
		t.Fatalf("unexpected fallback tool: %s", got)
	}
}

func TestIsEmptyBaziArgNilOrMissing(t *testing.T) {
	if !isEmptyBaziArg(nil) {
		t.Fatal("nil should be empty")
	}
	m := map[string]any{}
	if !isEmptyBaziArg(m["solarDatetime"]) {
		t.Fatal("missing key should be empty")
	}
}

func TestNormalizeBaziArgumentsGetChineseCalendarFillsDefault(t *testing.T) {
	out := normalizeBaziArguments("getChineseCalendar", map[string]any{}, "帮我查一下今天的黄历")
	if isEmptyBaziArg(out["solarDatetime"]) {
		t.Fatalf("expected default solarDatetime, got %#v", out)
	}
	s, ok := out["solarDatetime"].(string)
	if !ok || strings.TrimSpace(s) == "" {
		t.Fatalf("solarDatetime should be non-empty string, got %#v", out["solarDatetime"])
	}
}

func TestExtractBaziToolPlanLLMEmptyArgumentsGetsSolarDatetime(t *testing.T) {
	raw := `{"calls":[{"tool_name":"getChineseCalendar","arguments":{},"reason":"test"}]}`
	plan := extractBaziToolPlan(raw, "今天黄历")
	if len(plan.Calls) != 1 {
		t.Fatalf("calls: %d", len(plan.Calls))
	}
	if isEmptyBaziArg(plan.Calls[0].Arguments["solarDatetime"]) {
		t.Fatalf("expected solarDatetime after normalize, args=%#v", plan.Calls[0].Arguments)
	}
}
