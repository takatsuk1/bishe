package cmd

import (
	"ai/agents/financehelper"
	"ai/config"
	"ai/pkg/logger"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

var (
	financeHelperCmd = &cobra.Command{
		Use:          "financehelper",
		Short:        "financehelper",
		Long:         "financehelper",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			defer logger.Sync()
			config.Init()
			handler, addr, err := buildFinanceHelperHTTPServer()
			if err != nil {
				logger.Fatal(err)
			}
			if err := http.ListenAndServe(addr, handler); err != nil {
				logger.Fatal(err)
			}
		},
	}
)

func buildFinanceHelperHTTPServer() (http.Handler, string, error) {
	agt, err := financehelper.NewAgent()
	if err != nil {
		return nil, "", err
	}
	handler, err := financehelper.NewHTTPServer(agt)
	if err != nil {
		return nil, "", err
	}
	addr := ":9995"
	for _, cfg := range config.GetMainConfig().HostAgent.Agents {
		if strings.EqualFold(cfg.Name, "financehelper") {
			u, parseErr := url.Parse(cfg.ServerURL)
			if parseErr == nil && u.Host != "" {
				addr = u.Host
			}
			break
		}
	}
	return handler, addr, nil
}

func init() {
	financeHelperCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.ConfigProvider, "config-provider", "p",
		"file", "config provider")
	financeHelperCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.MainConfigFilename, "main-config", "m",
		"config.yaml", "main config file path")
}
