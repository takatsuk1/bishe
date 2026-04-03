package schedulehelper

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
	"ai/pkg/transport/httpagent"
)

func TestBuildScheduleHelperWorkflow(t *testing.T) {
	wf, err := buildScheduleHelperWorkflow()
	if err != nil {
		t.Fatalf("build workflow failed: %v", err)
	}
	if wf.StartNodeID != "start" {
		t.Fatalf("unexpected start node: %s", wf.StartNodeID)
	}
	if len(wf.Nodes) != 4 {
		t.Fatalf("unexpected node count: %d", len(wf.Nodes))
	}
}

func TestScheduleHelperProcessInternal_EndToEnd(t *testing.T) {
	llmServer := newMockLLMServer(t, func(prompt string) string {
		if strings.Contains(prompt, "个人生活日程规划助手") {
			return "初版计划"
		}
		if strings.Contains(prompt, "计划优化助手") {
			return "优化后计划"
		}
		return ""
	})

	a := &Agent{
		llmClient: llm.NewClient(llmServer.URL, ""),
		chatModel: "mock",
	}

	eng := orchestrator.NewEngine(orchestrator.Config{DefaultTaskTimeoutSec: 30, RetryMaxAttempts: 1}, orchestrator.NewInMemoryAgentRegistry())
	a.orchestratorEngine = eng
	if err := a.orchestratorEngine.RegisterWorker(orchestrator.AgentDescriptor{ID: ScheduleHelperWorkflowWorkerID, Name: "test", Capabilities: []orchestrator.AgentCapability{"chat_model"}}, &workflowNodeWorker{agent: a}); err != nil {
		t.Fatalf("register worker failed: %v", err)
	}
	wf, err := buildScheduleHelperWorkflow()
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

	msg := protocol.Message{Role: protocol.MessageRoleUser, Parts: []protocol.Part{protocol.NewTextPart("帮我安排明天的学习和运动")}}
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
	if task.Status.Message == nil || task.Status.Message.FirstText() != "优化后计划" {
		t.Fatalf("unexpected task output: %#v", task.Status.Message)
	}
}

func TestScheduleHelperHTTPServer_SendMessage_EndToEnd(t *testing.T) {
	llmServer := newMockLLMServer(t, func(prompt string) string {
		if strings.Contains(prompt, "个人生活日程规划助手") {
			return "初版计划"
		}
		if strings.Contains(prompt, "计划优化助手") {
			return "优化后计划"
		}
		return ""
	})

	a := &Agent{llmClient: llm.NewClient(llmServer.URL, ""), chatModel: "mock"}
	eng := orchestrator.NewEngine(orchestrator.Config{DefaultTaskTimeoutSec: 30, RetryMaxAttempts: 1}, orchestrator.NewInMemoryAgentRegistry())
	a.orchestratorEngine = eng
	if err := a.orchestratorEngine.RegisterWorker(orchestrator.AgentDescriptor{ID: ScheduleHelperWorkflowWorkerID, Name: "test", Capabilities: []orchestrator.AgentCapability{"chat_model"}}, &workflowNodeWorker{agent: a}); err != nil {
		t.Fatalf("register worker failed: %v", err)
	}
	wf, err := buildScheduleHelperWorkflow()
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
	taskID, err := client.SendMessage(ctx, protocol.Message{Role: protocol.MessageRoleUser, Parts: []protocol.Part{protocol.NewTextPart("安排明天任务")}})
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
			if task.Status.Message == nil || task.Status.Message.FirstText() != "优化后计划" {
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
