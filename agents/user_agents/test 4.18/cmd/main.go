package main

import (
		"ai/config"
	"flag"
	"fmt"
	"net/http"
	"os"

	agent "user_agent"
)

func main() {
	port := flag.Int("port", 8200, "HTTP server port")
	mainConfig := flag.String("main-config", "../../../config.yaml", "path to main config")
	flag.Parse()

	config.CmdlineFlags.ConfigProvider = "file"
	config.CmdlineFlags.MainConfigFilename = *mainConfig
	config.Init()

	a, err := agent.NewAgent()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create agent: %v\n", err)
		os.Exit(1)
	}

	handler, err := agent.NewHTTPServer(a)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create HTTP server: %v\n", err)
		os.Exit(1)
	}

	addr := fmt.Sprintf(":%d", *port)
	fmt.Printf("Starting agent on %s\n", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}
