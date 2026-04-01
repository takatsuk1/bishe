package tools

import (
	"context"
	"fmt"
	"strings"
)

const (
	AMapMCPToolName = "amap_mcp"
	AMapMCPToolMode = "auto"
)

func NewAMapTool(ctx context.Context, serverURL string) (Tool, error) {
	client, err := ConnectMCP(ctx, strings.TrimSpace(serverURL))
	if err != nil {
		return nil, fmt.Errorf("connect amap MCP: %w", err)
	}

	params := []ToolParameter{
		{
			Name:        "tool_name",
			Type:        ParamTypeString,
			Required:    true,
			Description: "AMap MCP sub-tool name, e.g. maps_text_search",
		},
		{
			Name:        "arguments",
			Type:        ParamTypeObject,
			Required:    false,
			Description: "arguments object for selected AMap MCP sub-tool",
		},
		{
			Name:        "query",
			Type:        ParamTypeString,
			Required:    false,
			Description: "compat fallback input",
		},
	}

	return NewMCPToolWithClient(
		AMapMCPToolName,
		"AMap MCP meta tool, sub-tool chosen by agent",
		params,
		client,
		AMapMCPToolMode,
	), nil
}
