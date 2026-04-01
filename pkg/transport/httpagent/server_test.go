package httpagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ai/pkg/protocol"
	"ai/pkg/taskmanager"

	"github.com/stretchr/testify/require"
)

type testProcessor struct{}

func (p *testProcessor) ProcessMessage(ctx context.Context, message protocol.Message,
	manager taskmanager.Manager) (string, <-chan protocol.StreamEvent, error) {
	taskID, err := manager.BuildTask(message.TaskID, map[string]string{"source": "test"})
	if err != nil {
		return "", nil, err
	}
	events, err := manager.SubscribeTask(ctx, taskID)
	if err != nil {
		return "", nil, err
	}
	go func() {
		_ = manager.UpdateTaskState(ctx, taskID, protocol.TaskStateWorking,
			&protocol.Message{Role: protocol.MessageRoleAgent, Parts: []protocol.Part{protocol.NewTextPart("working")}})
		_ = manager.UpdateTaskState(ctx, taskID, protocol.TaskStateCompleted,
			&protocol.Message{Role: protocol.MessageRoleAgent, Parts: []protocol.Part{protocol.NewTextPart("done")}})
	}()
	return taskID, events, nil
}

func TestHTTPAgentServerAndClient(t *testing.T) {
	mgr := taskmanager.NewInMemoryManager()
	srv, err := NewServer(protocol.AgentCard{Name: "tester"}, mgr, &testProcessor{})
	require.NoError(t, err)

	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()

	cli := NewClient(httpSrv.URL, 5*time.Second)
	taskID, err := cli.SendMessage(context.Background(), protocol.Message{
		Role:  protocol.MessageRoleUser,
		Parts: []protocol.Part{protocol.NewTextPart("hello")},
	})
	require.NoError(t, err)
	require.NotEmpty(t, taskID)

	task, err := cli.GetTask(context.Background(), taskID)
	require.NoError(t, err)
	require.Equal(t, taskID, task.ID)

	events, errs := cli.StreamTaskEvents(context.Background(), taskID)
	gotTerminal := false
	for !gotTerminal {
		select {
		case ev, ok := <-events:
			if !ok {
				gotTerminal = true
				break
			}
			if ev.TaskStatusUpdate != nil && ev.TaskStatusUpdate.Status.State.IsTerminal() {
				gotTerminal = true
			}
		case err = <-errs:
			if err != nil {
				t.Fatalf("stream error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for stream events")
		}
	}
}

func TestAgentCardEndpoint(t *testing.T) {
	mgr := taskmanager.NewInMemoryManager()
	srv, err := NewServer(protocol.AgentCard{Name: "card-agent", Version: "1.0.0"}, mgr, &testProcessor{})
	require.NoError(t, err)
	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()

	resp, err := http.Get(httpSrv.URL + "/.well-known/agent.json")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var card protocol.AgentCard
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&card))
	require.Equal(t, "card-agent", card.Name)
}
