package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ai/pkg/logger"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

type MCPClient struct {
	mu        sync.Mutex
	mode      string
	serverURL string
	command   string
	args      []string
	endpoint  string
	httpCli   *http.Client
	sseResp   *http.Response
	sseCancel context.CancelFunc
	connected bool
	stdioCli  *mcpclient.StdioMCPClient

	nextID  uint64
	pending map[string]chan rpcResponse
}

// ConnectMCP creates an MCP SSE client wrapper.
// It is lazy: it does not do network I/O until ListTools/CallTool is invoked.
func ConnectMCP(ctx context.Context, serverURL string) (*MCPClient, error) {
	_ = ctx
	serverURL = strings.TrimSpace(serverURL)
	if serverURL == "" {
		return nil, fmt.Errorf("serverURL is empty")
	}
	logger.Infof("[TRACE] mcp.Connect configured server=%q", serverURL)
	return &MCPClient{
		mode:      "sse",
		serverURL: serverURL,
		httpCli:   &http.Client{Timeout: 60 * time.Second},
		pending:   map[string]chan rpcResponse{},
	}, nil
}

// ConnectMCPStdio creates a stdio MCP client wrapper.
// It is lazy: it does not start the subprocess until ListTools/CallTool is invoked.
func ConnectMCPStdio(ctx context.Context, command string, args []string) (*MCPClient, error) {
	_ = ctx
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, fmt.Errorf("command is empty")
	}
	logger.Infof("[TRACE] mcp.Connect configured stdio command=%q args=%v", command, args)
	return &MCPClient{
		mode:    "stdio",
		command: command,
		args:    append([]string{}, args...),
		pending: map[string]chan rpcResponse{},
	}, nil
}

func (c *MCPClient) ServerURL() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.serverURL
}

func (c *MCPClient) Mode() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mode
}

func (c *MCPClient) Command() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.command
}

func (c *MCPClient) Args() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string{}, c.args...)
}

func (c *MCPClient) ensureConnected(ctx context.Context) error {
	if strings.EqualFold(c.Mode(), "stdio") {
		return c.ensureStdioConnected(ctx)
	}
	c.mu.Lock()
	if c.connected {
		c.mu.Unlock()
		return nil
	}
	serverURL := c.serverURL
	httpCli := c.httpCli
	c.mu.Unlock()

	logger.Infof("[TRACE] mcp.ensureConnected start server=%q", serverURL)

	sseCtx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(sseCtx, http.MethodGet, serverURL, nil)
	if err != nil {
		cancel()
		return fmt.Errorf("new sse request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")

	rsp, err := httpCli.Do(req)
	if err != nil {
		cancel()
		return fmt.Errorf("open sse stream: %w", err)
	}
	if rsp.StatusCode < 200 || rsp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(rsp.Body, 8<<10))
		_ = rsp.Body.Close()
		cancel()
		return fmt.Errorf("open sse stream: status=%s body=%q", rsp.Status, strings.TrimSpace(string(body)))
	}

	endpoint, err := waitEndpoint(rsp.Body, serverURL)
	if err != nil {
		_ = rsp.Body.Close()
		cancel()
		return err
	}

	c.mu.Lock()
	// If another goroutine connected first, keep the existing connection.
	if c.connected {
		c.mu.Unlock()
		_ = rsp.Body.Close()
		cancel()
		return nil
	}
	c.endpoint = endpoint
	c.sseResp = rsp
	c.sseCancel = cancel
	c.connected = true
	c.mu.Unlock()

	go c.readLoop(sseCtx, rsp.Body)
	// Best-effort reconnect (same cadence as reference)
	go c.reconnectLoop(context.Background())

	// MCP requires initialize handshake.
	var initOut any
	if err := c.rpc(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"clientInfo": map[string]any{
			"name":    "go-client",
			"version": "1.0.0",
		},
	}, &initOut); err != nil {
		_ = c.Close()
		return fmt.Errorf("mcp initialize: %w", err)
	}

	logger.Infof("[TRACE] mcp.ensureConnected done server=%q endpoint=%q", serverURL, endpoint)
	return nil
}

