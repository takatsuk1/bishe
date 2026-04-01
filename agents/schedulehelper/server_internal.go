package schedulehelper

import (
	"context"
	"fmt"
	"net/http"

	"ai/pkg/protocol"
	"ai/pkg/taskmanager"
	"ai/pkg/transport/httpagent"
)

type internalProcessor struct {
	agent *Agent
}

func (p *internalProcessor) ProcessMessage(ctx context.Context, message protocol.Message, manager taskmanager.Manager) (string, <-chan protocol.StreamEvent, error) {
	taskID, err := manager.BuildTask(message.TaskID, nil)
	if err != nil {
		return "", nil, fmt.Errorf("failed to build task: %w", err)
	}
	subscriber, err := manager.SubscribeTask(ctx, taskID)
	if err != nil {
		return "", nil, fmt.Errorf("failed to subscribe task: %w", err)
	}
	go func() {
		_ = manager.UpdateTaskState(ctx, taskID, protocol.TaskStateWorking, nil)
		if runErr := p.agent.ProcessInternal(ctx, taskID, message, manager); runErr != nil {
			_ = manager.UpdateTaskState(ctx, taskID, protocol.TaskStateFailed, &protocol.Message{
				Role:  protocol.MessageRoleAgent,
				Parts: []protocol.Part{protocol.NewTextPart(runErr.Error())},
			})
		}
	}()
	return taskID, subscriber, nil
}

func NewHTTPServer(agent *Agent) (http.Handler, error) {
	card := protocol.AgentCard{
		Name:        "schedulehelper",
		Description: "日程规划助手，生成任务优先级与时间安排建议",
		Version:     "0.0.1",
		Provider:    &protocol.AgentProvider{Organization: "a2a_samples"},
		Capabilities: protocol.AgentCapabilities{
			PushNotifications:      boolPtr(true),
			StateTransitionHistory: boolPtr(true),
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Skills: []protocol.AgentSkill{{
			ID:          "schedule_plan",
			Name:        "日程规划",
			Description: stringPtr("输出结构化日程安排与提醒建议"),
			Tags:        []string{"schedule", "todo", "planning"},
			InputModes:  []string{"text"},
			OutputModes: []string{"text"},
		}},
	}

	mgr := taskmanager.NewInMemoryManager()
	srv, err := httpagent.NewServer(card, mgr, &internalProcessor{agent: agent})
	if err != nil {
		return nil, err
	}
	return srv.Handler(), nil
}

func stringPtr(s string) *string { return &s }
func boolPtr(b bool) *bool       { return &b }
