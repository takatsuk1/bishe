package cmd

import (
	"ai/agents/memoreminder"
	"ai/config"
	"ai/pkg/logger"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

var (
	memoReminderCmd = &cobra.Command{
		Use:          "memoreminder",
		Short:        "memoreminder",
		Long:         "memoreminder",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			defer logger.Sync()
			config.Init()
			handler, addr, err := buildMemoReminderHTTPServer()
			if err != nil {
				logger.Fatal(err)
			}
			if err := http.ListenAndServe(addr, handler); err != nil {
				logger.Fatal(err)
			}
		},
	}
)

func buildMemoReminderHTTPServer() (http.Handler, string, error) {
	agt, err := memoreminder.NewAgent()
	if err != nil {
		return nil, "", err
	}
	handler, err := memoreminder.NewHTTPServer(agt)
	if err != nil {
		return nil, "", err
	}
	addr := ":9999"
	for _, cfg := range config.GetMainConfig().HostAgent.Agents {
		if strings.EqualFold(cfg.Name, "memoreminder") {
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
	memoReminderCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.ConfigProvider, "config-provider", "p",
		"file", "config provider")
	memoReminderCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.MainConfigFilename, "main-config", "m",
		"config.yaml", "main config file path")
}
