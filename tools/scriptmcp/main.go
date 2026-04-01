package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const scriptMCPServerName = "script-mcp"
const scriptMCPVersion = "1.0.0"

func main() {
	srv := server.NewMCPServer(scriptMCPServerName, scriptMCPVersion)
	srv.AddTool(mcp.Tool{
		Name:        "script_exec",
		Description: "Execute local scripts or commands",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Script path or command",
				},
				"args": map[string]any{
					"type":        "array",
					"description": "Command arguments",
				},
				"interpreter": map[string]any{
					"type":        "string",
					"description": "Optional interpreter, e.g. powershell or python",
				},
				"cwd": map[string]any{
					"type":        "string",
					"description": "Working directory",
				},
				"timeout_sec": map[string]any{
					"type":        "number",
					"description": "Execution timeout in seconds, default 60",
				},
			},
			Required: []string{"path"},
		},
	}, func(arguments map[string]interface{}) (*mcp.CallToolResult, error) {
		return handleScriptExec(arguments)
	})

	if err := server.ServeStdio(srv); err != nil {
		panic(err)
	}
}

func handleScriptExec(arguments map[string]interface{}) (*mcp.CallToolResult, error) {
	path, _ := arguments["path"].(string)
	path = strings.TrimSpace(path)
	if path == "" {
		return mcp.NewToolResultError("path is required"), nil
	}

	args := parseArgs(arguments["args"])
	interpreter, _ := arguments["interpreter"].(string)
	interpreter = strings.TrimSpace(interpreter)
	cwd, _ := arguments["cwd"].(string)
	cwd = strings.TrimSpace(cwd)
	timeoutSec := parseTimeout(arguments["timeout_sec"], 60)

	command, commandArgs := buildCommand(path, args, interpreter)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, command, commandArgs...)
	if cwd != "" {
		cmd.Dir = cwd
	}

	stdout, err := cmd.Output()
	exitCode := 0
	stderr := ""
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
			stderr = strings.TrimSpace(string(ee.Stderr))
		} else {
			return mcp.NewToolResultError(err.Error()), nil
		}
	}

	payload := map[string]any{
		"command":   command,
		"args":      commandArgs,
		"cwd":       cwd,
		"exit_code": exitCode,
		"stdout":    strings.TrimSpace(string(stdout)),
		"stderr":    stderr,
	}

	data, marshalErr := json.MarshalIndent(payload, "", "  ")
	if marshalErr != nil {
		return mcp.NewToolResultError(marshalErr.Error()), nil
	}

	if exitCode != 0 {
		return mcp.NewToolResultError(string(data)), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func buildCommand(path string, args []string, interpreter string) (string, []string) {
	if interpreter != "" {
		outArgs := make([]string, 0, len(args)+1)
		outArgs = append(outArgs, path)
		outArgs = append(outArgs, args...)
		return interpreter, outArgs
	}

	if runtime.GOOS == "windows" {
		lower := strings.ToLower(path)
		if strings.HasSuffix(lower, ".ps1") {
			outArgs := []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", path}
			outArgs = append(outArgs, args...)
			return "powershell", outArgs
		}
		if strings.HasSuffix(lower, ".bat") || strings.HasSuffix(lower, ".cmd") {
			line := path
			if len(args) > 0 {
				line = line + " " + strings.Join(args, " ")
			}
			return "cmd", []string{"/C", line}
		}
	}

	return path, args
}

func parseArgs(raw any) []string {
	switch v := raw.(type) {
	case []string:
		return append([]string{}, v...)
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

func parseTimeout(raw any, defaultValue int) int {
	switch v := raw.(type) {
	case int:
		if v > 0 {
			return v
		}
	case int32:
		if v > 0 {
			return int(v)
		}
	case int64:
		if v > 0 {
			return int(v)
		}
	case float64:
		if v > 0 {
			return int(v)
		}
	case float32:
		if v > 0 {
			return int(v)
		}
	}
	return defaultValue
}
