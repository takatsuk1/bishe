package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"ai/pkg/logger"
)

type MCPToolConfig struct {
	Mode      string   `json:"mcp_mode"`
	ServerURL string   `json:"server_url"`
	ToolName  string   `json:"tool_name"`
	Command   string   `json:"command,omitempty"`
	Args      []string `json:"args,omitempty"`
}

type MCPTool struct {
	*BaseTool
	mcpClient *MCPClient
	config    MCPToolConfig
}

func NewMCPTool(name string, description string, parameters []ToolParameter, config MCPToolConfig) *MCPTool {
	return &MCPTool{
		BaseTool:  NewBaseTool(name, ToolTypeMCP, description, parameters),
		config:    config,
		mcpClient: nil,
	}
}

func NewMCPToolWithClient(name string, description string, parameters []ToolParameter, client *MCPClient, toolName string) *MCPTool {
	return &MCPTool{
		BaseTool:  NewBaseTool(name, ToolTypeMCP, description, parameters),
		mcpClient: client,
		config: MCPToolConfig{
			Mode:      client.Mode(),
			ServerURL: client.ServerURL(),
			Command:   client.Command(),
			Args:      client.Args(),
			ToolName:  toolName,
		},
	}
}

func (t *MCPTool) getOrCreateClient(ctx context.Context) (*MCPClient, error) {
	if t.mcpClient != nil {
		return t.mcpClient, nil
	}
	mode := strings.ToLower(strings.TrimSpace(t.config.Mode))
	if mode == "stdio" {
		command := strings.TrimSpace(t.config.Command)
		if command == "" {
			return nil, fmt.Errorf("MCP stdio command is empty")
		}
		command, args, err := EnsureUvToolInstalled(ctx, command, t.config.Args)
		if err != nil {
			return nil, err
		}
		client, err := ConnectMCPStdio(ctx, command, args)
		if err != nil {
			return nil, fmt.Errorf("connect to MCP stdio failed: %w", err)
		}
		t.mcpClient = client
		return client, nil
	}
	if t.config.ServerURL == "" {
		return nil, fmt.Errorf("MCP server URL is empty")
	}
	client, err := ConnectMCP(ctx, t.config.ServerURL)
	if err != nil {
		return nil, fmt.Errorf("connect to MCP server failed: %w", err)
	}
	t.mcpClient = client
	return client, nil
}

func (t *MCPTool) Execute(ctx context.Context, params map[string]any) (map[string]any, error) {
	start := time.Now()
	logger.Infof("[TRACE] MCPTool.Execute start name=%s tool=%s server=%s", t.info.Name, t.config.ToolName, t.serverLabel())

	if err := ValidateParameters(params, t.info.Parameters); err != nil {
		return nil, err
	}
	params = ApplyDefaults(params, t.info.Parameters)

	client, err := t.getOrCreateClient(ctx)
	if err != nil {
		return nil, err
	}

	toolName := resolveMCPToolName(t.info.Name, t.config.ToolName, params)
	callArgs := resolveMCPArguments(params)

	result, err := client.CallTool(ctx, toolName, callArgs)
	if err != nil {
		logger.Infof("[TRACE] MCPTool.Execute call_tool_failed dur=%s err=%v", time.Since(start), err)
		return nil, fmt.Errorf("call MCP tool failed: %w", err)
	}

	output := map[string]any{
		"content":   result.Content,
		"is_error":  result.IsError,
		"tool_name": toolName,
	}

	logger.Infof("[TRACE] MCPTool.Execute done name=%s dur=%s is_error=%v", t.info.Name, time.Since(start), result.IsError)

	if result.IsError {
		return output, fmt.Errorf("MCP tool returned error: %v", result.Content)
	}

	return output, nil
}

func (t *MCPTool) serverLabel() string {
	mode := strings.ToLower(strings.TrimSpace(t.config.Mode))
	if mode == "stdio" {
		label := strings.TrimSpace(t.config.Command)
		if label == "" {
			return "stdio"
		}
		return label
	}
	return strings.TrimSpace(t.config.ServerURL)
}

func resolveMCPToolName(defaultToolName string, configuredToolName string, params map[string]any) string {
	if dynamicToolName, ok := params["tool_name"].(string); ok {
		dynamicToolName = strings.TrimSpace(dynamicToolName)
		if dynamicToolName != "" {
			return dynamicToolName
		}
	}

	configuredToolName = strings.TrimSpace(configuredToolName)
	if configuredToolName != "" && !strings.EqualFold(configuredToolName, "auto") {
		return configuredToolName
	}

	return strings.TrimSpace(defaultToolName)
}

