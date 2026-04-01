//go:build reference
// +build reference

package host

import (
	"ai/agents/host/memory"
	"ai/agents/host/model"
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-go/log"
)

type SendTask struct {
	agent *Agent
}

func (t *SendTask) Info(ctx context.Context) (*schema.ToolInfo, error) {
	var agentNameEnum []string
	for agentName := range t.agent.agentClientMap {
		agentNameEnum = append(agentNameEnum, agentName)
	}
	return &schema.ToolInfo{
		Name: "send_task",
		Desc: "给Agent发送任务",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"agent_name": {
				Type:     "string",
				Desc:     "发送任务的Agent名字",
				Enum:     agentNameEnum,
				Required: true,
			},
			"message": {
				Type:     "string",
				Desc:     "发送的任务参数",
				Required: true,
			},
		}),
	}, nil
}

type SendTaskParams struct {
	AgentName string `json:"agent_name"`
	Message   string `json:"message"`
}

func (t *SendTask) StreamableRun(ctx context.Context, argumentsInJSON string,
	opts ...tool.Option) (*schema.StreamReader[string], error) {
	params := &SendTaskParams{}
	err := json.Unmarshal([]byte(argumentsInJSON), params)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal arguments: %w", err)
	}

	clientAgent, ok := t.agent.agentClientMap[params.AgentName]
	if !ok {
		return nil, fmt.Errorf("agent %s not found", params.AgentName)
	}

	taskID := model.TaskID{
		AgentName: params.AgentName,
		ID:        uuid.New().String(),
	}
	err = compose.ProcessState(ctx, func(ctx context.Context, s *state) error {
		return s.mem.SetState(ctx, memory.StateKeyCurrentTaskID, taskID.Encode())
	})
	if err != nil {
		return nil, fmt.Errorf("failed to update user state: %w", err)
	}
	taskIdS := taskID.Encode()
	taskChan, err := clientAgent.a2aClient.StreamMessage(ctx, protocol.SendMessageParams{
		Message: protocol.Message{
			Role:   protocol.MessageRoleUser,
			Parts:  []protocol.Part{protocol.NewTextPart(params.Message)},
			TaskID: &taskIdS,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to send task: %w", err)
	}

	sr, sw := schema.Pipe[string](1)
	go func() {
		defer sw.Close()
		for event := range taskChan {
			eventBytes, err := json.Marshal(event)
			sw.Send(string(eventBytes), err)
		}
	}()

	return sr, nil
}

type ClearMemory struct {
	agent *Agent
}

func (t *ClearMemory) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "clear_memory",
		Desc: "清楚上下文，清楚用户记忆",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"all": {
				Type:     "boolean",
				Desc:     "是否清除所有记忆,必须为true",
				Required: true,
			},
		}),
	}, nil
}

func (t *ClearMemory) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	err := compose.ProcessState(ctx, func(ctx context.Context, s *state) error {
		if _, err := s.mem.NewConversation(ctx); err != nil {
			return fmt.Errorf("failed to new conversation: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("failed to clear memory: %w", err)
	}
	log.InfoContextf(ctx, "clear memofy success")
	return "Success", nil
}
