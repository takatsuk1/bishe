package cmd

import (
	"ai/agents/urlreader"
	"ai/config"
	"ai/pkg/logger"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

var (
	urlReaderCmd = &cobra.Command{
		Use:          "urlreader",
		Short:        "init url reader agent server",
		Long:         "init url reader agent server",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			defer logger.Sync()
			config.Init()

			handler, addr, err := buildURLReaderHTTPServer()
			if err != nil {
				logger.Fatal(err)
			}
			if err := http.ListenAndServe(addr, handler); err != nil {
				logger.Fatal(err)
			}
		},
	}
)

func buildURLReaderHTTPServer() (http.Handler, string, error) {
	agt, err := urlreader.NewAgent()
	if err != nil {
		return nil, "", err
	}
	handler, err := urlreader.NewHTTPServer(agt)
	if err != nil {
		return nil, "", err
	}
	addr := ":9991"
	for _, cfg := range config.GetMainConfig().HostAgent.Agents {
		if strings.EqualFold(cfg.Name, "urlreader") {
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
	urlReaderCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.ConfigProvider, "config-provider", "p",
		"file", "config provider")
	urlReaderCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.MainConfigFilename, "main-config", "m",
		"config.yaml", "main config file path")
}
