//go:build reference
// +build reference

package lbshelper

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
	redisCli, err := tgoredis.New("trpc.redis.lbshelper")
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
		Name:        "lbshelper",
		Description: "一个行程智能助手，可以利用工具进行网络搜索，帮助用户解决行程规划相关的问题。",
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
				ID:          "query_weather",
				Name:        "根据城市名称或者标准adcode查询指定城市的天气",
				Description: stringPtr(""),
				Tags:        []string{"weather"},
				Examples:    []string{"深圳今天的天气怎么样"},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
			{
				ID:          "maps_direction",
				Name:        "查询城市之间的路线",
				Description: stringPtr(""),
				Tags:        []string{"weather"},
				Examples:    []string{"深圳到广州怎么走"},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
			{
				ID:          "travel_planning",
				Name:        "查询旅行攻略",
				Description: stringPtr("搜索目的地，查询旅行攻略"),
				Tags:        []string{"travel"},
				Examples:    []string{"广州长隆有哪些好玩的项目"},
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
	// 启动Agent任务
	fmt.Println("行程助手收到消息:", message)
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
