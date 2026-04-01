package agentmanager

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ai/pkg/logger"
	"ai/pkg/protocol"
	"ai/pkg/storage"
	"ai/pkg/transport/httpagent"
)

type ManagerConfig struct {
	BasePort       int
	MaxPort        int
	AgentsDir      string
	ProjectRoot    string
	HealthInterval time.Duration
}

type ProcessStatus string

const (
	ProcessStatusStarting ProcessStatus = "starting"
	ProcessStatusRunning  ProcessStatus = "running"
	ProcessStatusStopped  ProcessStatus = "stopped"
	ProcessStatusFailed   ProcessStatus = "failed"
)

type AgentProcess struct {
	ID        string
	Name      string
	Port      int
	Status    ProcessStatus
	Cmd       *exec.Cmd
	Client    *httpagent.Client
	Card      *protocol.AgentCard
	StartedAt time.Time
	CodePath  string
	Error     string
}

type AgentProcessManager struct {
	config    ManagerConfig
	portPool  *PortPool
	processes map[string]*AgentProcess
	storage   *storage.MySQLStorage
	mu        sync.RWMutex
}

func NewAgentProcessManager(config ManagerConfig, mysqlStorage *storage.MySQLStorage) *AgentProcessManager {
	if config.BasePort <= 0 {
		config.BasePort = 8200
	}
	if config.MaxPort <= 0 {
		config.MaxPort = 8300
	}
	if config.HealthInterval <= 0 {
		config.HealthInterval = 30 * time.Second
	}

	return &AgentProcessManager{
		config:    config,
		portPool:  NewPortPool(config.BasePort, config.MaxPort),
		processes: make(map[string]*AgentProcess),
		storage:   mysqlStorage,
	}
}

func (m *AgentProcessManager) CompileAgent(ctx context.Context, agentID string, codePath string) error {
	logger.Infof("[AgentManager] CompileAgent agentId=%s codePath=%s", agentID, codePath)

	mainFile := filepath.Join(codePath, "cmd", "main.go")
	if _, err := os.Stat(mainFile); os.IsNotExist(err) {
		if err := m.generateMainFile(codePath, agentID); err != nil {
			return fmt.Errorf("generate main file: %w", err)
		}
	}

	goModFile := filepath.Join(codePath, "go.mod")
	if _, err := os.Stat(goModFile); os.IsNotExist(err) {
		if err := m.generateGoMod(codePath); err != nil {
			return fmt.Errorf("generate go.mod: %w", err)
		}
	}

	cmd := exec.CommandContext(ctx, "go", "mod", "tidy")
	cmd.Dir = codePath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go mod tidy failed: %w, output: %s", err, string(output))
	}

	cmd = exec.CommandContext(ctx, "go", "build", "-o", "agent.exe", "./cmd")
	cmd.Dir = codePath
	output, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go build failed: %w, output: %s", err, string(output))
	}

	logger.Infof("[AgentManager] CompileAgent done agentId=%s", agentID)
	return nil
}

