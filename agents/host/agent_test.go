package host

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

func TestBuildHostWorkflow(t *testing.T) {
	wf, err := buildHostWorkflow()
	if err != nil {
		t.Fatalf("build workflow failed: %v", err)
	}

	if wf.StartNodeID != "N_start" {
		t.Fatalf("unexpected start node: %s", wf.StartNodeID)
	}
	if len(wf.Nodes) != 7 {
		t.Fatalf("unexpected node count: %d", len(wf.Nodes))
	}

	if wf.Nodes["N_condition"].Type != orchestrator.NodeTypeCondition {
		t.Fatalf("N_condition type mismatch")
	}
	meta := wf.Nodes["N_condition"].Metadata
	if meta["true_to"] != "N_direct_answer" || meta["false_to"] != "N_call_agent" {
		t.Fatalf("condition metadata mismatch: %+v", meta)
	}
	if wf.Nodes["N_call_agent"].Config["tool_name"] != "call_agent" {
		t.Fatalf("N_call_agent tool_name mismatch: %v", wf.Nodes["N_call_agent"].Config["tool_name"])
	}
}

func TestNormalizeRouteDecision(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{in: "false", want: "false"},
		{in: "无需调用", want: "false"},
		{in: "urlreader", want: "urlreader"},
		{in: "建议调用 deepresearch 处理", want: "建议调用 deepresearch 处理"},
	}
	for _, tc := range cases {
		got := normalizeRouteDecision(tc.in)
		if got != tc.want {
			t.Fatalf("normalizeRouteDecision(%q)=%q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestResolveAllowedAgentName(t *testing.T) {
	allowed := []string{"deep_researcher", "urlreader", "lbshelper"}
	if got := resolveAllowedAgentName("建议调用 deep_researcher 完成", allowed); got != "deep_researcher" {
		t.Fatalf("unexpected resolve result: %q", got)
	}
	if got := resolveAllowedAgentName("false", allowed); got != "" {
		t.Fatalf("false should not resolve to agent: %q", got)
	}
}

func TestCollectAllowedAgents(t *testing.T) {
	in := map[string]any{
		"agents": []any{
			map[string]any{"name": "host"},
			map[string]any{"name": "urlreader"},
			map[string]any{"name": "host"},
		},
	}
	out := collectAllowedAgents(in)
	if len(out) != 2 {
		t.Fatalf("unexpected allowed agents len: %d (%v)", len(out), out)
	}
	if out[0] != "host" || out[1] != "urlreader" {
		t.Fatalf("unexpected allowed agents order/value: %v", out)
	}
}

func TestHostProcessInternal_DirectAnswer(t *testing.T) {
	llmServer := newMockLLMServer(t, func(prompt string) string {
		if strings.Contains(prompt, "Host 路由助手") {
			return "false"
		}
		if strings.Contains(prompt, "通用中文助手") {
			return "直接回答结果"
		}
		return "false"
	})

	a := &Agent{
		llmClient: llm.NewClient(llmServer.URL, ""),
		chatModel: "mock",
		AgentInfoTool: tools.NewRawHTTPTool("agent_info", "", nil, func(ctx context.Context, params map[string]any) (map[string]any, error) {
			return map[string]any{"agents": []any{map[string]any{"name": "deepresearch"}}}, nil
		}),
		CallAgentTool: tools.NewRawHTTPTool("call_agent", "", nil, func(ctx context.Context, params map[string]any) (map[string]any, error) {
			t.Fatalf("call_agent should not be called in direct path")
			return nil, nil
		}),
	}

	eng := orchestrator.NewEngine(orchestrator.Config{DefaultTaskTimeoutSec: 30, RetryMaxAttempts: 1}, orchestrator.NewInMemoryAgentRegistry())
	a.orchestratorEngine = eng
	if err := a.orchestratorEngine.RegisterWorker(orchestrator.AgentDescriptor{ID: HostWorkflowWorkerID, Name: "test", Capabilities: []orchestrator.AgentCapability{"chat_model", "tool"}}, &workflowNodeWorker{agent: a}); err != nil {
		t.Fatalf("register worker failed: %v", err)
	}
	wf, err := buildHostWorkflow()
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

	msg := protocol.Message{Role: protocol.MessageRoleUser, Parts: []protocol.Part{protocol.NewTextPart("你好，今天天气怎么样")}}
	if err = a.ProcessInternal(ctx, taskID, msg, mgr); err != nil {
		t.Fatalf("ProcessInternal failed: %v", err)
	}

	task, err := mgr.GetTask(ctx, taskID)
	if err != nil {
		t.Fatalf("get task failed: %v", err)
	}
	if task.Status.State != protocol.TaskStateCompleted {
		t.Fatalf("unexpected task state: %s", task.Status.State)
	}
	if task.Status.Message == nil || task.Status.Message.FirstText() != "直接回答结果" {
		t.Fatalf("unexpected task output: %#v", task.Status.Message)
	}
}

func TestHostProcessInternal_CallAgent(t *testing.T) {
	llmServer := newMockLLMServer(t, func(prompt string) string {
		if strings.Contains(prompt, "Host 路由助手") {
			return "建议调用 deepresearch 处理"
		}
		return "false"
	})

	called := false
	a := &Agent{
		llmClient: llm.NewClient(llmServer.URL, ""),
		chatModel: "mock",
		AgentInfoTool: tools.NewRawHTTPTool("agent_info", "", nil, func(ctx context.Context, params map[string]any) (map[string]any, error) {
			return map[string]any{"agents": []any{map[string]any{"name": "deepresearch"}, map[string]any{"name": "urlreader"}}}, nil
		}),
		CallAgentTool: tools.NewRawHTTPTool("call_agent", "", nil, func(ctx context.Context, params map[string]any) (map[string]any, error) {
			called = true
			if params["agent_name"] != "deepresearch" {
				t.Fatalf("unexpected agent_name: %v", params["agent_name"])
			}
			return map[string]any{"response": "来自 deepresearch 的结果"}, nil
		}),
	}

	eng := orchestrator.NewEngine(orchestrator.Config{DefaultTaskTimeoutSec: 30, RetryMaxAttempts: 1}, orchestrator.NewInMemoryAgentRegistry())
	a.orchestratorEngine = eng
	if err := a.orchestratorEngine.RegisterWorker(orchestrator.AgentDescriptor{ID: HostWorkflowWorkerID, Name: "test", Capabilities: []orchestrator.AgentCapability{"chat_model", "tool"}}, &workflowNodeWorker{agent: a}); err != nil {
		t.Fatalf("register worker failed: %v", err)
	}
	wf, err := buildHostWorkflow()
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

	msg := protocol.Message{Role: protocol.MessageRoleUser, Parts: []protocol.Part{protocol.NewTextPart("帮我做深度调研")}}
	if err = a.ProcessInternal(ctx, taskID, msg, mgr); err != nil {
		t.Fatalf("ProcessInternal failed: %v", err)
	}
	if !called {
		t.Fatalf("call_agent was not invoked")
	}

	task, err := mgr.GetTask(ctx, taskID)
	if err != nil {
		t.Fatalf("get task failed: %v", err)
	}
	if task.Status.State != protocol.TaskStateCompleted {
		t.Fatalf("unexpected task state: %s", task.Status.State)
	}
	if task.Status.Message == nil || task.Status.Message.FirstText() != "来自 deepresearch 的结果" {
		t.Fatalf("unexpected task output: %#v", task.Status.Message)
	}
}

func TestHostHTTPServer_SendMessage_EndToEnd(t *testing.T) {
	llmServer := newMockLLMServer(t, func(prompt string) string {
		if strings.Contains(prompt, "Host 路由助手") {
			return "false"
		}
		if strings.Contains(prompt, "通用中文助手") {
			return "HTTP链路直答结果"
		}
		return "false"
	})

	a := &Agent{
		llmClient: llm.NewClient(llmServer.URL, ""),
		chatModel: "mock",
		AgentInfoTool: tools.NewRawHTTPTool("agent_info", "", nil, func(ctx context.Context, params map[string]any) (map[string]any, error) {
			return map[string]any{"agents": []any{map[string]any{"name": "deepresearch"}}}, nil
		}),
		CallAgentTool: tools.NewRawHTTPTool("call_agent", "", nil, func(ctx context.Context, params map[string]any) (map[string]any, error) {
			return map[string]any{"response": "不应触发"}, nil
		}),
	}

	eng := orchestrator.NewEngine(orchestrator.Config{DefaultTaskTimeoutSec: 30, RetryMaxAttempts: 1}, orchestrator.NewInMemoryAgentRegistry())
	a.orchestratorEngine = eng
	if err := a.orchestratorEngine.RegisterWorker(orchestrator.AgentDescriptor{ID: HostWorkflowWorkerID, Name: "test", Capabilities: []orchestrator.AgentCapability{"chat_model", "tool"}}, &workflowNodeWorker{agent: a}); err != nil {
		t.Fatalf("register worker failed: %v", err)
	}
	wf, err := buildHostWorkflow()
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
	taskID, err := client.SendMessage(ctx, protocol.Message{Role: protocol.MessageRoleUser, Parts: []protocol.Part{protocol.NewTextPart("你好")}})
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
			if task.Status.Message == nil || task.Status.Message.FirstText() != "HTTP链路直答结果" {
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
