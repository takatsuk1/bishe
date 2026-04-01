package deepresearch

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

func TestBuildDeepResearchWorkflow(t *testing.T) {
	wf, err := buildDeepResearchWorkflow()
	if err != nil {
		t.Fatalf("build workflow failed: %v", err)
	}

	if wf.StartNodeID != "N_start" {
		t.Fatalf("unexpected start node: %s", wf.StartNodeID)
	}
	if len(wf.Nodes) != 7 {
		t.Fatalf("unexpected node count: %d", len(wf.Nodes))
	}

	loop := wf.Nodes["N_loop"]
	if loop.Type != orchestrator.NodeTypeLoop || loop.LoopConfig == nil {
		t.Fatalf("loop node config invalid: %+v", loop)
	}
	if loop.LoopConfig.MaxIterations != 5 {
		t.Fatalf("unexpected loop max iterations: %d", loop.LoopConfig.MaxIterations)
	}

	if !hasEdgeWithLabel(wf, "N_loop", "N_judge", "body") {
		t.Fatalf("missing loop body edge")
	}
	if !hasEdgeWithLabel(wf, "N_condition", "N_end", "true") {
		t.Fatalf("missing condition true edge to end")
	}
	if !hasEdgeWithLabel(wf, "N_loop", "N_end", "exit") {
		t.Fatalf("missing loop exit edge")
	}
}

