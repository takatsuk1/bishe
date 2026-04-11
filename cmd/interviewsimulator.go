package cmd

import (
	"ai/agents/interviewsimulator"
	"ai/config"
	"ai/pkg/logger"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

var (
	interviewSimulatorCmd = &cobra.Command{
		Use:          "interviewsimulator",
		Short:        "interviewsimulator",
		Long:         "interviewsimulator",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			defer logger.Sync()
			config.Init()
			handler, addr, err := buildInterviewSimulatorHTTPServer()
			if err != nil {
				logger.Fatal(err)
			}
			if err := http.ListenAndServe(addr, handler); err != nil {
				logger.Fatal(err)
			}
		},
	}
)

func buildInterviewSimulatorHTTPServer() (http.Handler, string, error) {
	agt, err := interviewsimulator.NewAgent()
	if err != nil {
		return nil, "", err
	}
	handler, err := interviewsimulator.NewHTTPServer(agt)
	if err != nil {
		return nil, "", err
	}
	addr := ":9998"
	for _, cfg := range config.GetMainConfig().HostAgent.Agents {
		if strings.EqualFold(cfg.Name, "interviewsimulator") {
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
	interviewSimulatorCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.ConfigProvider, "config-provider", "p",
		"file", "config provider")
	interviewSimulatorCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.MainConfigFilename, "main-config", "m",
		"config.yaml", "main config file path")
}
