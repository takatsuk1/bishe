package cmd

import (
	"ai/config"
	"ai/pkg/logger"
	"ai/pkg/storage"
	"context"
	"fmt"
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

	for _, ag := range cfg.OpenAIConnector.Agents {
		name := strings.ToLower(strings.TrimSpace(ag.Name))
		if name == "" || builtin[name] {
			continue
		}
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
		mainGo := filepath.Join(agentDir, "cmd", "main.go")
		if _, err := os.Stat(mainGo); err != nil {
			logger.Warnf("skip auto-start user agent %s: %s not found", ag.Name, mainGo)
			continue
		}

		cmd := exec.Command("go", "run", "./cmd", "--port", strconv.Itoa(port), "--main-config", mainConfigPath)
		cmd.Dir = agentDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start user agent %s on port %d: %w", ag.Name, port, err)
		}
		go func(agentName string, c *exec.Cmd, st *storage.MySQLStorage) {
			if waitErr := c.Wait(); waitErr != nil {
				logger.Warnf("user agent %s exited: %v", agentName, waitErr)
			}
			if st != nil {
				_ = st.UpdateAgentStatus(context.Background(), agentName, storage.AgentStatusStopped, 0, 0)
			}
		}(ag.Name, cmd, mysqlStorage)

		logger.Infof("user agent %s listening on %s (pid=%d)", ag.Name, serverURL, cmd.Process.Pid)
		logger.Infof("Waiting for %s agent card to be ready...", ag.Name)
		if err := waitForAgentCard(serverURL, 15*time.Second); err != nil {
			return err
		}
		if mysqlStorage != nil {
			if err := mysqlStorage.UpdateAgentStatus(context.Background(), ag.Name, storage.AgentStatusPublished, port, cmd.Process.Pid); err != nil {
				logger.Warnf("user agent %s db sync failed: %v", ag.Name, err)
			}
		}
	}
	return nil
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
