package tools

import (
	"ai/config"
	"ai/pkg/agentfmt"
	"ai/pkg/transport/httpagent"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	internalproto "ai/pkg/protocol"
)

type UserAgentNameLister func(ctx context.Context, userID string) ([]string, error)

type CallAgentTool struct {
	*BaseTool
	agentInfoManager *AgentInfoManager
	listUserAgents   UserAgentNameLister
	presetAgents     map[string]struct{}
}

func NewCallAgentTool(ctx context.Context, cfg *config.MainConfig, lister UserAgentNameLister) (*CallAgentTool, error) {
	manager, err := NewAgentInfoManager(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("init agent info manager: %w", err)
	}

	preset := make(map[string]struct{})
	for _, info := range manager.GetAgentInfos() {
		name := strings.TrimSpace(info.Name)
		if name != "" {
			preset[name] = struct{}{}
		}
	}

	params := []ToolParameter{
		{Name: "agent_name", Type: ParamTypeString, Required: true, Description: "目标 agent 名称"},
		{Name: "text", Type: ParamTypeString, Required: true, Description: "发送给目标 agent 的文本"},
		{Name: "allowed_agents", Type: ParamTypeArray, Required: true, Description: "允许调用的 agent 白名单"},
		{Name: "user_id", Type: ParamTypeString, Required: false, Description: "当前用户 ID，用于校验自有 agent"},
		{Name: "api_key", Type: ParamTypeString, Required: false, Description: "下游调用鉴权 token"},
		{Name: "task_id", Type: ParamTypeString, Required: false, Description: "父任务 ID（用于链路追踪）"},
	}

	return &CallAgentTool{
		BaseTool:         NewBaseTool("call_agent", ToolTypeFunction, "调用受白名单约束的下游 Agent", params),
		agentInfoManager: manager,
		listUserAgents:   lister,
		presetAgents:     preset,
	}, nil
}

func (t *CallAgentTool) Execute(ctx context.Context, params map[string]any) (map[string]any, error) {
	if err := ValidateParameters(params, t.info.Parameters); err != nil {
		return nil, err
	}

	agentName := strings.TrimSpace(fmt.Sprint(params["agent_name"]))
	text := strings.TrimSpace(fmt.Sprint(params["text"]))
	if agentName == "" {
		return nil, fmt.Errorf("agent_name is empty")
	}
	if text == "" {
		return nil, fmt.Errorf("text is empty")
	}

	allowedSet := toStringSet(params["allowed_agents"])
	if len(allowedSet) == 0 {
		return nil, fmt.Errorf("allowed_agents is empty")
	}
	if _, ok := allowedSet[agentName]; !ok {
		return nil, fmt.Errorf("agent %s is not in allowed_agents", agentName)
	}

	userID := strings.TrimSpace(fmt.Sprint(params["user_id"]))
	if userID != "" {
		if err := t.validateOwnerScope(ctx, userID, agentName); err != nil {
			return nil, err
		}
	}

	client, err := t.agentInfoManager.GetAgentClient(agentName)
	if err != nil {
		return nil, fmt.Errorf("get agent client %s: %w", agentName, err)
	}

	callCtx := ctx
	authToken := strings.TrimSpace(fmt.Sprint(params["api_key"]))
	if authToken != "" {
		callCtx = httpagent.WithAuthorizationToken(callCtx, authToken)
	}

	parentTaskID := strings.TrimSpace(fmt.Sprint(params["task_id"]))
	if parentTaskID == "" {
		parentTaskID = "call_agent"
	}
	childTaskID := fmt.Sprintf("%s:%s:%s", agentName, parentTaskID, uuid.NewString())

	sentTaskID, err := client.SendMessage(callCtx, internalproto.Message{
		Role:   internalproto.MessageRoleUser,
		Parts:  []internalproto.Part{internalproto.NewTextPart(text)},
		TaskID: &childTaskID,
		Metadata: map[string]any{
			"user_id": userID,
			"api_key": authToken,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("send message to %s: %w", agentName, err)
	}

	stream, streamErr := client.StreamTaskEvents(callCtx, sentTaskID)
	lastText := ""
	for event := range stream {
		if event.TaskStatusUpdate == nil {
			continue
		}
		status := event.TaskStatusUpdate.Status
		current := extractTextFromStatus(status)
		if cleaned := strings.TrimSpace(agentfmt.Clean(current)); cleaned != "" {
			lastText = cleaned
		}

		switch status.State {
		case internalproto.TaskStateCompleted:
			if lastText == "" {
				lastText = "任务已完成，但未返回可展示内容。"
			}
			return map[string]any{
				"response":   agentfmt.Beautify(agentName, text, lastText),
				"agent_name": agentName,
				"task_id":    event.TaskStatusUpdate.TaskID,
			}, nil
		case internalproto.TaskStateFailed, internalproto.TaskStateCanceled:
			if lastText == "" {
				lastText = string(status.State)
			}
			return nil, errors.New(lastText)
		}
	}

	if err = <-streamErr; err != nil {
		return nil, fmt.Errorf("agent stream error: %w", err)
	}

	return nil, fmt.Errorf("agent stream closed without terminal state")
}

func (t *CallAgentTool) validateOwnerScope(ctx context.Context, userID, agentName string) error {
	if _, ok := t.presetAgents[agentName]; ok {
		return nil
	}
	if t.listUserAgents == nil {
		return fmt.Errorf("agent %s is not a preset agent", agentName)
	}
	owned, err := t.listUserAgents(ctx, userID)
	if err != nil {
		return fmt.Errorf("list user agents: %w", err)
	}
	for _, name := range owned {
		if strings.EqualFold(strings.TrimSpace(name), agentName) {
			return nil
		}
	}
	return fmt.Errorf("agent %s is not owned by user %s", agentName, userID)
}

func toStringSet(v any) map[string]struct{} {
	out := make(map[string]struct{})
	switch vv := v.(type) {
	case []string:
		for _, s := range vv {
			s = strings.TrimSpace(s)
			if s != "" {
				out[s] = struct{}{}
			}
		}
	case []any:
		for _, item := range vv {
			s := strings.TrimSpace(fmt.Sprint(item))
			if s != "" {
				out[s] = struct{}{}
			}
		}
	case string:
		parts := strings.Split(vv, ",")
		for _, p := range parts {
			s := strings.TrimSpace(p)
			if s != "" {
				out[s] = struct{}{}
			}
		}
	}
	return out
}

func extractTextFromStatus(status internalproto.TaskStatus) string {
	if status.Message == nil || len(status.Message.Parts) == 0 {
		return ""
	}
	for _, p := range status.Message.Parts {
		if p.Type == internalproto.PartTypeText {
			return p.Text
		}
	}
	return ""
}

func NewDefaultCallAgentTool() (Tool, error) {
	cfg := config.GetMainConfig()
	return NewCallAgentTool(context.Background(), cfg, nil)
}

var _ Tool = (*CallAgentTool)(nil)
