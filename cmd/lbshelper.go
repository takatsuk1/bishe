package cmd

import (
	"ai/agents/lbshelper"
	"ai/config"
	"ai/pkg/logger"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

var (
	lbsHelperCmd = &cobra.Command{
		Use:          "lbshelper",
		Short:        "lbshelper",
		Long:         "lbshelper",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			defer logger.Sync()
			config.Init()
			handler, addr, err := buildLbsHelperHTTPServer()
			if err != nil {
				logger.Fatal(err)
			}
			if err := http.ListenAndServe(addr, handler); err != nil {
				logger.Fatal(err)
			}
		},
	}
)

func buildLbsHelperHTTPServer() (http.Handler, string, error) {
	agt, err := lbshelper.NewAgent()
	if err != nil {
		return nil, "", err
	}
	handler, err := lbshelper.NewHTTPServer(agt)
	if err != nil {
		return nil, "", err
	}
	addr := ":9992"
	for _, cfg := range config.GetMainConfig().HostAgent.Agents {
		if strings.EqualFold(cfg.Name, "lbshelper") {
			u, parseErr := url.Parse(cfg.ServerURL)
			if parseErr == nil && u.Host != "" {
				addr = u.Host
			}
			break
		}
	}
	return handler, addr, nil
}

// init
func init() {
	lbsHelperCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.TRPCConfig, "config", "c",
		"./trpc_go.yaml", "(deprecated) trpc config file path")
	lbsHelperCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.ConfigProvider, "config-provider", "p",
		"file", "config provider")
	lbsHelperCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.MainConfigFilename, "main-config", "m",
		"config.yaml", "main config file path")
}
