package agentpool

import (
	"ai/config"
	"ai/pkg/card"
	"ai/pkg/protocol"
	"ai/pkg/transport/httpagent"
	"fmt"
	"strings"
	"time"
)

const defaultAgentCardPath = "/.well-known/agent.json"

type Entry struct {
	Name   string
	Client *httpagent.Client
	Card   *protocol.AgentCard
}

func BuildFromConfig(agents []config.AgentConfig, timeout time.Duration) (map[string]*Entry, error) {
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	out := make(map[string]*Entry, len(agents))
	for _, cfg := range agents {
		name := strings.TrimSpace(cfg.Name)
		if name == "" {
			return nil, fmt.Errorf("agent name is empty")
		}
		serverURL := strings.TrimSpace(cfg.ServerURL)
		if serverURL == "" {
			return nil, fmt.Errorf("agent %s server_url is empty", name)
		}
		cardURL := strings.TrimSpace(cfg.CardURL)
		if cardURL == "" {
			cardURL = strings.TrimRight(serverURL, "/") + defaultAgentCardPath
		}
		agentCard, err := card.Fetch(cardURL)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch agent card for %s: %w", name, err)
		}
		out[name] = &Entry{
			Name:   name,
			Client: httpagent.NewClient(serverURL, timeout),
			Card:   agentCard,
		}
	}
	return out, nil
}