func (m *AgentProcessManager) StartAgent(ctx context.Context, agentID string, codePath string) error {
	logger.Infof("[AgentManager] StartAgent agentId=%s codePath=%s", agentID, codePath)

	m.mu.Lock()
	if proc, exists := m.processes[agentID]; exists && proc.Status == ProcessStatusRunning {
		m.mu.Unlock()
		return fmt.Errorf("agent %s is already running", agentID)
	}
	m.mu.Unlock()

	execPath := filepath.Join(codePath, "agent.exe")
	if _, err := os.Stat(execPath); os.IsNotExist(err) {
		return fmt.Errorf("agent executable not found: %s", execPath)
	}

	port, err := m.allocateFreePort()
	if err != nil {
		return fmt.Errorf("allocate port: %w", err)
	}

	// Do not bind the agent process lifecycle to request context.
	// Published agents are long-running services and must survive after HTTP handlers return.
	cmd := exec.Command(execPath, "--port", fmt.Sprintf("%d", port))
	cmd.Dir = codePath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	proc := &AgentProcess{
		ID:        agentID,
		Port:      port,
		Status:    ProcessStatusStarting,
		Cmd:       cmd,
		CodePath:  codePath,
		StartedAt: time.Now(),
	}

	m.mu.Lock()
	m.processes[agentID] = proc
	m.mu.Unlock()

	if err := cmd.Start(); err != nil {
		m.mu.Lock()
		proc.Status = ProcessStatusFailed
		proc.Error = err.Error()
		delete(m.processes, agentID)
		m.mu.Unlock()
		m.portPool.Release(port)
		return fmt.Errorf("start process: %w", err)
	}

	client := httpagent.NewClient(fmt.Sprintf("http://localhost:%d", port), 10*time.Minute)

	cardURL := fmt.Sprintf("http://localhost:%d/.well-known/agent.json", port)
	card, err := m.waitForAgentReady(ctx, cardURL, 10*time.Second)
	if err != nil {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		m.mu.Lock()
		if failedProc, exists := m.processes[agentID]; exists {
			failedProc.Status = ProcessStatusFailed
			failedProc.Error = err.Error()
			delete(m.processes, agentID)
		}
		m.mu.Unlock()
		m.portPool.Release(port)
		return fmt.Errorf("agent %s failed readiness check on port %d: %w", agentID, port, err)
	}

	m.mu.Lock()
	proc.Status = ProcessStatusRunning
	proc.Client = client
	proc.Card = card
	m.mu.Unlock()

	if m.storage != nil {
		_ = m.storage.UpdateAgentStatus(ctx, agentID, storage.AgentStatusPublished, port, cmd.Process.Pid)
	}

	go m.monitorProcess(agentID)

	logger.Infof("[AgentManager] StartAgent done agentId=%s port=%d pid=%d", agentID, port, cmd.Process.Pid)
	return nil
}

func (m *AgentProcessManager) StopAgent(ctx context.Context, agentID string) error {
	logger.Infof("[AgentManager] StopAgent agentId=%s", agentID)

	m.mu.Lock()
	proc, exists := m.processes[agentID]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("agent %s not found", agentID)
	}
	m.mu.Unlock()

	if proc.Cmd != nil && proc.Cmd.Process != nil {
		if err := proc.Cmd.Process.Kill(); err != nil {
			logger.Warnf("[AgentManager] Failed to kill process: %v", err)
		}
	}

	m.mu.Lock()
	proc.Status = ProcessStatusStopped
	m.portPool.Release(proc.Port)
	delete(m.processes, agentID)
	m.mu.Unlock()

	if m.storage != nil {
		_ = m.storage.UpdateAgentStatus(ctx, agentID, storage.AgentStatusStopped, 0, 0)
	}

	logger.Infof("[AgentManager] StopAgent done agentId=%s", agentID)
	return nil
}

func (m *AgentProcessManager) RestartAgent(ctx context.Context, agentID string) error {
	logger.Infof("[AgentManager] RestartAgent agentId=%s", agentID)

	m.mu.RLock()
	proc, exists := m.processes[agentID]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("agent %s not found", agentID)
	}

	if err := m.StopAgent(ctx, agentID); err != nil {
		return fmt.Errorf("stop agent: %w", err)
	}

	time.Sleep(1 * time.Second)

	return m.StartAgent(ctx, agentID, proc.CodePath)
}

func (m *AgentProcessManager) GetAgentClient(agentID string) (*httpagent.Client, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	proc, exists := m.processes[agentID]
	if !exists {
		return nil, fmt.Errorf("agent %s not found", agentID)
	}

	if proc.Status != ProcessStatusRunning {
		return nil, fmt.Errorf("agent %s is not running", agentID)
	}

	return proc.Client, nil
}

func (m *AgentProcessManager) GetAgentCard(agentID string) (*protocol.AgentCard, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	proc, exists := m.processes[agentID]
	if !exists {
		return nil, fmt.Errorf("agent %s not found", agentID)
	}

	return proc.Card, nil
}

func (m *AgentProcessManager) ListAgents() []AgentProcess {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]AgentProcess, 0, len(m.processes))
	for _, proc := range m.processes {
		result = append(result, *proc)
	}
	return result
}

func (m *AgentProcessManager) GetAgentStatus(agentID string) (ProcessStatus, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	proc, exists := m.processes[agentID]
	if !exists {
		return ProcessStatusStopped, fmt.Errorf("agent %s not found", agentID)
	}

	return proc.Status, nil
}

