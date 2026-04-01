package cmd

import (
	"ai/agents/deepresearch"
	"ai/config"
	"ai/pkg/logger"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

var (
	deepResearchCmd = &cobra.Command{
		Use:          "deepresearch",
		Short:        "deepresearch",
		Long:         "deepresearch",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			defer logger.Sync()
			config.Init()
			handler, addr, err := buildDeepResearchHTTPServer()
			if err != nil {
				logger.Fatal(err)
			}
			if err := http.ListenAndServe(addr, handler); err != nil {
				logger.Fatal(err)
			}
		},
	}
)

func buildDeepResearchHTTPServer() (http.Handler, string, error) {
	agt, err := deepresearch.NewAgent()
	if err != nil {
		return nil, "", err
	}
	handler, err := deepresearch.NewHTTPServer(agt)
	if err != nil {
		return nil, "", err
	}
	addr := ":9993"
	for _, cfg := range config.GetMainConfig().HostAgent.Agents {
		if strings.EqualFold(cfg.Name, "deepresearch") || strings.EqualFold(cfg.Name, "deep_researcher") {
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
	deepResearchCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.TRPCConfig, "config", "c",
		"./trpc_go.yaml", "(deprecated) trpc config file path")
	deepResearchCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.ConfigProvider, "config-provider", "p",
		"file", "config provider")
	deepResearchCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.MainConfigFilename, "main-config", "m",
		"config.yaml", "main config file path")
}
