package interviewsimulator

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
		Name:        "interviewsimulator",
		Description: "面试模拟助手，基于用户上传简历生成结构化模拟面试题与回答要点",
		Version:     "0.0.1",
		Provider:    &protocol.AgentProvider{Organization: "a2a_samples"},
		Capabilities: protocol.AgentCapabilities{
			PushNotifications:      boolPtr(true),
			StateTransitionHistory: boolPtr(true),
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Skills: []protocol.AgentSkill{{
			ID:          "interview_simulation",
			Name:        "面试模拟",
			Description: stringPtr("基于简历内容进行面试模拟，输出问题、回答要点、追问和改进建议"),
			Tags:        []string{"interview", "resume", "mock"},
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
