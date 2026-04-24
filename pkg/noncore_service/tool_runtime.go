package noncore_service

import (
	"fmt"
	"strings"
	"time"

	"ai/pkg/storage"
	"ai/pkg/tools"
)

func ConvertToToolsParameter(params []storage.ToolParameterDef) []tools.ToolParameter {
	result := make([]tools.ToolParameter, 0, len(params))
	for _, p := range params {
		result = append(result, tools.ToolParameter{
			Name:        p.Name,
			Type:        tools.ParameterType(p.Type),
			Required:    p.Required,
			Description: p.Description,
			Default:     p.Default,
			Enum:        p.Enum,
		})
	}
	return result
}

func BuildRuntimeTool(def storage.UserToolDefinition) (tools.Tool, error) {
	params := ConvertToToolsParameter(def.Parameters)
	switch tools.ToolType(def.ToolType) {
	case tools.ToolTypeHTTP:
		cfg := tools.HTTPToolConfig{
			Method:  getStringMap(def.Config, "method", "GET"),
			URL:     getStringMap(def.Config, "url", ""),
			Timeout: time.Duration(getIntMap(def.Config, "timeout", 30)) * time.Second,
		}
		if headers := getStringStringMap(def.Config, "headers"); len(headers) > 0 {
			cfg.Headers = headers
		}
		cfg.BodyTemplate = getStringMap(def.Config, "body_template", "")
		return tools.NewHTTPTool(def.Name, def.Description, params, cfg), nil

	case tools.ToolTypeMCP:
		mode := GetMCPMode(def.Config)
		cfg := tools.MCPToolConfig{
			Mode:     mode,
			ToolName: getStringMap(def.Config, "tool_name", ""),
		}
		if mode == "stdio" {
			_, serverCfg, err := ExtractMCPStdioServer(def.Config)
			if err != nil {
				return nil, err
			}
			command, args := tools.NormalizeStdioCommand(serverCfg.Command, serverCfg.Args)
			cfg.Command = command
			cfg.Args = args
		} else {
			cfg.ServerURL = getStringMap(def.Config, "server_url", "")
			if strings.TrimSpace(cfg.ServerURL) == "" {
				return nil, fmt.Errorf("mcp server_url is required")
			}
		}
		return tools.NewMCPTool(def.Name, def.Description, params, cfg), nil

	default:
		return nil, fmt.Errorf("unsupported tool type: %s", def.ToolType)
	}
}
