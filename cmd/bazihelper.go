package cmd

import (
	"ai/agents/bazihelper"
	"ai/config"
	"ai/pkg/logger"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

var (
	baziHelperCmd = &cobra.Command{
		Use:          "bazihelper",
		Short:        "bazihelper",
		Long:         "bazihelper",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			defer logger.Sync()
			config.Init()
			handler, addr, err := buildBaziHelperHTTPServer()
			if err != nil {
				logger.Fatal(err)
			}
			if err := http.ListenAndServe(addr, handler); err != nil {
				logger.Fatal(err)
			}
		},
	}
)

func buildBaziHelperHTTPServer() (http.Handler, string, error) {
	agt, err := bazihelper.NewAgent()
	if err != nil {
		return nil, "", err
	}
	handler, err := bazihelper.NewHTTPServer(agt)
	if err != nil {
		return nil, "", err
	}
	addr := ":9999"
	for _, cfg := range config.GetMainConfig().HostAgent.Agents {
		if strings.EqualFold(cfg.Name, "bazihelper") {
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
	baziHelperCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.ConfigProvider, "config-provider", "p",
		"file", "config provider")
	baziHelperCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.MainConfigFilename, "main-config", "m",
		"config.yaml", "main config file path")
}