func (c *MCPClient) ensureStdioConnected(ctx context.Context) error {
	c.mu.Lock()
	if c.stdioCli != nil {
		c.mu.Unlock()
		return nil
	}
	command := c.command
	args := append([]string{}, c.args...)
	c.mu.Unlock()

	logger.Infof("[TRACE] mcp.stdio.start command=%q args=%v", command, args)

	cli, err := mcpclient.NewStdioMCPClient(command, args...)
	if err != nil {
		logger.Warnf("[TRACE] mcp.stdio.start failed err=%v", err)
		return fmt.Errorf("start stdio mcp: %w", err)
	}

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "go-client",
		Version: "1.0.0",
	}
	if _, err := cli.Initialize(ctx, initReq); err != nil {
		_ = cli.Close()
		logger.Warnf("[TRACE] mcp.stdio.init failed err=%v", err)
		return fmt.Errorf("mcp initialize: %w", err)
	}

	c.mu.Lock()
	if c.stdioCli != nil {
		c.mu.Unlock()
		_ = cli.Close()
		return nil
	}
	c.stdioCli = cli
	c.mu.Unlock()
	logger.Infof("[TRACE] mcp.stdio.ready command=%q", command)
	return nil
}

func (c *MCPClient) reconnectLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = c.reconnectOnce(context.Background())
		}
	}
}

func (c *MCPClient) reconnectOnce(ctx context.Context) error {
	_ = ctx
	// Keep this conservative: if the current SSE stream is still alive, do nothing.
	c.mu.Lock()
	connected := c.connected
	resp := c.sseResp
	c.mu.Unlock()
	if connected && resp != nil {
		return nil
	}
	// If disconnected, next call will re-connect lazily.
	return nil
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type CallToolResult struct {
	Content any  `json:"content"`
	IsError bool `json:"isError,omitempty"`
}

type ListToolsResult struct {
	Tools []RemoteToolInfo `json:"tools"`
}

type RemoteToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema,omitempty"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

func (c *MCPClient) CallTool(ctx context.Context, name string, arguments map[string]any) (*CallToolResult, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("tool name is empty")
	}
	if err := c.ensureConnected(ctx); err != nil {
		return nil, err
	}
	if strings.EqualFold(c.Mode(), "stdio") {
		c.mu.Lock()
		cli := c.stdioCli
		c.mu.Unlock()
		if cli == nil {
			return nil, fmt.Errorf("stdio client is not initialized")
		}
		req := mcp.CallToolRequest{}
		req.Params.Name = name
		req.Params.Arguments = arguments
		result, err := cli.CallTool(ctx, req)
		if err != nil {
			return nil, err
		}
		return &CallToolResult{Content: result.Content, IsError: result.IsError}, nil
	}
	var out CallToolResult
	if err := c.rpc(ctx, "tools/call", map[string]any{"name": name, "arguments": arguments}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *MCPClient) ListTools(ctx context.Context) ([]RemoteToolInfo, error) {
	if err := c.ensureConnected(ctx); err != nil {
		return nil, err
	}
	if strings.EqualFold(c.Mode(), "stdio") {
		c.mu.Lock()
		cli := c.stdioCli
		c.mu.Unlock()
		if cli == nil {
			return nil, fmt.Errorf("stdio client is not initialized")
		}
		result, err := cli.ListTools(ctx, mcp.ListToolsRequest{})
		if err != nil {
			return nil, err
		}
		out := make([]RemoteToolInfo, 0, len(result.Tools))
		for _, toolInfo := range result.Tools {
			out = append(out, RemoteToolInfo{
				Name:        toolInfo.Name,
				Description: toolInfo.Description,
				InputSchema: toolInputSchemaToMap(toolInfo.InputSchema),
			})
		}
		return out, nil
	}
	var out ListToolsResult
	if err := c.rpc(ctx, "tools/list", map[string]any{}, &out); err != nil {
		return nil, err
	}
	return out.Tools, nil
}

func (c *MCPClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stdioCli != nil {
		_ = c.stdioCli.Close()
		c.stdioCli = nil
	}
	if c.sseCancel != nil {
		c.sseCancel()
		c.sseCancel = nil
	}
	if c.sseResp != nil {
		_ = c.sseResp.Body.Close()
		c.sseResp = nil
	}
	c.connected = false
	c.endpoint = ""
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	return nil
}

func waitEndpoint(body io.Reader, serverURL string) (string, error) {
	reader := bufio.NewReader(body)
	for {
		event, data, err := readSSEEvent(reader)
		if err != nil {
			return "", fmt.Errorf("read sse endpoint: %w", err)
		}
		if event != "endpoint" {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "" {
			continue
		}
		base, err := url.Parse(serverURL)
		if err != nil {
			return "", fmt.Errorf("parse serverURL: %w", err)
		}
		ref, err := url.Parse(data)
		if err != nil {
			return "", fmt.Errorf("parse endpoint: %w", err)
		}
		return base.ResolveReference(ref).String(), nil
	}
}

func readSSEEvent(reader *bufio.Reader) (eventName string, data string, err error) {
	var dataLines []string
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			return "", "", readErr
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			// end of event
			return eventName, strings.Join(dataLines, "\n"), nil
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			continue
		}
	}
}

