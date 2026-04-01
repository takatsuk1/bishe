package cmd

import (
	"ai/api/chat"
	"ai/config"
	"ai/pkg/logger"
	"net/http"

	"github.com/spf13/cobra"
)

var (
	openaiConnectorCmd = &cobra.Command{
		Use:          "openai_connector",
		Short:        "",
		Long:         "connect agent with openai compatible server",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			defer logger.Sync()
			// 初始化配置
			config.Init()
			h, addr, err := buildOpenAIConnectorHTTPServer()
			if err != nil {
				logger.Fatal(err)
			}
			logger.Infof("openai_connector listening on %s", addr)
			if err := http.ListenAndServe(addr, h); err != nil {
				logger.Fatal(err)
			}
		},
	}
)

func buildOpenAIConnectorHTTPServer() (http.Handler, string, error) {
	addr := ":11000"
	if cfg := config.GetMainConfig(); cfg != nil {
		if cfg.OpenAIConnector.Listen != "" {
			addr = cfg.OpenAIConnector.Listen
		}
	}
	h, err := chat.NewOpenAIServer()
	if err != nil {
		return nil, "", err
	}
	return h, addr, nil
}

// init
func init() {
	openaiConnectorCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.TRPCConfig, "config", "c",
		"./trpc_go.yaml", "(deprecated) trpc config file path")
	openaiConnectorCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.ConfigProvider, "config-provider", "p",
		"file", "config provider")
	openaiConnectorCmd.PersistentFlags().StringVarP(&config.CmdlineFlags.MainConfigFilename, "main-config", "m",
		"config.yaml", "main config file path")
}
