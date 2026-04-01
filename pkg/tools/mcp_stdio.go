package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"ai/pkg/logger"
)

// NormalizeStdioCommand converts uvx commands to uv tool run for stdio usage.
func NormalizeStdioCommand(command string, args []string) (string, []string) {
	cmd := strings.TrimSpace(command)
	if strings.EqualFold(cmd, "uvx") {
		merged := make([]string, 0, len(args)+2)
		merged = append(merged, "tool", "run")
		merged = append(merged, args...)
		return "uv", merged
	}
	return cmd, args
}

// EnsureUvToolInstalled installs uv tools when using uv/uvx based stdio commands.
// It returns a normalized command/args for execution.
func EnsureUvToolInstalled(ctx context.Context, command string, args []string) (string, []string, error) {
	cmd, normalizedArgs := NormalizeStdioCommand(command, args)
	logger.Infof("[TRACE] mcp.uv.ensure start command=%q args=%v normalized=%q", command, args, cmd)
	if !strings.EqualFold(cmd, "uv") {
		logger.Infof("[TRACE] mcp.uv.ensure skip non-uv command=%q", cmd)
		return cmd, normalizedArgs, nil
	}
	if len(normalizedArgs) < 3 || normalizedArgs[0] != "tool" || normalizedArgs[1] != "run" {
		logger.Infof("[TRACE] mcp.uv.ensure skip non-tool-run args=%v", normalizedArgs)
		return cmd, normalizedArgs, nil
	}
	if _, err := exec.LookPath("uv"); err != nil {
		logger.Warnf("[TRACE] mcp.uv.ensure uv not found, attempting auto-install")
		if installErr := installUv(ctx); installErr != nil {
			return cmd, normalizedArgs, fmt.Errorf("uv not found and auto-install failed: %w", installErr)
		}
		if uvPath := findUvPath(); uvPath == "" {
			return cmd, normalizedArgs, fmt.Errorf("uv install completed but uv not found in PATH")
		} else {
			cmd = uvPath
			logger.Infof("[TRACE] mcp.uv.ensure resolved uv path=%q", cmd)
		}
	} else if uvPath := findUvPath(); uvPath != "" {
		cmd = uvPath
		logger.Infof("[TRACE] mcp.uv.ensure resolved uv path=%q", cmd)
	}

	toolName := strings.TrimSpace(normalizedArgs[2])
	if toolName == "" {
		logger.Warnf("[TRACE] mcp.uv.ensure tool name empty args=%v", normalizedArgs)
		return cmd, normalizedArgs, fmt.Errorf("uv tool name is empty")
	}

	installCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	logger.Infof("[TRACE] mcp.uv.install start tool=%q", toolName)
	installCmd := exec.CommandContext(installCtx, cmd, "tool", "install", toolName)
	if output, err := installCmd.CombinedOutput(); err != nil {
		logger.Warnf("[TRACE] mcp.uv.install failed tool=%q err=%v", toolName, err)
		return cmd, normalizedArgs, fmt.Errorf("uv tool install failed: %v, output=%s", err, strings.TrimSpace(string(output)))
	}
	logger.Infof("[TRACE] mcp.uv.install done tool=%q", toolName)

	return cmd, normalizedArgs, nil
}

func installUv(ctx context.Context) error {
	installCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	if runtime.GOOS == "windows" {
		cmd := exec.CommandContext(installCtx, "powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", "irm https://astral.sh/uv/install.ps1 | iex")
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("windows uv install failed: %v, output=%s", err, strings.TrimSpace(string(output)))
		}
		return nil
	}

	cmd := exec.CommandContext(installCtx, "sh", "-c", "curl -LsSf https://astral.sh/uv/install.sh | sh")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("unix uv install failed: %v, output=%s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func findUvPath() string {
	if path, err := exec.LookPath("uv"); err == nil && path != "" {
		return path
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	if runtime.GOOS == "windows" {
		candidates := []string{
			filepath.Join(home, ".cargo", "bin", "uv.exe"),
			filepath.Join(home, ".local", "bin", "uv.exe"),
			filepath.Join(home, "AppData", "Local", "Programs", "uv", "uv.exe"),
		}
		for _, candidate := range candidates {
			if fileExists(candidate) {
				return candidate
			}
		}
		return ""
	}

	candidates := []string{
		filepath.Join(home, ".cargo", "bin", "uv"),
		filepath.Join(home, ".local", "bin", "uv"),
	}
	for _, candidate := range candidates {
		if fileExists(candidate) {
			return candidate
		}
	}
	return ""
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}