func TestNormalizeBoolResponse(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{in: "true", want: "true"},
		{in: "FALSE", want: "false"},
		{in: "信息不足", want: "false"},
		{in: "当前信息足够回答", want: "true"},
	}
	for _, tc := range cases {
		got := normalizeBoolResponse(tc.in)
		if got != tc.want {
			t.Fatalf("normalizeBoolResponse(%q)=%q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTrimForTavilyQuery(t *testing.T) {
	in := "前缀\n=== 当前问题 ===\n用户: 2026年中国GDP"
	got := trimForTavilyQuery(in)
	if got != "2026年中国GDP" {
		t.Fatalf("unexpected query: %q", got)
	}
}

func TestDeepResearchProcessInternal_EndToEnd(t *testing.T) {
	llmServer := newMockLLMServer(t, func(prompt string) string {
		_ = prompt
		return "true"
	})

	toolCalled := false
	a := &Agent{
		llmClient: llm.NewClient(llmServer.URL, ""),
		chatModel: "mock",
		TavilyTool: tools.NewRawHTTPTool("tavily", "", nil, func(ctx context.Context, params map[string]any) (map[string]any, error) {
			toolCalled = true
			return map[string]any{"json": map[string]any{"results": []any{}}}, nil
		}),
	}

	eng := orchestrator.NewEngine(orchestrator.Config{DefaultTaskTimeoutSec: 30, RetryMaxAttempts: 1}, orchestrator.NewInMemoryAgentRegistry())
	a.orchestratorEngine = eng
	if err := a.orchestratorEngine.RegisterWorker(orchestrator.AgentDescriptor{ID: DeepResearchWorkflowWorkerID, Name: "test", Capabilities: []orchestrator.AgentCapability{"chat_model", "tool"}}, &workflowNodeWorker{agent: a}); err != nil {
		t.Fatalf("register worker failed: %v", err)
	}
	wf, err := buildDeepResearchWorkflow()
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

	msg := protocol.Message{Role: protocol.MessageRoleUser, Parts: []protocol.Part{protocol.NewTextPart("请分析某行业趋势")}}
	if err = a.ProcessInternal(ctx, taskID, msg, mgr); err != nil {
		t.Fatalf("ProcessInternal failed: %v", err)
	}
	if !toolCalled {
		t.Fatalf("tavily should be called at least once before judge can end")
	}

	task, err := mgr.GetTask(ctx, taskID)
	if err != nil {
		t.Fatalf("get task failed: %v", err)
	}
	if task.Status.State != protocol.TaskStateCompleted {
		t.Fatalf("unexpected task state: %s", task.Status.State)
	}
	if task.Status.Message == nil || task.Status.Message.FirstText() == "" {
		t.Fatalf("unexpected task output: %#v", task.Status.Message)
	}
}

func TestDeepResearchHTTPServer_SendMessage_EndToEnd(t *testing.T) {
	llmServer := newMockLLMServer(t, func(prompt string) string {
		_ = prompt
		return "true"
	})

	a := &Agent{
		llmClient: llm.NewClient(llmServer.URL, ""),
		chatModel: "mock",
		TavilyTool: tools.NewRawHTTPTool("tavily", "", nil, func(ctx context.Context, params map[string]any) (map[string]any, error) {
			return map[string]any{"json": map[string]any{"results": []any{}}}, nil
		}),
	}

	eng := orchestrator.NewEngine(orchestrator.Config{DefaultTaskTimeoutSec: 30, RetryMaxAttempts: 1}, orchestrator.NewInMemoryAgentRegistry())
	a.orchestratorEngine = eng
	if err := a.orchestratorEngine.RegisterWorker(orchestrator.AgentDescriptor{ID: DeepResearchWorkflowWorkerID, Name: "test", Capabilities: []orchestrator.AgentCapability{"chat_model", "tool"}}, &workflowNodeWorker{agent: a}); err != nil {
		t.Fatalf("register worker failed: %v", err)
	}
	wf, err := buildDeepResearchWorkflow()
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
	taskID, err := client.SendMessage(ctx, protocol.Message{Role: protocol.MessageRoleUser, Parts: []protocol.Part{protocol.NewTextPart("请帮我做研究")}})
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
			if task.Status.Message == nil || task.Status.Message.FirstText() == "" {
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

func TestCollectResearchSources(t *testing.T) {
	finalOutput := map[string]any{
		"result": map[string]any{
			"json": map[string]any{
				"results": []any{
					map[string]any{"title": "A", "url": "https://a.example", "content": "alpha", "score": 0.9},
					map[string]any{"title": "B", "url": "https://b.example", "content": "beta", "score": 0.7},
				},
			},
		},
	}

	sources := collectResearchSources(finalOutput)
	if len(sources) != 2 {
		t.Fatalf("unexpected source count: %d", len(sources))
	}
	if sources[0].Title != "A" {
		t.Fatalf("unexpected first source: %+v", sources[0])
	}
}

func TestBuildStructuredResponse_FiltersRawMapLikeResponse(t *testing.T) {
	a := &Agent{}
	out := a.buildStructuredResponse(context.Background(), "task-1", "重庆邮电大学介绍", map[string]any{
		"response": "map[status_code:200 json:map[results:[...]]]",
	})
	if strings.Contains(out, "map[") {
		t.Fatalf("structured response should not include raw map text: %s", out)
	}
	if !strings.Contains(out, "研究结论") {
		t.Fatalf("expected structured sections in output: %s", out)
	}
}

func TestSanitizeRawResponse_BoolLiteral(t *testing.T) {
	if got := sanitizeRawResponse("true"); got != "" {
		t.Fatalf("expected empty for true, got %q", got)
	}
	if got := sanitizeRawResponse("false"); got != "" {
		t.Fatalf("expected empty for false, got %q", got)
	}
}

func TestHasSearchEvidence(t *testing.T) {
	if hasSearchEvidence(nil) {
		t.Fatalf("nil payload should be false")
	}
	if hasSearchEvidence(map[string]any{"x": 1}) {
		t.Fatalf("payload without results should be false")
	}
	if !hasSearchEvidence(map[string]any{"N_tavily": map[string]any{"response": "ok"}}) {
		t.Fatalf("payload with N_tavily node output should be true")
	}
	if !hasSearchEvidence(map[string]any{
		"N_tavily": map[string]any{
			"result": map[string]any{
				"json": map[string]any{
					"results": []any{map[string]any{"title": "ok"}},
				},
			},
		},
	}) {
		t.Fatalf("payload with results should be true")
	}
}

func hasEdgeWithLabel(wf *orchestrator.Workflow, from, to, label string) bool {
	for _, e := range wf.Edges {
		if e.From == from && e.To == to && e.Label == label {
			return true
		}
	}
	return false
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
