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

// ManagerConfig 代理进程管理器配置
type ManagerConfig struct {
	BasePort       int           // 端口范围起始值
	MaxPort        int           // 端口范围结束值
	AgentsDir      string        // 代理目录路径
	ProjectRoot    string        // 项目根目录路径
	HealthInterval time.Duration // 健康检查间隔时间
}

// ProcessStatus 进程状态类型
type ProcessStatus string

const (
	ProcessStatusStarting ProcessStatus = "starting" // 进程启动中
	ProcessStatusRunning  ProcessStatus = "running"  // 进程运行中
	ProcessStatusStopped  ProcessStatus = "stopped"  // 进程已停止
	ProcessStatusFailed   ProcessStatus = "failed"   // 进程启动失败
)

// AgentProcess 代理进程信息
type AgentProcess struct {
	ID        string              // 代理ID
	Name      string              // 代理名称
	Port      int                 // 代理运行端口
	Status    ProcessStatus       // 进程状态
	Cmd       *exec.Cmd           // 进程命令对象
	Client    *httpagent.Client   // HTTP客户端
	Card      *protocol.AgentCard // 代理卡片信息
	StartedAt time.Time           // 启动时间
	CodePath  string              // 代码路径
	Error     string              // 错误信息
}

// AgentProcessManager 代理进程管理器
type AgentProcessManager struct {
	config    ManagerConfig            // 管理器配置
	portPool  *PortPool                // 端口池
	processes map[string]*AgentProcess // 代理进程映射表
	storage   *storage.MySQLStorage    // MySQL存储
	mu        sync.RWMutex             // 读写锁
}

// NewAgentProcessManager 创建一个新的代理进程管理器
// 参数:
//
//	config - 管理器配置
//	mysqlStorage - MySQL存储实例
//
// 返回值:
//
//	新创建的代理进程管理器实例
func NewAgentProcessManager(config ManagerConfig, mysqlStorage *storage.MySQLStorage) *AgentProcessManager {
	// 设置默认值
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

// CompileAgent 编译代理代码
// 参数:
//
//	ctx - 上下文
//	agentID - 代理ID
//	codePath - 代码路径
//
// 返回值:
//
//	编译成功返回nil，否则返回错误
func (m *AgentProcessManager) CompileAgent(ctx context.Context, agentID string, codePath string) error {
	logger.Infof("[AgentManager] CompileAgent agentId=%s codePath=%s", agentID, codePath)

	// 检查并生成main.go文件
	mainFile := filepath.Join(codePath, "cmd", "main.go")
	if _, err := os.Stat(mainFile); os.IsNotExist(err) {
		if err := m.generateMainFile(codePath, agentID); err != nil {
			return fmt.Errorf("generate main file: %w", err)
		}
	}

	// 检查并生成go.mod文件
	goModFile := filepath.Join(codePath, "go.mod")
	if _, err := os.Stat(goModFile); os.IsNotExist(err) {
		if err := m.generateGoMod(codePath); err != nil {
			return fmt.Errorf("generate go.mod: %w", err)
		}
	}

	// 执行go mod tidy
	cmd := exec.CommandContext(ctx, "go", "mod", "tidy")
	cmd.Dir = codePath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go mod tidy failed: %w, output: %s", err, string(output))
	}

	// 执行go build
	cmd = exec.CommandContext(ctx, "go", "build", "-o", "agent.exe", "./cmd")
	cmd.Dir = codePath
	output, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go build failed: %w, output: %s", err, string(output))
	}

	logger.Infof("[AgentManager] CompileAgent done agentId=%s", agentID)
	return nil
}

// StartAgent 启动代理进程
// 参数:
//
//	ctx - 上下文
//	agentID - 代理ID
//	codePath - 代码路径
//
// 返回值:
//
//	启动成功返回nil，否则返回错误
func (m *AgentProcessManager) StartAgent(ctx context.Context, agentID string, codePath string) error {
	logger.Infof("[AgentManager] StartAgent agentId=%s codePath=%s", agentID, codePath)

	// 检查代理是否已在运行
	m.mu.Lock()
	if proc, exists := m.processes[agentID]; exists && proc.Status == ProcessStatusRunning {
		m.mu.Unlock()
		return fmt.Errorf("agent %s is already running", agentID)
	}
	m.mu.Unlock()

	// 检查可执行文件是否存在
	execPath := filepath.Join(codePath, "agent.exe")
	if _, err := os.Stat(execPath); os.IsNotExist(err) {
		return fmt.Errorf("agent executable not found: %s", execPath)
	}

	// 分配可用端口
	port, err := m.allocateFreePort()
	if err != nil {
		return fmt.Errorf("allocate port: %w", err)
	}

	// 不将代理进程生命周期绑定到请求上下文
	// 已发布的代理是长期运行的服务，必须在HTTP处理程序返回后继续运行
	cmd := exec.Command(execPath, "--port", fmt.Sprintf("%d", port))
	cmd.Dir = codePath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// 创建进程对象
	proc := &AgentProcess{
		ID:        agentID,
		Port:      port,
		Status:    ProcessStatusStarting,
		Cmd:       cmd,
		CodePath:  codePath,
		StartedAt: time.Now(),
	}

	// 注册进程
	m.mu.Lock()
	m.processes[agentID] = proc
	m.mu.Unlock()

	// 启动进程
	if err := cmd.Start(); err != nil {
		m.mu.Lock()
		proc.Status = ProcessStatusFailed
		proc.Error = err.Error()
		delete(m.processes, agentID)
		m.mu.Unlock()
		m.portPool.Release(port)
		return fmt.Errorf("start process: %w", err)
	}

	// 创建HTTP客户端
	client := httpagent.NewClient(fmt.Sprintf("http://localhost:%d", port), 10*time.Minute)

	// 等待代理就绪
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

	// 更新进程状态为运行中
	m.mu.Lock()
	proc.Status = ProcessStatusRunning
	proc.Client = client
	proc.Card = card
	m.mu.Unlock()

	// 更新数据库中的代理状态
	if m.storage != nil {
		_ = m.storage.UpdateAgentStatus(ctx, agentID, storage.AgentStatusPublished, port, cmd.Process.Pid)
	}

	// 启动进程监控
	go m.monitorProcess(agentID)

	logger.Infof("[AgentManager] StartAgent done agentId=%s port=%d pid=%d", agentID, port, cmd.Process.Pid)
	return nil
}

