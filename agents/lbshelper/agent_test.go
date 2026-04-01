package lbshelper

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai/pkg/llm"
	"ai/pkg/orchestrator"
	"ai/pkg/protocol"
	"ai/pkg/taskmanager"
	"ai/pkg/tools"
	"ai/pkg/transport/httpagent"
)

func TestBuildLBSHelperWorkflow(t *testing.T) {
	wf, err := buildLBSHelperWorkflow()
	if err != nil {
		t.Fatalf("build workflow failed: %v", err)
	}
	if wf.StartNodeID != "N_start" {
		t.Fatalf("unexpected start node: %s", wf.StartNodeID)
	}
	if len(wf.Nodes) != 5 {
		t.Fatalf("unexpected node count: %d", len(wf.Nodes))
	}

	if wf.Nodes["N_extract"].Type != orchestrator.NodeTypeChatModel {
		t.Fatalf("N_extract type mismatch")
	}
	if wf.Nodes["N_amap"].Type != orchestrator.NodeTypeTool {
		t.Fatalf("N_amap type mismatch")
	}

	cfg := wf.Nodes["N_amap"].Config
	if cfg["tool_name"] != "amap" {
		t.Fatalf("N_amap tool_name mismatch: %v", cfg["tool_name"])
	}
}

func TestExtractToolCall(t *testing.T) {
	jsonInput := "```json\n{\"query\":\"北京到上海\",\"tool_name\":\"maps_direction_driving\",\"arguments\":{\"origin\":\"北京\"}}\n```"
	out := extractToolCall(jsonInput)

	if out["query"] != "北京到上海" {
		t.Fatalf("unexpected query: %v", out["query"])
	}
	if out["tool_name"] != "maps_direction_driving" {
		t.Fatalf("unexpected tool_name: %v", out["tool_name"])
	}
	args, ok := out["arguments"].(map[string]any)
	if !ok || args["origin"] != "北京" {
		t.Fatalf("unexpected arguments: %v", out["arguments"])
	}

	fallback := extractToolCall("没有JSON")
	if fallback["query"] != "没有JSON" {
		t.Fatalf("fallback query mismatch: %v", fallback["query"])
	}
	norm := normalizeAmapCallParams(map[string]any{"query": "北京到上海"}, "北京到上海")
	if norm["tool_name"] != "maps_direction_driving" {
		t.Fatalf("unexpected inferred tool: %v", norm["tool_name"])
	}
}

func TestLBSHelperProcessInternal_EndToEnd(t *testing.T) {
	llmServer := newMockLLMServer(t, func(prompt string) string {
		if strings.Contains(prompt, "地图路径规划助手") {
			return `{"query":"北京到上海","tool_name":"maps_direction_driving","arguments":{"origin":"北京","destination":"上海"}}`
		}
		if strings.Contains(prompt, "旅行行程规划助手") {
			return "路线整理完成"
		}
		return ""
	})

	toolCalled := false
	a := &Agent{
		llmClient: llm.NewClient(llmServer.URL, ""),
		chatModel: "mock",
		AmapTool: tools.NewRawHTTPTool("amap", "", nil, func(ctx context.Context, params map[string]any) (map[string]any, error) {
			toolCalled = true
			if params["tool_name"] != "maps_direction_driving" {
				t.Fatalf("unexpected tool_name: %v", params["tool_name"])
			}
			return map[string]any{"content": "AMap route result"}, nil
		}),
		amapToolCatalog: "可用工具: maps_direction_driving,maps_text_search",
	}

	eng := orchestrator.NewEngine(orchestrator.Config{DefaultTaskTimeoutSec: 30, RetryMaxAttempts: 1}, orchestrator.NewInMemoryAgentRegistry())
	a.orchestratorEngine = eng
	if err := a.orchestratorEngine.RegisterWorker(orchestrator.AgentDescriptor{ID: LBSHelperWorkflowWorkerID, Name: "test", Capabilities: []orchestrator.AgentCapability{"chat_model", "tool"}}, &workflowNodeWorker{agent: a}); err != nil {
		t.Fatalf("register worker failed: %v", err)
	}
	wf, err := buildLBSHelperWorkflow()
	if err != nil {
		t.Fatalf("build workflow failed: %v", err)
	}
	if err = a.orchestratorEngine.RegisterWorkflow(wf); err != nil {
		t.Fatalf("register workflow failed: %v", err)
	}

	ctx := context.Background()
	mgr := taskmanager.NewInMemoryManager()
	taskID, err := mgr.BuildTask(nil, nil)
	if err != nil {
		t.Fatalf("build task failed: %v", err)
	}
	_ = mgr.UpdateTaskState(ctx, taskID, protocol.TaskStateWorking, nil)

	msg := protocol.Message{Role: protocol.MessageRoleUser, Parts: []protocol.Part{protocol.NewTextPart("帮我规划北京到上海路线")}}
	if err = a.ProcessInternal(ctx, taskID, msg, mgr); err != nil {
		t.Fatalf("ProcessInternal failed: %v", err)
	}
	if !toolCalled {
		t.Fatalf("amap tool was not called")
	}

	task, err := mgr.GetTask(ctx, taskID)
	if err != nil {
		t.Fatalf("get task failed: %v", err)
	}
	if task.Status.State != protocol.TaskStateCompleted {
		t.Fatalf("unexpected task state: %s", task.Status.State)
	}
	if task.Status.Message == nil || task.Status.Message.FirstText() != "路线整理完成" {
		t.Fatalf("unexpected task output: %#v", task.Status.Message)
	}
}