func resolveMCPArguments(params map[string]any) map[string]any {
	if args, ok := params["arguments"].(map[string]any); ok && args != nil {
		return args
	}

	out := make(map[string]any, len(params))
	for k, v := range params {
		if k == "tool_name" || k == "arguments" {
			continue
		}
		out[k] = v
	}
	return out
}

func (t *MCPTool) Close() error {
	if t.mcpClient != nil {
		return t.mcpClient.Close()
	}
	return nil
}

type MCPToolDefinition struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  []ToolParameter `json:"parameters"`
	Config      MCPToolConfig   `json:"config"`
}

func NewMCPToolFromDefinition(def MCPToolDefinition) *MCPTool {
	return NewMCPTool(def.Name, def.Description, def.Parameters, def.Config)
}

func (t *MCPTool) ToDefinition(id string) MCPToolDefinition {
	return MCPToolDefinition{
		ID:          id,
		Name:        t.info.Name,
		Description: t.info.Description,
		Parameters:  t.info.Parameters,
		Config:      t.config,
	}
}

type MCPToolDiscovery struct {
	client *MCPClient
}

func NewMCPToolDiscovery(serverURL string) (*MCPToolDiscovery, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := ConnectMCP(ctx, serverURL)
	if err != nil {
		return nil, err
	}
	return &MCPToolDiscovery{client: client}, nil
}

func (d *MCPToolDiscovery) Close() error {
	if d.client != nil {
		return d.client.Close()
	}
	return nil
}

func (d *MCPToolDiscovery) ListTools(ctx context.Context) ([]ToolInfo, error) {
	remoteTools, err := d.client.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ToolInfo, 0, len(remoteTools))
	for _, rt := range remoteTools {
		out = append(out, ToolInfo{
			Name:        rt.Name,
			Type:        ToolTypeMCP,
			Description: rt.Description,
			Parameters:  convertMCPParameters(rt.InputSchema),
		})
	}
	return out, nil
}

func convertMCPParameters(inputSchema map[string]any) []ToolParameter {
	if len(inputSchema) == 0 {
		return nil
	}
	properties, _ := inputSchema["properties"].(map[string]any)
	requiredSet := map[string]struct{}{}
	if reqList, ok := inputSchema["required"].([]any); ok {
		for _, item := range reqList {
			if name, ok := item.(string); ok {
				requiredSet[name] = struct{}{}
			}
		}
	}

	out := make([]ToolParameter, 0, len(properties))
	for name, raw := range properties {
		prop, _ := raw.(map[string]any)
		param := ToolParameter{
			Name:        name,
			Type:        ParamTypeString,
			Description: getSchemaString(prop, "description", ""),
		}
		if _, ok := requiredSet[name]; ok {
			param.Required = true
		}
		if typeName := getSchemaString(prop, "type", "string"); typeName != "" {
			param.Type = mapSchemaType(typeName)
		}
		if def, ok := prop["default"]; ok {
			param.Default = def
		}
		if enumRaw, ok := prop["enum"].([]any); ok && len(enumRaw) > 0 {
			param.Enum = enumRaw
		}
		out = append(out, param)
	}
	return out
}

func mapSchemaType(schemaType string) ParameterType {
	switch strings.ToLower(strings.TrimSpace(schemaType)) {
	case "string":
		return ParamTypeString
	case "number", "integer":
		return ParamTypeNumber
	case "boolean":
		return ParamTypeBoolean
	case "array":
		return ParamTypeArray
	case "object":
		return ParamTypeObject
	default:
		return ParamTypeString
	}
}

func getSchemaString(m map[string]any, key string, fallback string) string {
	if m == nil {
		return fallback
	}
	raw, ok := m[key]
	if !ok {
		return fallback
	}
	if v, ok := raw.(string); ok {
		v = strings.TrimSpace(v)
		if v != "" {
			return v
		}
	}
	return fallback
}

func WrapMCPClientAsTool(client *MCPClient, toolName string, description string, parameters []ToolParameter) Tool {
	return NewMCPToolWithClient(toolName, description, parameters, client, toolName)
}

func ConvertMCPToolsToRegistry(client *MCPClient, toolNames []string, descriptions map[string]string, paramsMap map[string][]ToolParameter) *ToolRegistry {
	registry := NewToolRegistry()
	for _, name := range toolNames {
		desc := descriptions[name]
		if desc == "" {
			desc = fmt.Sprintf("MCP tool: %s", name)
		}
		params := paramsMap[name]
		tool := NewMCPToolWithClient(name, desc, params, client, name)
		registry.Register(tool)
	}
	return registry
}

func SanitizeToolName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, " ", "_")
	name = strings.ReplaceAll(name, "-", "_")
	name = strings.ToLower(name)
	return name
}
