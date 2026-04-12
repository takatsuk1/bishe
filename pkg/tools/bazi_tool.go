package tools

const (
	// BaziMCPToolID is the logical tool / stdio manager id (workflow tool_name, seed ToolID).
	BaziMCPToolID = "bazi"
	// BaziMCPToolMode selects dynamic MCP sub-tools via params["tool_name"] (same idea as akshare-one-mcp "auto").
	BaziMCPToolMode = "auto"
	// BaziMCPServerName is the key in user-tool mcp_servers and server_name for stdio discovery.
	BaziMCPServerName = "Bazi"
)

// BaziMCPToolParameters is shared by the standalone MCP tool and WrapMCPClientAsTool after UI "Start MCP".
func BaziMCPToolParameters() []ToolParameter {
	return []ToolParameter{
		{Name: "tool_name", Type: ParamTypeString, Required: true, Description: "要调用的 Bazi MCP 子工具名"},
		{Name: "arguments", Type: ParamTypeObject, Required: false, Description: "Bazi MCP 子工具参数对象"},
		{Name: "query", Type: ParamTypeString, Required: false, Description: "兼容字段"},
	}
}

// BaziMCPToolNpxArgs returns npx argv for the published bazi-mcp package (after EnsureUvToolInstalled on "npx").
func BaziMCPToolNpxArgs() []string {
	return []string{"--yes", "bazi-mcp"}
}

// NewBaziMCPTool builds the default stdio Bazi MCP tool (same pattern as financehelper AkShare).
func NewBaziMCPTool() *MCPTool {
	return NewMCPTool(
		BaziMCPToolID,
		"本地 Bazi MCP 服务；由 Agent 自动选择具体八字子工具",
		BaziMCPToolParameters(),
		MCPToolConfig{
			Mode:     "stdio",
			Command:  "npx",
			Args:     BaziMCPToolNpxArgs(),
			ToolName: BaziMCPToolMode,
		},
	)
}

// WrapBaziStdioMCPClient wraps a stdio MCP client started from the Tool page (shared process).
func WrapBaziStdioMCPClient(client *MCPClient) Tool {
	return WrapMCPClientAsTool(
		client,
		BaziMCPToolID,
		"通过本地 Bazi MCP 获取八字信息",
		BaziMCPToolParameters(),
	)
}

// BaziMCPUserToolConfig is the persisted user-tool shape for seeding / orchestrator (stdio).
func BaziMCPUserToolConfig() map[string]any {
	args := BaziMCPToolNpxArgs()
	anyArgs := make([]any, len(args))
	for i, a := range args {
		anyArgs[i] = a
	}
	return map[string]any{
		"mcp_mode":    "stdio",
		"server_name": BaziMCPServerName,
		"tool_name":   BaziMCPToolMode,
		"mcp_servers": map[string]any{
			BaziMCPServerName: map[string]any{
				"command": "npx",
				"args":    anyArgs,
			},
		},
	}
}
