package cmd

import (
	"ai/config"
	"ai/pkg/logger"
	"ai/pkg/storage"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var (
	allinoneCmd = &cobra.Command{
		Use:          "allinone",
		Short:        "",
		Long:         "all-in-one server",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			defer logger.Sync()
			// 初始化配置
			config.Init()
			cfg := config.GetMainConfig()
			if cfg == nil {
				logger.Fatal("main config is nil")
			}

			dsn := strings.TrimSpace(cfg.MySQL.DSN)
			if dsn != "" {
				if _, err := storage.InitMySQL(dsn); err != nil {
					logger.Fatalf("failed to init mysql in allinone startup: %v", err)
				}
				logger.Infof("allinone mysql initialized before agent startup")
			} else {
				logger.Warnf("allinone mysql dsn is empty; monitor features may be disabled")
			}
			projectRoot, _ := os.Getwd()

			type serverConfig struct {
				name    string
				addr    string
				handler http.Handler
			}

			errCh := make(chan error, 10)

			startServer := func(srv serverConfig) {
				go func() {
					logger.Infof("%s listening on %s", srv.name, srv.addr)
					errCh <- http.ListenAndServe(srv.addr, srv.handler)
				}()
			}

			if h, addr, err := buildDeepResearchHTTPServer(); err != nil {
				logger.Fatal(err)
			} else {
				startServer(serverConfig{name: "deepresearch", addr: addr, handler: h})
			}
			if h, addr, err := buildLbsHelperHTTPServer(); err != nil {
				logger.Fatal(err)
			} else {
				startServer(serverConfig{name: "lbshelper", addr: addr, handler: h})
			}
			if h, addr, err := buildURLReaderHTTPServer(); err != nil {
				logger.Fatal(err)
			} else {
				startServer(serverConfig{name: "urlreader", addr: addr, handler: h})
			}
			if h, addr, err := buildScheduleHelperHTTPServer(); err != nil {
				logger.Fatal(err)
			} else {
				startServer(serverConfig{name: "schedulehelper", addr: addr, handler: h})
			}
			if h, addr, err := buildFinanceHelperHTTPServer(); err != nil {
				logger.Fatal(err)
			} else {
				startServer(serverConfig{name: "financehelper", addr: addr, handler: h})
			}
			if h, addr, err := buildMemoReminderHTTPServer(); err != nil {
				logger.Fatal(err)
			} else {
				startServer(serverConfig{name: "memoreminder", addr: addr, handler: h})
			}

			for _, name := range []string{"deepresearch", "lbshelper", "urlreader", "schedulehelper", "financehelper", "memoreminder"} {
				serverURL := getAgentServerURLByName(cfg.HostAgent.Agents, name)
				if serverURL == "" {
					logger.Fatalf("host_agent.agents missing server_url for %s", name)
				}
				logger.Infof("Waiting for %s agent card to be ready...", name)
				if err := waitForAgentCard(serverURL, 10*time.Second); err != nil {
					logger.Fatal(err)
				}
			}

			if err := startConfiguredUserAgents(cfg, projectRoot); err != nil {
				logger.Fatal(err)
			}

			if h, addr, err := buildHostHTTPServer(); err != nil {
				logger.Fatal(err)
			} else {
				startServer(serverConfig{name: "host", addr: addr, handler: h})
			}
			if h, addr, err := buildOpenAIConnectorHTTPServer(); err != nil {
				logger.Fatal(err)
			} else {
				startServer(serverConfig{name: "openai_connector", addr: addr, handler: h})
			}
			// knowledgeChat is optional; if config contains it, it can be started via its own command.

			err := <-errCh
			_ = err
			logger.Fatal(err)
		},
	}
)

