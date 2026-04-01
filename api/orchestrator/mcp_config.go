package orchestrator

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type mcpStdioServerConfig struct {
	Command string
	Args    []string
}

func getMCPMode(config map[string]any) string {
	mode := strings.ToLower(strings.TrimSpace(getStringMap(config, "mcp_mode", "")))
	if mode == "" {
		return "url"
	}
	return mode
}

func extractMCPStdioServer(config map[string]any) (string, mcpStdioServerConfig, error) {
	servers, err := parseMCPServers(config)
	if err != nil {
		return "", mcpStdioServerConfig{}, err
	}
	if len(servers) == 0 {
		return "", mcpStdioServerConfig{}, fmt.Errorf("mcpServers is empty")
	}

	serverName := strings.TrimSpace(getStringMap(config, "server_name", ""))
	if serverName == "" {
		serverName = strings.TrimSpace(getStringMap(config, "mcp_server", ""))
	}
	if serverName != "" {
		cfg, ok := servers[serverName]
		if !ok {
			return "", mcpStdioServerConfig{}, fmt.Errorf("mcp server %q not found", serverName)
		}
		return serverName, cfg, nil
	}

	keys := make([]string, 0, len(servers))
	for name := range servers {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	selected := keys[0]
	return selected, servers[selected], nil
}

func parseMCPServers(config map[string]any) (map[string]mcpStdioServerConfig, error) {
	if config == nil {
		return nil, fmt.Errorf("mcp config is empty")
	}

	if raw, ok := config["mcp_servers"]; ok {
		return parseMCPServersRaw(raw)
	}
	if raw, ok := config["mcpServers"]; ok {
		return parseMCPServersRaw(raw)
	}
	if raw, ok := config["mcp_servers_json"]; ok {
		return parseMCPServersRaw(raw)
	}
	if raw, ok := config["mcp_config_json"]; ok {
		return parseMCPServersRaw(raw)
	}

	command := strings.TrimSpace(getStringMap(config, "command", ""))
	if command != "" {
		return map[string]mcpStdioServerConfig{
			"default": {
				Command: command,
				Args:    parseStringSlice(config["args"]),
			},
		}, nil
	}

	return nil, fmt.Errorf("mcpServers config is required")
}

func parseMCPServersRaw(raw any) (map[string]mcpStdioServerConfig, error) {
	switch v := raw.(type) {
	case string:
		text := strings.TrimSpace(v)
		if text == "" {
			return nil, fmt.Errorf("mcpServers json is empty")
		}
		var parsed any
		if err := json.Unmarshal([]byte(text), &parsed); err != nil {
			return nil, fmt.Errorf("parse mcpServers json: %w", err)
		}
		return parseMCPServersRaw(parsed)
	case map[string]any:
		if nested, ok := v["mcpServers"]; ok {
			return parseMCPServersRaw(nested)
		}
		if nested, ok := v["mcp_servers"]; ok {
			return parseMCPServersRaw(nested)
		}

		servers := make(map[string]mcpStdioServerConfig, len(v))
		for name, rawCfg := range v {
			cfgMap, ok := rawCfg.(map[string]any)
			if !ok {
				continue
			}
			command := strings.TrimSpace(getStringMap(cfgMap, "command", ""))
			args := parseStringSlice(cfgMap["args"])
			if command == "" {
				continue
			}
			servers[name] = mcpStdioServerConfig{Command: command, Args: args}
		}
		if len(servers) == 0 {
			return nil, fmt.Errorf("mcpServers has no valid entries")
		}
		return servers, nil
	default:
		return nil, fmt.Errorf("unsupported mcpServers format")
	}
}

func parseStringSlice(raw any) []string {
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			out = append(out, fmt.Sprintf("%v", item))
		}
		return out
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return nil
		}
		return []string{trimmed}
	default:
		return nil
	}
}
