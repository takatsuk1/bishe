package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const jsonFileMCPServerName = "json-file-mcp"
const jsonFileMCPVersion = "1.0.0"

func main() {
	root := strings.TrimSpace(getFlagValue("--root", os.Args))

	srv := server.NewMCPServer(jsonFileMCPServerName, jsonFileMCPVersion)
	srv.AddTool(mcp.Tool{
		Name:        "json_file",
		Description: "Read or write local JSON files",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"action": map[string]any{
					"type":        "string",
					"description": "read or write",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "JSON file path",
				},
				"json": map[string]any{
					"type":        "object",
					"description": "JSON content for write",
				},
			},
			Required: []string{"action", "path"},
		},
	}, func(arguments map[string]interface{}) (*mcp.CallToolResult, error) {
		return handleJSONFile(root, arguments)
	})

	if err := server.ServeStdio(srv); err != nil {
		panic(err)
	}
}

func getFlagValue(name string, args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func handleJSONFile(root string, arguments map[string]interface{}) (*mcp.CallToolResult, error) {
	action, _ := arguments["action"].(string)
	path, _ := arguments["path"].(string)
	action = strings.ToLower(strings.TrimSpace(action))
	path = strings.TrimSpace(path)
	if action == "" || path == "" {
		return mcp.NewToolResultError("action and path are required"), nil
	}

	resolvedPath := resolvePath(root, path)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	switch action {
	case "read":
		payload, err := readJSON(ctx, resolvedPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonTextResult(map[string]any{
			"path": resolvedPath,
			"data": payload,
		})
	case "write":
		payload, err := extractJSONPayload(arguments["json"])
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := writeJSON(ctx, resolvedPath, payload); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonTextResult(map[string]any{
			"path":    resolvedPath,
			"written": true,
		})
	default:
		return mcp.NewToolResultError("action must be read or write"), nil
	}
}

func resolvePath(root, path string) string {
	clean := filepath.Clean(path)
	if root == "" || filepath.IsAbs(clean) {
		return clean
	}
	return filepath.Clean(filepath.Join(root, clean))
}

func readJSON(ctx context.Context, path string) (any, error) {
	_ = ctx
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var payload any
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func writeJSON(ctx context.Context, path string, payload any) error {
	_ = ctx
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func extractJSONPayload(raw any) (any, error) {
	if raw == nil {
		return nil, nil
	}
	switch v := raw.(type) {
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return nil, nil
		}
		var payload any
		if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
			return nil, err
		}
		return payload, nil
	default:
		return v, nil
	}
}

func jsonTextResult(payload any) (*mcp.CallToolResult, error) {
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}