// StopAgent 停止代理进程
// 参数:
//
//	ctx - 上下文
//	agentID - 代理ID
//
// 返回值:
//
//	停止成功返回nil，否则返回错误
func (m *AgentProcessManager) StopAgent(ctx context.Context, agentID string) error {
	logger.Infof("[AgentManager] StopAgent agentId=%s", agentID)

	// 获取进程信息
	m.mu.Lock()
	proc, exists := m.processes[agentID]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("agent %s not found", agentID)
	}
	m.mu.Unlock()

	// 终止进程
	if proc.Cmd != nil && proc.Cmd.Process != nil {
		if err := proc.Cmd.Process.Kill(); err != nil {
			logger.Warnf("[AgentManager] Failed to kill process: %v", err)
		}
	}

	// 更新进程状态并释放资源
	m.mu.Lock()
	proc.Status = ProcessStatusStopped
	m.portPool.Release(proc.Port)
	delete(m.processes, agentID)
	m.mu.Unlock()

	// 更新数据库中的代理状态
	if m.storage != nil {
		_ = m.storage.UpdateAgentStatus(ctx, agentID, storage.AgentStatusStopped, 0, 0)
	}

	logger.Infof("[AgentManager] StopAgent done agentId=%s", agentID)
	return nil
}

// RestartAgent 重启代理进程
// 参数:
//
//	ctx - 上下文
//	agentID - 代理ID
//
// 返回值:
//
//	重启成功返回nil，否则返回错误
func (m *AgentProcessManager) RestartAgent(ctx context.Context, agentID string) error {
	logger.Infof("[AgentManager] RestartAgent agentId=%s", agentID)

	// 获取进程信息
	m.mu.RLock()
	proc, exists := m.processes[agentID]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("agent %s not found", agentID)
	}

	// 停止代理
	if err := m.StopAgent(ctx, agentID); err != nil {
		return fmt.Errorf("stop agent: %w", err)
	}

	// 等待一秒确保进程完全停止
	time.Sleep(1 * time.Second)

	// 重新启动代理
	return m.StartAgent(ctx, agentID, proc.CodePath)
}

// GetAgentClient 获取代理的HTTP客户端
// 参数:
//
//	agentID - 代理ID
//
// 返回值:
//
//	HTTP客户端实例，如果代理不存在或未运行则返回错误
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

// GetAgentCard 获取代理的卡片信息
// 参数:
//
//	agentID - 代理ID
//
// 返回值:
//
//	代理卡片信息，如果代理不存在则返回错误
func (m *AgentProcessManager) GetAgentCard(agentID string) (*protocol.AgentCard, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	proc, exists := m.processes[agentID]
	if !exists {
		return nil, fmt.Errorf("agent %s not found", agentID)
	}

	return proc.Card, nil
}

// ListAgents 列出所有代理进程
// 返回值:
//
//	代理进程列表
func (m *AgentProcessManager) ListAgents() []AgentProcess {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]AgentProcess, 0, len(m.processes))
	for _, proc := range m.processes {
		result = append(result, *proc)
	}
	return result
}

// GetAgentStatus 获取代理进程状态
// 参数:
//
//	agentID - 代理ID
//
// 返回值:
//
//	进程状态，如果代理不存在则返回错误
func (m *AgentProcessManager) GetAgentStatus(agentID string) (ProcessStatus, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	proc, exists := m.processes[agentID]
	if !exists {
		return ProcessStatusStopped, fmt.Errorf("agent %s not found", agentID)
	}

	return proc.Status, nil
}