func startConfiguredUserAgents(cfg *config.MainConfig, projectRoot string) error {
	if cfg == nil {
		return nil
	}
	var mysqlStorage *storage.MySQLStorage
	if st, err := storage.GetMySQLStorage(); err == nil {
		mysqlStorage = st
	} else if dsn := strings.TrimSpace(cfg.MySQL.DSN); dsn != "" {
		if st, initErr := storage.InitMySQL(dsn); initErr != nil {
			logger.Warnf("user-agent auto-start db sync disabled: init mysql failed: %v", initErr)
		} else {
			mysqlStorage = st
		}
	}

	if strings.TrimSpace(projectRoot) == "" {
		cwd, _ := os.Getwd()
		projectRoot = cwd
	}
	mainConfigPath := strings.TrimSpace(config.CmdlineFlags.MainConfigFilename)
	if mainConfigPath == "" {
		mainConfigPath = "config.yaml"
	}
	if !filepath.IsAbs(mainConfigPath) {
		mainConfigPath = filepath.Join(projectRoot, mainConfigPath)
	}

	builtin := map[string]bool{
		"deepresearch":   true,
		"urlreader":      true,
		"lbshelper":      true,
		"schedulehelper": true,
		"financehelper":  true,
		"memoreminder":   true,
		"host":           true,
	}
	usedConfiguredPorts := map[int]string{}
	configuredUserAgents := map[string]bool{}

	startUserAgent := func(agentName string, serverURL string, port int, agentDir string) error {
		mainGo := filepath.Join(agentDir, "cmd", "main.go")
		if _, err := os.Stat(mainGo); err != nil {
			logger.Warnf("skip auto-start user agent %s: %s not found", agentName, mainGo)
			return nil
		}

		cmd := exec.Command("go", "run", "./cmd", "--port", strconv.Itoa(port), "--main-config", mainConfigPath)
		cmd.Dir = agentDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start user agent %s on port %d: %w", agentName, port, err)
		}
		go func(name string, c *exec.Cmd, st *storage.MySQLStorage) {
			if waitErr := c.Wait(); waitErr != nil {
				logger.Warnf("user agent %s exited: %v", name, waitErr)
			}
			if st != nil {
				_ = st.UpdateAgentStatus(context.Background(), name, storage.AgentStatusStopped, 0, 0)
			}
		}(agentName, cmd, mysqlStorage)

		logger.Infof("user agent %s listening on %s (pid=%d)", agentName, serverURL, cmd.Process.Pid)
		logger.Infof("Waiting for %s agent card to be ready...", agentName)
		if err := waitForAgentCard(serverURL, 15*time.Second); err != nil {
			return err
		}
		if mysqlStorage != nil {
			if err := mysqlStorage.UpdateAgentStatus(context.Background(), agentName, storage.AgentStatusPublished, port, cmd.Process.Pid); err != nil {
				logger.Warnf("user agent %s db sync failed: %v", agentName, err)
			}
		}
		return nil
	}

	for _, ag := range cfg.OpenAIConnector.Agents {
		name := strings.ToLower(strings.TrimSpace(ag.Name))
		if name == "" || builtin[name] {
			continue
		}
		configuredUserAgents[name] = true
		serverURL := strings.TrimSpace(ag.ServerURL)
		if serverURL == "" {
			logger.Warnf("skip auto-start user agent %s: empty server_url", ag.Name)
			continue
		}
		port, err := parsePortFromServerURL(serverURL)
		if err != nil {
			logger.Warnf("skip auto-start user agent %s: %v", ag.Name, err)
			continue
		}
		if existing, exists := usedConfiguredPorts[port]; exists {
			logger.Warnf("skip auto-start user agent %s: duplicate configured port %d already used by %s", ag.Name, port, existing)
			continue
		}
		usedConfiguredPorts[port] = ag.Name

		agentDir := filepath.Join(projectRoot, "agents", "user_agents", ag.Name)
		if err := startUserAgent(ag.Name, serverURL, port, agentDir); err != nil {
			return err
		}
	}

	if mysqlStorage != nil {
		dbAgents, err := mysqlStorage.ListUserAgents(context.Background(), "")
		if err != nil {
			logger.Warnf("load user agents from db for auto-start failed: %v", err)
			return nil
		}
		for _, def := range dbAgents {
			agentName := strings.TrimSpace(def.AgentID)
			nameKey := strings.ToLower(agentName)
			if agentName == "" || builtin[nameKey] || configuredUserAgents[nameKey] {
				continue
			}
			if def.Port <= 0 {
				allocatedPort, allocErr := findAvailableUserAgentPort(usedConfiguredPorts)
				if allocErr != nil {
					logger.Warnf("skip auto-start db user agent %s: invalid persisted port %d and allocate failed: %v", agentName, def.Port, allocErr)
					continue
				}
				logger.Infof("db user agent %s has invalid persisted port %d; assigned fallback port %d", agentName, def.Port, allocatedPort)
				def.Port = allocatedPort
			}
			if existing, exists := usedConfiguredPorts[def.Port]; exists {
				logger.Warnf("skip auto-start db user agent %s: port %d already used by %s", agentName, def.Port, existing)
				continue
			}
			usedConfiguredPorts[def.Port] = agentName

			agentDir := strings.TrimSpace(def.CodePath)
			if agentDir == "" {
				agentDir = filepath.Join(projectRoot, "agents", "user_agents", agentName)
			} else if !filepath.IsAbs(agentDir) {
				agentDir = filepath.Join(projectRoot, agentDir)
			}

			serverURL := fmt.Sprintf("http://127.0.0.1:%d", def.Port)
			logger.Infof("auto-start db user agent %s status=%s code_path=%s server_url=%s", agentName, def.Status, agentDir, serverURL)
			if err := startUserAgent(agentName, serverURL, def.Port, agentDir); err != nil {
				return err
			}
		}
	}
	return nil
}

