package tools

import (
	"context"
	"fmt"
	"sync"
)

type StdioMCPManager struct {
	mu      sync.Mutex
	clients map[string]*MCPClient
}

var defaultStdioMCPManager = &StdioMCPManager{
	clients: make(map[string]*MCPClient),
}

func GetStdioMCPManager() *StdioMCPManager {
	return defaultStdioMCPManager
}

func (m *StdioMCPManager) Get(toolID string) (*MCPClient, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	client, ok := m.clients[toolID]
	return client, ok
}

func (m *StdioMCPManager) Start(ctx context.Context, toolID string, command string, args []string) (*MCPClient, error) {
	m.mu.Lock()
	if existing, ok := m.clients[toolID]; ok {
		m.mu.Unlock()
		return existing, nil
	}
	m.mu.Unlock()

	client, err := ConnectMCPStdio(ctx, command, args)
	if err != nil {
		return nil, err
	}

	// Warm up connection to keep process alive.
	if _, err := client.ListTools(ctx); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("start stdio mcp failed: %w", err)
	}

	m.mu.Lock()
	m.clients[toolID] = client
	m.mu.Unlock()
	return client, nil
}

func (m *StdioMCPManager) Remove(toolID string) {
	m.mu.Lock()
	client, ok := m.clients[toolID]
	if ok {
		delete(m.clients, toolID)
	}
	m.mu.Unlock()
	if ok && client != nil {
		_ = client.Close()
	}
}