// monitorProcess 监控代理进程的运行状态
// 参数:
//
//	agentID - 代理ID
func (m *AgentProcessManager) monitorProcess(agentID string) {
	// 获取进程信息
	m.mu.RLock()
	proc, exists := m.processes[agentID]
	m.mu.RUnlock()

	if !exists {
		return
	}

	if proc.Cmd == nil {
		return
	}

	// 等待进程结束
	err := proc.Cmd.Wait()
	port := 0
	status := ProcessStatusStopped

	// 更新进程状态
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

	// 更新数据库中的代理状态
	if m.storage != nil {
		_ = m.storage.UpdateAgentStatus(context.Background(), agentID, storage.AgentStatusStopped, 0, 0)
	}

	logger.Infof("[AgentManager] Process exited agentId=%s status=%s port=%d", agentID, status, port)
}

// fetchAgentCard 获取代理的卡片信息
// 参数:
//
//	ctx - 上下文
//	cardURL - 卡片信息的URL
//
// 返回值:
//
//	代理卡片信息，如果获取失败则返回错误
func (m *AgentProcessManager) fetchAgentCard(ctx context.Context, cardURL string) (*protocol.AgentCard, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cardURL, nil)
	if err != nil {
		return nil, err
	}

	// 发送HTTP请求，设置超时时间为1.2秒
	resp, err := (&http.Client{Timeout: 1200 * time.Millisecond}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// 检查响应状态码
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("card status %d", resp.StatusCode)
	}

	// 读取响应体，限制最大读取1MB
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	// 解析JSON响应
	var card protocol.AgentCard
	if err := json.Unmarshal(body, &card); err != nil {
		return nil, fmt.Errorf("decode card: %w", err)
	}
	if strings.TrimSpace(card.Name) == "" {
		return nil, fmt.Errorf("invalid card: empty name")
	}

	return &card, nil
}

// waitForAgentReady 等待代理就绪
// 参数:
//
//	parent - 父上下文
//	cardURL - 卡片信息的URL
//	timeout - 超时时间
//
// 返回值:
//
//	代理卡片信息，如果超时或获取失败则返回错误
func (m *AgentProcessManager) waitForAgentReady(parent context.Context, cardURL string, timeout time.Duration) (*protocol.AgentCard, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	var lastErr error
	// 循环尝试获取代理卡片信息
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

// allocateFreePort 分配一个可用的TCP端口
// 返回值:
//
//	分配的端口号，如果没有可用端口则返回错误
func (m *AgentProcessManager) allocateFreePort() (int, error) {
	// 遍历端口范围，找到可用的端口
	for port := m.config.BasePort; port <= m.config.MaxPort; port++ {
		// 检查端口是否已被使用
		if m.portPool.IsUsed(port) {
			continue
		}
		// 检查TCP端口是否可用
		if !isTCPPortAvailable(port) {
			continue
		}
		// 预留端口
		if err := m.portPool.Reserve(port); err != nil {
			continue
		}
		// 再次检查以减少与外部进程的竞争窗口
		if !isTCPPortAvailable(port) {
			m.portPool.Release(port)
			continue
		}
		return port, nil
	}

	return 0, fmt.Errorf("no available free tcp ports in range %d-%d", m.config.BasePort, m.config.MaxPort)
}

// isTCPPortAvailable 检查TCP端口是否可用
// 参数:
//
//	port - 要检查的端口号
//
// 返回值:
//
//	如果端口可用返回true，否则返回false
func isTCPPortAvailable(port int) bool {
	// 尝试监听端口
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// generateMainFile 生成代理的main.go文件
// 参数:
//
//	codePath - 代码路径
//	agentID - 代理ID
//
// 返回值:
//
//	生成成功返回nil，否则返回错误
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

	// 创建cmd目录并写入main.go文件
	mainPath := filepath.Join(codePath, "cmd", "main.go")
	if err := os.MkdirAll(filepath.Dir(mainPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(mainPath, []byte(mainContent), 0644)
}

// generateGoMod 生成go.mod文件
// 参数:
//
//	codePath - 代码路径
//
// 返回值:
//
//	生成成功返回nil，否则返回错误
func (m *AgentProcessManager) generateGoMod(codePath string) error {
	// 确定项目根目录路径
	replacePath := strings.TrimSpace(m.config.ProjectRoot)
	if replacePath == "" {
		replacePath = filepath.Clean(filepath.Join(codePath, "..", "..", ".."))
	}
	replacePath = filepath.ToSlash(replacePath)

	// 生成go.mod内容
	goModContent := `module user_agent

go 1.24

require ai v0.0.0

replace ai => ` + replacePath + `
`

	goModPath := filepath.Join(codePath, "go.mod")
	return os.WriteFile(goModPath, []byte(goModContent), 0644)
}

// HealthCheck 对所有运行的代理进行健康检查
// 参数:
//
//	ctx - 上下文
func (m *AgentProcessManager) HealthCheck(ctx context.Context) {
	// 获取所有运行的代理进程
	m.mu.RLock()
	processes := make([]*AgentProcess, 0, len(m.processes))
	for _, proc := range m.processes {
		processes = append(processes, proc)
	}
	m.mu.RUnlock()

	// 对每个运行中的代理发送中断信号进行健康检查
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
