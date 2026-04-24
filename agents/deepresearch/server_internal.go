package deepresearch

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

// ProcessMessage 是 deepresearch 的对外消息入口：
// 它会创建任务、订阅事件流，并异步调用内部工作流处理函数。
func (p *internalProcessor) ProcessMessage(ctx context.Context, message protocol.Message,
	manager taskmanager.Manager) (string, <-chan protocol.StreamEvent, error) {
	// 先创建任务，后续所有状态变化都围绕这个 taskID 展开。
	taskID, err := manager.BuildTask(message.TaskID, nil)
	if err != nil {
		return "", nil, fmt.Errorf("failed to build task: %w", err)
	}
	// 在真正执行前先订阅任务流，避免丢失最早的 working 事件。
	subscriber, err := manager.SubscribeTask(ctx, taskID)
	if err != nil {
		return "", nil, fmt.Errorf("failed to subscribe task: %w", err)
	}
	go func() {
		// 异步执行内部逻辑，让 HTTP 层可以立即返回 taskID 和事件订阅通道。
		_ = manager.UpdateTaskState(ctx, taskID, protocol.TaskStateWorking, nil)
		if runErr := p.agent.ProcessInternal(ctx, taskID, message, manager); runErr != nil {
			// 发生错误时，把错误信息包装成 agent 消息写回任务状态。
			_ = manager.UpdateTaskState(ctx, taskID, protocol.TaskStateFailed, &protocol.Message{
				Role:  protocol.MessageRoleAgent,
				Parts: []protocol.Part{protocol.NewTextPart(runErr.Error())},
			})
		}
	}()
	return taskID, subscriber, nil
}

// NewHTTPServer 负责把 deepresearch agent 封装成标准 HTTP 服务。
// 这里会声明 agent 的对外元信息，并接入统一的 httpagent.Server。
func NewHTTPServer(agent *Agent) (http.Handler, error) {
	// AgentCard 是对外暴露的“能力名片”，描述名称、技能、输入输出模式等信息。
	card := protocol.AgentCard{
		Name:        "deep_researcher",
		Description: "通过循环检索和评估，使用 Tavily 进行深度信息检索",
		Version:     "0.0.1",
		Provider:    &protocol.AgentProvider{Organization: "a2a_samples"},
		Capabilities: protocol.AgentCapabilities{
			PushNotifications:      boolPtr(true),
			StateTransitionHistory: boolPtr(true),
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Skills: []protocol.AgentSkill{{
			ID:          "deep_research",
			Name:        "深度检索",
			Description: stringPtr("通过 Tavily 的循环检索来收集信息，并在信息充分时结束检索"),
			Tags:        []string{"deep research", "tavily"},
			InputModes:  []string{"text"},
			OutputModes: []string{"text"},
		}},
	}

	// 使用内存版任务管理器承接任务状态与流式消息。
	mgr := taskmanager.NewInMemoryManager()
	srv, err := httpagent.NewServer(card, mgr, &internalProcessor{agent: agent})
	if err != nil {
		return nil, err
	}
	return srv.Handler(), nil
}

// stringPtr / boolPtr 是构造 AgentCard 指针字段时的辅助函数。
func stringPtr(s string) *string { return &s }
func boolPtr(b bool) *bool       { return &b }
