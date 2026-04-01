package cmd

import (
	"ai/agents/host"
	"ai/config"
	"ai/pkg/logger"
	"ai/pkg/storage"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

var (
	hostCmd = &cobra.Command{
		Use:          "host",
		Short:        "host",
		Long:         "host",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			defer logger.Sync()
			config.Init()
			handler, addr, err := buildHostHTTPServer()
			if err != nil {
				logger.Fatal(err)
			}
			if err := http.ListenAndServe(addr, handler); err != nil {
				logger.Fatal(err)
			}
		},
	}
)

func buildHostHTTPServer() (http.Handler, string, error) {
	mysqlStorage, err := storage.GetMySQLStorage()
	if err != nil {
		cfg := config.GetMainConfig()
		dsn := strings.TrimSpace(cfg.MySQL.DSN)
		if dsn == "" {
			logger.Warnf("MySQL DSN is empty, public services use in-memory mode: %v", err)
		} else {
			mysqlStorage, err = storage.InitMySQL(dsn)
			if err != nil {
				logger.Warnf("MySQL init failed, public services use in-memory mode: %v", err)
			} else {
				logger.Infof("MySQL storage initialized in host server")
			}
		}
	} else {
		logger.Infof("MySQL storage available, initializing with storage")
	}

	agt, err := host.NewAgent()
	if err != nil {
		return nil, "", err
	}
	h, err := host.NewHTTPServer(agt)
	if err != nil {
		return nil, "", err
	}

	publicHandler := buildPublicServicesHandler(mysqlStorage)
	composed := composeHostRoutes(h, publicHandler)
	addr := ":8080"
	for _, cfg := range config.GetMainConfig().HostAgent.Agents {
		if strings.EqualFold(cfg.Name, "host") {
			u, parseErr := url.Parse(cfg.ServerURL)
			if parseErr == nil && u.Host != "" {
				addr = u.Host
			}
			break
		}
	}
	return composed, addr, nil
}

func composeHostRoutes(hostHandler, publicHandler http.Handler) http.Handler {
	composed := http.NewServeMux()
	if publicHandler != nil {
		composed.Handle("/v1/orchestrator/", publicHandler)
		composed.Handle("/v1/monitor/", publicHandler)
		composed.Handle("/v1/auth/", publicHandler)
		composed.Handle("/v1/admin/", publicHandler)
	}
	composed.Handle("/", hostHandler)
	return composed
}

// init
func init() {
	hostCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.TRPCConfig, "config", "c",
		"./trpc_go.yaml", "(deprecated) trpc config file path")
	hostCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.ConfigProvider, "config-provider", "p",
		"file", "config provider")
	hostCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.MainConfigFilename, "main-config", "m",
		"config.yaml", "main config file path")
}
