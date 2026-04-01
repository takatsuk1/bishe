//go:build reference
// +build reference

package deepresearch

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/server"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
	redistaskmanager "trpc.group/trpc-go/trpc-a2a-go/taskmanager/redis"
	tgoredis "trpc.group/trpc-go/trpc-database/goredis"
	"trpc.group/trpc-go/trpc-go/log"
)

func NewA2AServer(agent *Agent) (*server.A2AServer, error) {
	var err error
	agentCard := getAgentCard()
	processor := &TaskProcessor{}
	processor.agent = agent
	redisCli, err := tgoredis.New("trpc.redis.deepresearch")
	if err != nil {
		return nil, fmt.Errorf("failed to create redis client: %w", err)
	}

	// 单机redis TODO 优化
	cli, ok := redisCli.(*redis.Client)
	if !ok {

		log.Fatal("UniversalClient is not *redis.Client")
	}
	taskManager, err := redistaskmanager.NewTaskManager(cli, processor)
	if err != nil {
		return nil, fmt.Errorf("failed to create task manager: %w", err)
	}
	srv, err := server.NewA2AServer(agentCard, taskManager)
	if err != nil {
		return nil, fmt.Errorf("failed to create A2A server: %w", err)
	}
	return srv, nil
}

// Helper function to create a string pointer
func stringPtr(s string) *string {
	return &s
}

// boolPtr is a helper function to get a pointer to a bool.
func boolPtr(b bool) *bool {
	return &b
}

func getAgentCard() server.AgentCard {
	agentCard := server.AgentCard{
		Name:        "deep_researcher",
		Description: "",
		Version:     "0.0.1",
		Provider: &server.AgentProvider{
			Organization: "a2a_samples",
		},
		Capabilities: server.AgentCapabilities{
			PushNotifications:      boolPtr(true), // Enable push notifications
			StateTransitionHistory: boolPtr(true), // MemoryTaskManager stores history
		},
		// Support text input/output
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Skills: []server.AgentSkill{
			{
				ID:          "search",
				Name:        "深度搜索",
				Description: stringPtr("通过联网搜索来搜集相关信息，然后根据这些信息来回答用户的问题"),
				Tags:        []string{"deep research"},
				Examples:    []string{"找到中国GPD超过万亿的城市，详细分析其中排名后10位的城市增长率和GPC构成，并结合各城市规划预测五年后这些城市GDP排名可能会如何变化"},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
		},
	}
	return agentCard
}

type TaskProcessor struct {
	agent *Agent
}

func (t *TaskProcessor) ProcessMessage(
	ctx context.Context,
	message protocol.Message,
	options taskmanager.ProcessOptions,
	handle taskmanager.TaskHandler,
) (*taskmanager.MessageProcessingResult, error) {
	taskID, err := handle.BuildTask(message.TaskID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build task: %w", err)
	}
	subscriber, err := handle.SubscribeTask(&taskID)
	if err != nil {
		return nil, fmt.Errorf("failed to subscribe to task: %w", err)
	}
	go func() {
		if err := t.agent.Process(ctx, taskID, message, handle); err != nil {
			log.ErrorContextf(ctx, "process %s fail, err: %v", message, err)
		}
	}()
	return &taskmanager.MessageProcessingResult{
		StreamingEvents: subscriber,
	}, nil
}
