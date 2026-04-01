//go:build reference
// +build reference

package tools

import (
	"context"
	"fmt"
	"sync"
	"time"

	einomcp "github.com/cloudwego/eino-ext/components/tool/mcp"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"trpc.group/trpc-go/trpc-go/log"
)

type ToolWrapper struct {
	Tool tool.BaseTool
}

func (t *ToolWrapper) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return t.Tool.Info(ctx)
}

func (t *ToolWrapper) InvokableRun(ctx context.Context, argumentsInJSON string,
	opts ...tool.Option) (string, error) {
	invokableTool, ok := t.Tool.(tool.InvokableTool)
	if !ok {
		return "", fmt.Errorf("invalid tool")
	}

	result, err := invokableTool.InvokableRun(ctx, argumentsInJSON, opts...)
	if err != nil {
		log.ErrorContextf(ctx, "failed to exec tool, error: %v", err)
		return fmt.Sprintf("failed to exec tool, error: %v, please fix arguments", err), nil
	}
	return result, nil
}

type mcpClientWrapper struct {
	mu        sync.Mutex
	cli       *client.Client
	ServerURL string
}

func (m *mcpClientWrapper) Initialize(ctx context.Context,
	request mcp.InitializeRequest) (*mcp.InitializeResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cli.Initialize(ctx, request)
}

func (m *mcpClientWrapper) Ping(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cli.Ping(ctx)
}

func (m *mcpClientWrapper) ListResourcesByPage(ctx context.Context,
	request mcp.ListResourcesRequest) (*mcp.ListResourcesResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cli.ListResourcesByPage(ctx, request)
}

func (m *mcpClientWrapper) ListResources(ctx context.Context,
	request mcp.ListResourcesRequest) (*mcp.ListResourcesResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cli.ListResources(ctx, request)
}

func (m *mcpClientWrapper) ListResourceTemplatesByPage(ctx context.Context,
	request mcp.ListResourceTemplatesRequest) (*mcp.ListResourceTemplatesResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cli.ListResourceTemplatesByPage(ctx, request)
}

func (m *mcpClientWrapper) ListResourceTemplates(ctx context.Context,
	request mcp.ListResourceTemplatesRequest) (*mcp.ListResourceTemplatesResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cli.ListResourceTemplates(ctx, request)
}

func (m *mcpClientWrapper) ReadResource(ctx context.Context,
	request mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cli.ReadResource(ctx, request)
}

func (m *mcpClientWrapper) Subscribe(ctx context.Context, request mcp.SubscribeRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cli.Subscribe(ctx, request)
}

func (m *mcpClientWrapper) Unsubscribe(ctx context.Context, request mcp.UnsubscribeRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cli.Unsubscribe(ctx, request)
}

func (m *mcpClientWrapper) ListPromptsByPage(ctx context.Context,
	request mcp.ListPromptsRequest) (*mcp.ListPromptsResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cli.ListPromptsByPage(ctx, request)
}

func (m *mcpClientWrapper) ListPrompts(ctx context.Context,
	request mcp.ListPromptsRequest) (*mcp.ListPromptsResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cli.ListPrompts(ctx, request)
}

func (m *mcpClientWrapper) GetPrompt(ctx context.Context, request mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cli.GetPrompt(ctx, request)
}

func (m *mcpClientWrapper) ListToolsByPage(ctx context.Context,
	request mcp.ListToolsRequest) (*mcp.ListToolsResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cli.ListToolsByPage(ctx, request)
}

func (m *mcpClientWrapper) ListTools(ctx context.Context, request mcp.ListToolsRequest) (*mcp.ListToolsResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cli.ListTools(ctx, request)
}

func (m *mcpClientWrapper) CallTool(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cli.CallTool(ctx, request)
}

func (m *mcpClientWrapper) SetLevel(ctx context.Context, request mcp.SetLevelRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cli.SetLevel(ctx, request)
}

func (m *mcpClientWrapper) Complete(ctx context.Context, request mcp.CompleteRequest) (*mcp.CompleteResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cli.Complete(ctx, request)
}

func (m *mcpClientWrapper) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cli.Close()
}

func (m *mcpClientWrapper) OnNotification(handler func(notification mcp.JSONRPCNotification)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cli.OnNotification(handler)
}

func (m *mcpClientWrapper) Connect(ctx context.Context) error {
	cli, err := connect(ctx, m.ServerURL)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cli = cli

	go func() {
		ticker := time.NewTicker(time.Minute * 5)
		defer ticker.Stop()
		running := true
		for running {
			select {
			case <-ctx.Done():
				running = false
			case <-ticker.C:
				m.reconnect(ctx)
			}
		}
	}()
	return nil
}

func connect(ctx context.Context, serverURL string) (*client.Client, error) {
	log.InfoContextf(ctx, "try to connect: %s", serverURL)
	cli, err := client.NewSSEMCPClient(serverURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}
	err = cli.Start(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to restart: %w", err)
	}
	initRequest := mcp.InitializeRequest{}
	initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initRequest.Params.ClientInfo = mcp.Implementation{
		Name:    "go-client",
		Version: "1.0.0",
	}
	_, err = cli.Initialize(ctx, initRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize: %w", err)
	}
	return cli, nil
}

func (m *mcpClientWrapper) reconnect(ctx context.Context) {
	cli, err := connect(ctx, m.ServerURL)
	if err != nil {
		log.ErrorContextf(ctx, "failed to reconnect, err %v", err)
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cli != nil {
		m.cli.Close()
	}
	m.cli = cli
	return
}

func ConnectMCP(ctx context.Context, serverURL string) ([]tool.BaseTool, error) {
	mcpClient := &mcpClientWrapper{ServerURL: serverURL}
	if err := mcpClient.Connect(ctx); err != nil {
		return nil, fmt.Errorf("failed to connect mcp server, %w", err)
	}
	mcpTools, err := einomcp.GetTools(ctx, &einomcp.Config{Cli: mcpClient})
	if err != nil {
		return nil, fmt.Errorf("failed to get tools: %w", err)
	}
	var allTools []tool.BaseTool
	for _, v := range mcpTools {
		allTools = append(allTools, &ToolWrapper{Tool: v})
	}
	return allTools, nil
}