func TestLBSHelperHTTPServer_SendMessage_EndToEnd(t *testing.T) {
	llmServer := newMockLLMServer(t, func(prompt string) string {
		if strings.Contains(prompt, "地图路径规划助手") {
			return `{"query":"北京到上海","tool_name":"maps_direction_driving","arguments":{"origin":"北京","destination":"上海"}}`
		}
		if strings.Contains(prompt, "旅行行程规划助手") {
			return "路线整理完成"
		}
		return ""
	})

	a := &Agent{
		llmClient: llm.NewClient(llmServer.URL, ""),
		chatModel: "mock",
		AmapTool: tools.NewRawHTTPTool("amap", "", nil, func(ctx context.Context, params map[string]any) (map[string]any, error) {
			return map[string]any{"content": "AMap route result"}, nil
		}),
		amapToolCatalog: "可用工具: maps_direction_driving,maps_text_search",
	}

	eng := orchestrator.NewEngine(orchestrator.Config{DefaultTaskTimeoutSec: 30, RetryMaxAttempts: 1}, orchestrator.NewInMemoryAgentRegistry())
	a.orchestratorEngine = eng
	if err := a.orchestratorEngine.RegisterWorker(orchestrator.AgentDescriptor{ID: LBSHelperWorkflowWorkerID, Name: "test", Capabilities: []orchestrator.AgentCapability{"chat_model", "tool"}}, &workflowNodeWorker{agent: a}); err != nil {
		t.Fatalf("register worker failed: %v", err)
	}
	wf, err := buildLBSHelperWorkflow()
	if err != nil {
		t.Fatalf("build workflow failed: %v", err)
	}
	if err = a.orchestratorEngine.RegisterWorkflow(wf); err != nil {
		t.Fatalf("register workflow failed: %v", err)
	}

	h, err := NewHTTPServer(a)
	if err != nil {
		t.Fatalf("NewHTTPServer failed: %v", err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := httpagent.NewClient(srv.URL, 5*time.Second)
	ctx := context.Background()
	taskID, err := client.SendMessage(ctx, protocol.Message{Role: protocol.MessageRoleUser, Parts: []protocol.Part{protocol.NewTextPart("帮我规划北京到上海路线")}})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		task, getErr := client.GetTask(ctx, taskID)
		if getErr == nil && task != nil && task.Status.State.IsTerminal() {
			if task.Status.State != protocol.TaskStateCompleted {
				t.Fatalf("unexpected task state: %s", task.Status.State)
			}
			if task.Status.Message == nil || task.Status.Message.FirstText() != "路线整理完成" {
				t.Fatalf("unexpected task output: %#v", task.Status.Message)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("task did not finish in time")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func newMockLLMServer(t *testing.T, pick func(prompt string) string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		prompt := ""
		if len(req.Messages) > 0 {
			prompt = req.Messages[len(req.Messages)-1].Content
		}
		resp := map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"content": pick(prompt)},
			}},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(server.Close)
	return server
}
