package tools

import (
	"ai/config"
	"ai/pkg/transport/agentpool"
	"ai/pkg/transport/httpagent"
	"context"
	"fmt"
	"time"

	internalproto "ai/pkg/protocol"
)

// AgentInfo 定义了agent信息结构
type AgentInfo struct {
	Name        string
	Description string
	Skills      []internalproto.AgentSkill
}

// ClientAgent 定义了客户端agent结构
type ClientAgent struct {
	HTTPClient *httpagent.Client
	AgentCard  *internalproto.AgentCard
}

// AgentInfoManager 管理agent信息
type AgentInfoManager struct {
	agentClientMap map[string]*ClientAgent
	agentInfo      []AgentInfo
}

// NewAgentInfoManager 创建一个新的AgentInfoManager实例
func NewAgentInfoManager(ctx context.Context, cfg *config.MainConfig) (*AgentInfoManager, error) {
	manager := &AgentInfoManager{}
	if err := manager.initAgentMap(ctx, cfg); err != nil {
		return nil, err
	}
	return manager, nil
}

// initAgentMap 初始化agent映射
func (m *AgentInfoManager) initAgentMap(ctx context.Context, cfg *config.MainConfig) error {
	_ = ctx
	m.agentClientMap = make(map[string]*ClientAgent)
	m.agentInfo = nil
	entries, err := agentpool.BuildFromConfig(cfg.HostAgent.Agents, time.Minute*10)
	if err != nil {
		return err
	}
	for name, entry := range entries {
		m.agentClientMap[name] = &ClientAgent{HTTPClient: entry.Client, AgentCard: entry.Card}
		m.agentInfo = append(m.agentInfo, AgentInfo{
			Name:        name,
			Description: entry.Card.Description,
			Skills:      entry.Card.Skills,
		})
	}
	return nil
}

// GetAgentInfos 获取所有agent信息
func (m *AgentInfoManager) GetAgentInfos() []AgentInfo {
	out := make([]AgentInfo, len(m.agentInfo))
	copy(out, m.agentInfo)
	return out
}

// GetAgentClient 获取指定agent的客户端
func (m *AgentInfoManager) GetAgentClient(name string) (*httpagent.Client, error) {
	clientAgent, ok := m.agentClientMap[name]
	if !ok || clientAgent == nil {
		return nil, fmt.Errorf("agent %s not found", name)
	}
	return clientAgent.HTTPClient, nil
}

// GetAgentCard 获取指定agent的卡片信息
func (m *AgentInfoManager) GetAgentCard(name string) (*internalproto.AgentCard, error) {
	clientAgent, ok := m.agentClientMap[name]
	if !ok || clientAgent == nil {
		return nil, fmt.Errorf("agent %s not found", name)
	}
	return clientAgent.AgentCard, nil
}
