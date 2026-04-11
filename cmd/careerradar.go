package cmd

import (
	"ai/agents/careerradar"
	"ai/config"
	"ai/pkg/logger"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

var (
	careerRadarCmd = &cobra.Command{
		Use:          "careerradar",
		Short:        "careerradar",
		Long:         "careerradar",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			defer logger.Sync()
			config.Init()
			handler, addr, err := buildCareerRadarHTTPServer()
			if err != nil {
				logger.Fatal(err)
			}
			if err := http.ListenAndServe(addr, handler); err != nil {
				logger.Fatal(err)
			}
		},
	}
)

func buildCareerRadarHTTPServer() (http.Handler, string, error) {
	agt, err := careerradar.NewAgent()
	if err != nil {
		return nil, "", err
	}
	handler, err := careerradar.NewHTTPServer(agt)
	if err != nil {
		return nil, "", err
	}
	addr := ":9997"
	for _, cfg := range config.GetMainConfig().HostAgent.Agents {
		if strings.EqualFold(cfg.Name, "careerradar") {
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
	careerRadarCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.ConfigProvider, "config-provider", "p",
		"file", "config provider")
	careerRadarCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.MainConfigFilename, "main-config", "m",
		"config.yaml", "main config file path")
}
