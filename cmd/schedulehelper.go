package cmd

import (
	"ai/agents/schedulehelper"
	"ai/config"
	"ai/pkg/logger"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

var (
	scheduleHelperCmd = &cobra.Command{
		Use:          "schedulehelper",
		Short:        "schedulehelper",
		Long:         "schedulehelper",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			defer logger.Sync()
			config.Init()
			handler, addr, err := buildScheduleHelperHTTPServer()
			if err != nil {
				logger.Fatal(err)
			}
			if err := http.ListenAndServe(addr, handler); err != nil {
				logger.Fatal(err)
			}
		},
	}
)

func buildScheduleHelperHTTPServer() (http.Handler, string, error) {
	agt, err := schedulehelper.NewAgent()
	if err != nil {
		return nil, "", err
	}
	handler, err := schedulehelper.NewHTTPServer(agt)
	if err != nil {
		return nil, "", err
	}
	addr := ":9994"
	for _, cfg := range config.GetMainConfig().HostAgent.Agents {
		if strings.EqualFold(cfg.Name, "schedulehelper") {
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
	scheduleHelperCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.ConfigProvider, "config-provider", "p",
		"file", "config provider")
	scheduleHelperCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.MainConfigFilename, "main-config", "m",
		"config.yaml", "main config file path")
}
