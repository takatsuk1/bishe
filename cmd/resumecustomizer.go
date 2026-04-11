package cmd

import (
	"ai/agents/resumecustomizer"
	"ai/config"
	"ai/pkg/logger"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

var (
	resumeCustomizerCmd = &cobra.Command{
		Use:          "resumecustomizer",
		Short:        "resumecustomizer",
		Long:         "resumecustomizer",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			defer logger.Sync()
			config.Init()
			handler, addr, err := buildResumeCustomizerHTTPServer()
			if err != nil {
				logger.Fatal(err)
			}
			if err := http.ListenAndServe(addr, handler); err != nil {
				logger.Fatal(err)
			}
		},
	}
)

func buildResumeCustomizerHTTPServer() (http.Handler, string, error) {
	agt, err := resumecustomizer.NewAgent()
	if err != nil {
		return nil, "", err
	}
	handler, err := resumecustomizer.NewHTTPServer(agt)
	if err != nil {
		return nil, "", err
	}
	addr := ":9996"
	for _, cfg := range config.GetMainConfig().HostAgent.Agents {
		if strings.EqualFold(cfg.Name, "resumecustomizer") {
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
	resumeCustomizerCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.ConfigProvider, "config-provider", "p",
		"file", "config provider")
	resumeCustomizerCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.MainConfigFilename, "main-config", "m",
		"config.yaml", "main config file path")
}
