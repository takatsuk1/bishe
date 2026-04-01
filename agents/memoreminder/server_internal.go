package memoreminder

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

func (p *internalProcessor) ProcessMessage(ctx context.Context, message protocol.Message,
	manager taskmanager.Manager) (string, <-chan protocol.StreamEvent, error) {
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
		Name:        "memo_reminder",
		Description: "备忘录提醒助手，结构化记录提醒并在指定时间前10分钟弹窗提醒",
		Version:     "0.0.1",
		Provider:    &protocol.AgentProvider{Organization: "a2a_samples"},
		Capabilities: protocol.AgentCapabilities{
			PushNotifications:      boolPtr(true),
			StateTransitionHistory: boolPtr(true),
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Skills: []protocol.AgentSkill{{
			ID:          "memo_remind",
			Name:        "备忘录提醒",
			Description: stringPtr("记录备忘提醒并定时触发弹窗提醒"),
			Tags:        []string{"reminder", "memo", "notification"},
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
