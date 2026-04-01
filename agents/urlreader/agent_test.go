package urlreader

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

func TestBuildURLReaderWorkflow(t *testing.T) {
	wf, err := buildURLReaderWorkflow()
	if err != nil {
		t.Fatalf("build workflow failed: %v", err)
	}

	if wf.StartNodeID != "N_start" {
		t.Fatalf("unexpected start node: %s", wf.StartNodeID)
	}
	if len(wf.Nodes) != 5 {
		t.Fatalf("unexpected node count: %d", len(wf.Nodes))
	}
	if wf.Nodes["N_extract_url"].Type != orchestrator.NodeTypeChatModel {
		t.Fatalf("N_extract_url type mismatch")
	}
	if wf.Nodes["N_fetch"].Type != orchestrator.NodeTypeTool {
		t.Fatalf("N_fetch type mismatch")
	}
	if wf.Nodes["N_fetch"].Config["tool_name"] != "fetch" {
		t.Fatalf("N_fetch tool_name mismatch: %v", wf.Nodes["N_fetch"].Config["tool_name"])
	}
}

func TestFirstURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{in: "访问 https://example.com 读取内容", want: "https://example.com"},
		{in: "http://a.com 和 https://b.com", want: "http://a.com"},
		{in: "没有链接", want: ""},
	}

	for _, tc := range cases {
		got := firstURL(tc.in)
		if got != tc.want {
			t.Fatalf("firstURL(%q)=%q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestURLReaderProcessInternal_EndToEnd(t *testing.T) {
	llmServer := newMockLLMServer(t, func(prompt string) string {
		if strings.Contains(prompt, "URL 提取助手") {
			return "https://example.com/article"
		}
		if strings.Contains(prompt, "网页内容整理助手") {
			return "网页摘要完成"
		}
		return ""
	})

	toolCalled := false
	a := &Agent{
		llmClient: llm.NewClient(llmServer.URL, ""),
		chatModel: "mock",
		FetchTool: tools.NewRawHTTPTool("fetch", "", nil, func(ctx context.Context, params map[string]any) (map[string]any, error) {
			toolCalled = true
			if params["url"] != "https://example.com/article" {
				t.Fatalf("unexpected url param: %v", params["url"])
			}
			return map[string]any{"body": "mock page content"}, nil
		}),
	}

	eng := orchestrator.NewEngine(orchestrator.Config{DefaultTaskTimeoutSec: 30, RetryMaxAttempts: 1}, orchestrator.NewInMemoryAgentRegistry())
	a.orchestratorEngine = eng
	if err := a.orchestratorEngine.RegisterWorker(orchestrator.AgentDescriptor{ID: URLReaderWorkflowWorkerID, Name: "test", Capabilities: []orchestrator.AgentCapability{"chat_model", "tool"}}, &workflowNodeWorker{agent: a}); err != nil {
		t.Fatalf("register worker failed: %v", err)
	}
	wf, err := buildURLReaderWorkflow()
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

	msg := protocol.Message{Role: protocol.MessageRoleUser, Parts: []protocol.Part{protocol.NewTextPart("请读一下这个链接 https://example.com/article")}}
	if err = a.ProcessInternal(ctx, taskID, msg, mgr); err != nil {
		t.Fatalf("ProcessInternal failed: %v", err)
	}
	if !toolCalled {
		t.Fatalf("fetch tool was not called")
	}

	task, err := mgr.GetTask(ctx, taskID)
	if err != nil {
		t.Fatalf("get task failed: %v", err)
	}
	if task.Status.State != protocol.TaskStateCompleted {
		t.Fatalf("unexpected task state: %s", task.Status.State)
	}
	if task.Status.Message == nil || task.Status.Message.FirstText() != "网页摘要完成" {
		t.Fatalf("unexpected task output: %#v", task.Status.Message)
	}
}

func TestURLReaderHTTPServer_SendMessage_EndToEnd(t *testing.T) {
	llmServer := newMockLLMServer(t, func(prompt string) string {
		if strings.Contains(prompt, "URL 提取助手") {
			return "https://example.com/article"
		}
		if strings.Contains(prompt, "网页内容整理助手") {
			return "网页摘要完成"
		}
		return ""
	})

	a := &Agent{
		llmClient: llm.NewClient(llmServer.URL, ""),
		chatModel: "mock",
		FetchTool: tools.NewRawHTTPTool("fetch", "", nil, func(ctx context.Context, params map[string]any) (map[string]any, error) {
			return map[string]any{"body": "mock page content"}, nil
		}),
	}

	eng := orchestrator.NewEngine(orchestrator.Config{DefaultTaskTimeoutSec: 30, RetryMaxAttempts: 1}, orchestrator.NewInMemoryAgentRegistry())
	a.orchestratorEngine = eng
	if err := a.orchestratorEngine.RegisterWorker(orchestrator.AgentDescriptor{ID: URLReaderWorkflowWorkerID, Name: "test", Capabilities: []orchestrator.AgentCapability{"chat_model", "tool"}}, &workflowNodeWorker{agent: a}); err != nil {
		t.Fatalf("register worker failed: %v", err)
	}
	wf, err := buildURLReaderWorkflow()
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
	taskID, err := client.SendMessage(ctx, protocol.Message{Role: protocol.MessageRoleUser, Parts: []protocol.Part{protocol.NewTextPart("请读 https://example.com/article")}})
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
			if task.Status.Message == nil || task.Status.Message.FirstText() != "网页摘要完成" {
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