func (m *AgentProcessManager) monitorProcess(agentID string) {
	m.mu.RLock()
	proc, exists := m.processes[agentID]
	m.mu.RUnlock()

	if !exists {
		return
	}

	if proc.Cmd == nil {
		return
	}

	err := proc.Cmd.Wait()
	port := 0
	status := ProcessStatusStopped

	m.mu.Lock()
	if proc, exists := m.processes[agentID]; exists {
		if err != nil {
			proc.Status = ProcessStatusFailed
			proc.Error = err.Error()
			status = ProcessStatusFailed
		} else {
			proc.Status = ProcessStatusStopped
			status = ProcessStatusStopped
		}
		port = proc.Port
		m.portPool.Release(proc.Port)
		delete(m.processes, agentID)
	}
	m.mu.Unlock()

	if m.storage != nil {
		_ = m.storage.UpdateAgentStatus(context.Background(), agentID, storage.AgentStatusStopped, 0, 0)
	}

	logger.Infof("[AgentManager] Process exited agentId=%s status=%s port=%d", agentID, status, port)
}

func (m *AgentProcessManager) fetchAgentCard(ctx context.Context, cardURL string) (*protocol.AgentCard, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cardURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := (&http.Client{Timeout: 1200 * time.Millisecond}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("card status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	var card protocol.AgentCard
	if err := json.Unmarshal(body, &card); err != nil {
		return nil, fmt.Errorf("decode card: %w", err)
	}
	if strings.TrimSpace(card.Name) == "" {
		return nil, fmt.Errorf("invalid card: empty name")
	}

	return &card, nil
}

func (m *AgentProcessManager) waitForAgentReady(parent context.Context, cardURL string, timeout time.Duration) (*protocol.AgentCard, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	var lastErr error
	for {
		card, err := m.fetchAgentCard(ctx, cardURL)
		if err == nil {
			return card, nil
		}
		lastErr = err

		select {
		case <-ctx.Done():
			if lastErr == nil {
				lastErr = ctx.Err()
			}
			return nil, lastErr
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (m *AgentProcessManager) allocateFreePort() (int, error) {
	for port := m.config.BasePort; port <= m.config.MaxPort; port++ {
		if m.portPool.IsUsed(port) {
			continue
		}
		if !isTCPPortAvailable(port) {
			continue
		}
		if err := m.portPool.Reserve(port); err != nil {
			continue
		}
		// Re-check after reserve to reduce race window with external processes.
		if !isTCPPortAvailable(port) {
			m.portPool.Release(port)
			continue
		}
		return port, nil
	}

	return 0, fmt.Errorf("no available free tcp ports in range %d-%d", m.config.BasePort, m.config.MaxPort)
}

func isTCPPortAvailable(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

func (m *AgentProcessManager) generateMainFile(codePath string, agentID string) error {
	_ = agentID
	mainContent := fmt.Sprintf(`package main

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
		fmt.Fprintf(os.Stderr, "Failed to create agent: %%v\n", err)
		os.Exit(1)
	}

	handler, err := agent.NewHTTPServer(a)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create HTTP server: %%v\n", err)
		os.Exit(1)
	}

	addr := fmt.Sprintf(":%%d", *port)
	fmt.Printf("Starting agent on %%s\n", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %%v\n", err)
		os.Exit(1)
	}
}
`)

	mainPath := filepath.Join(codePath, "cmd", "main.go")
	if err := os.MkdirAll(filepath.Dir(mainPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(mainPath, []byte(mainContent), 0644)
}

func (m *AgentProcessManager) generateGoMod(codePath string) error {
	replacePath := strings.TrimSpace(m.config.ProjectRoot)
	if replacePath == "" {
		replacePath = filepath.Clean(filepath.Join(codePath, "..", "..", ".."))
	}
	replacePath = filepath.ToSlash(replacePath)

	goModContent := `module user_agent

go 1.24

require ai v0.0.0

replace ai => ` + replacePath + `
`

	goModPath := filepath.Join(codePath, "go.mod")
	return os.WriteFile(goModPath, []byte(goModContent), 0644)
}
func (m *AgentProcessManager) HealthCheck(ctx context.Context) {
	m.mu.RLock()
	processes := make([]*AgentProcess, 0, len(m.processes))
	for _, proc := range m.processes {
		processes = append(processes, proc)
	}
	m.mu.RUnlock()

	for _, proc := range processes {
		if proc.Status != ProcessStatusRunning {
			continue
		}

		if proc.Cmd == nil || proc.Cmd.Process == nil {
			continue
		}

		if err := proc.Cmd.Process.Signal(os.Interrupt); err != nil {
			logger.Warnf("[AgentManager] Health check failed for %s: %v", proc.ID, err)
		}
	}
}