func toolInputSchemaToMap(schema mcp.ToolInputSchema) map[string]any {
	data, err := json.Marshal(schema)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return out
}

func (c *MCPClient) readLoop(ctx context.Context, body io.Reader) {
	reader := bufio.NewReader(body)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		event, data, err := readSSEEvent(reader)
		if err != nil {
			c.mu.Lock()
			c.connected = false
			c.mu.Unlock()
			return
		}
		_ = event
		data = strings.TrimSpace(data)
		if data == "" {
			continue
		}
		if !strings.HasPrefix(data, "{") {
			continue
		}
		var msg rpcResponse
		if err := json.Unmarshal([]byte(data), &msg); err != nil {
			continue
		}
		idStr := fmt.Sprintf("%v", msg.ID)
		c.mu.Lock()
		ch, ok := c.pending[idStr]
		c.mu.Unlock()
		if ok {
			select {
			case ch <- msg:
			default:
			}
		}
	}
}

func (c *MCPClient) rpc(ctx context.Context, method string, params any, out any) error {
	method = strings.TrimSpace(method)
	if method == "" {
		return fmt.Errorf("mcp rpc method is empty")
	}

	c.mu.Lock()
	endpoint := c.endpoint
	httpCli := c.httpCli
	c.mu.Unlock()
	if endpoint == "" {
		return fmt.Errorf("mcp endpoint is empty")
	}

	id := atomic.AddUint64(&c.nextID, 1)
	idStr := fmt.Sprintf("%d", id)
	respCh := make(chan rpcResponse, 1)

	c.mu.Lock()
	c.pending[idStr] = respCh
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, idStr)
		c.mu.Unlock()
	}()

	reqBody := rpcRequest{JSONRPC: "2.0", ID: idStr, Method: method, Params: params}
	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal rpc request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(jsonBytes)))
	if err != nil {
		return fmt.Errorf("new rpc http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpRsp, err := httpCli.Do(httpReq)
	if err != nil {
		return fmt.Errorf("rpc http do: %w", err)
	}
	_ = httpRsp.Body.Close()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case resp, ok := <-respCh:
		if !ok {
			return fmt.Errorf("mcp rpc response channel closed")
		}
		if resp.Error != nil {
			return fmt.Errorf("mcp rpc error: code=%d message=%s", resp.Error.Code, resp.Error.Message)
		}
		if out == nil {
			return nil
		}
		if len(resp.Result) == 0 {
			return fmt.Errorf("mcp rpc empty result")
		}
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return fmt.Errorf("unmarshal rpc result: %w", err)
		}
		return nil
	}
}