func findAvailableUserAgentPort(usedConfiguredPorts map[int]string) (int, error) {
	const (
		basePort = 8200
		maxPort  = 8999
	)
	for port := basePort; port <= maxPort; port++ {
		if _, exists := usedConfiguredPorts[port]; exists {
			continue
		}
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			continue
		}
		_ = ln.Close()
		return port, nil
	}
	return 0, fmt.Errorf("no available user-agent port in range %d-%d", basePort, maxPort)
}

func parsePortFromServerURL(serverURL string) (int, error) {
	u, err := url.Parse(strings.TrimSpace(serverURL))
	if err != nil {
		return 0, fmt.Errorf("invalid server_url %q: %w", serverURL, err)
	}
	hostPort := u.Host
	if hostPort == "" {
		return 0, fmt.Errorf("invalid server_url %q: missing host", serverURL)
	}
	portStr := u.Port()
	if strings.TrimSpace(portStr) == "" {
		return 0, fmt.Errorf("invalid server_url %q: missing port", serverURL)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		return 0, fmt.Errorf("invalid port in server_url %q", serverURL)
	}
	return port, nil
}

func getAgentServerURLByName(agents []config.AgentConfig, name string) string {
	for _, agent := range agents {
		if strings.EqualFold(agent.Name, name) {
			return strings.TrimSpace(agent.ServerURL)
		}
	}
	return ""
}

func waitForAgentCard(serverURL string, timeout time.Duration) error {
	cardURL := strings.TrimRight(serverURL, "/") + "/.well-known/agent.json"
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 1200 * time.Millisecond}
	var lastErr error

	for time.Now().Before(deadline) {
		resp, err := client.Get(cardURL)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			lastErr = fmt.Errorf("unexpected status code: %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(200 * time.Millisecond)
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("timeout")
	}
	return fmt.Errorf("agent card not ready at %s: %w", cardURL, lastErr)
}

// init
func init() {
	allinoneCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.TRPCConfig, "config", "c",
		"./trpc_go.yaml", "(deprecated) trpc config file path")
	allinoneCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.ConfigProvider, "config-provider", "p",
		"file", "config provider")
	allinoneCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.MainConfigFilename, "main-config", "m",
		"config.yaml", "main config file path")
}
