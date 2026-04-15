package codegen

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"ai/pkg/executor"
	"ai/pkg/tools"
)

type GeneratorConfig struct {
	OutputDir      string
	AgentOutputDir string
	ToolOutputDir  string
	PackagePrefix  string
}

type CodeGenerator struct {
	config GeneratorConfig
}

func NewCodeGenerator(config GeneratorConfig) *CodeGenerator {
	if config.AgentOutputDir == "" {
		config.AgentOutputDir = filepath.Join(config.OutputDir, "agents", "user_agents")
	}
	if config.ToolOutputDir == "" {
		config.ToolOutputDir = filepath.Join(config.OutputDir, "pkg", "tools")
	}
	if config.PackagePrefix == "" {
		config.PackagePrefix = "ai"
	}
	return &CodeGenerator{config: config}
}

type AgentGenerateRequest struct {
	AgentID     string
	Name        string
	Description string
	WorkflowDef *executor.WorkflowDefinition
	Tools       []ToolDefinition
}

type ToolDefinition struct {
	ToolID      string
	Name        string
	Description string
	ToolType    tools.ToolType
	Config      map[string]any
	Parameters  []tools.ToolParameter
	// SkipFileGeneration controls whether codegen should skip creating/updating
	// the standalone user_generated_<tool>.go helper for this tool.
	SkipFileGeneration bool
}

type GenerateResult struct {
	AgentDir    string
	AgentFile   string
	ServerFile  string
	ToolFiles   []string
	GeneratedAt time.Time
}

func (g *CodeGenerator) GenerateAgent(req *AgentGenerateRequest) (*GenerateResult, error) {
	result := &GenerateResult{
		GeneratedAt: time.Now(),
	}

	agentDir := filepath.Join(g.config.AgentOutputDir, req.AgentID)
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		return nil, fmt.Errorf("create agent directory: %w", err)
	}
	result.AgentDir = agentDir

	agentCode := g.generateAgentCode(req)
	agentFile := filepath.Join(agentDir, "agent.go")
	if err := os.WriteFile(agentFile, []byte(agentCode), 0644); err != nil {
		return nil, fmt.Errorf("write agent file: %w", err)
	}
	result.AgentFile = agentFile

	serverCode := g.generateServerCode(req)
	serverFile := filepath.Join(agentDir, "server_internal.go")
	if err := os.WriteFile(serverFile, []byte(serverCode), 0644); err != nil {
		return nil, fmt.Errorf("write server file: %w", err)
	}
	result.ServerFile = serverFile

	for _, toolDef := range req.Tools {
		if toolDef.SkipFileGeneration {
			continue
		}
		toolCode := g.generateToolCode(&toolDef)
		newName := sanitizePackageName(toolDef.ToolID)
		toolFile := filepath.Join(g.config.ToolOutputDir, fmt.Sprintf("user_generated_%s.go", newName))
		if _, statErr := os.Stat(toolFile); statErr == nil {
			// Keep previously generated tool file stable unless caller explicitly
			// decides to remove/regenerate it.
			continue
		} else if !os.IsNotExist(statErr) {
			return nil, fmt.Errorf("stat tool file: %w", statErr)
		}

		// Compatibility cleanup: old versions used unsafely-sanitized names,
		// which can coexist and trigger duplicate symbols on rebuild.
		legacyName := legacySanitizePackageName(toolDef.ToolID)
		if legacyName != newName {
			legacyFile := filepath.Join(g.config.ToolOutputDir, fmt.Sprintf("user_generated_%s.go", legacyName))
			_ = os.Remove(legacyFile)
		}

		if err := os.MkdirAll(filepath.Dir(toolFile), 0755); err != nil {
			return nil, fmt.Errorf("create tool directory: %w", err)
		}
		if err := os.WriteFile(toolFile, []byte(toolCode), 0644); err != nil {
			return nil, fmt.Errorf("write tool file: %w", err)
		}
		result.ToolFiles = append(result.ToolFiles, toolFile)
	}

	return result, nil
}

func (g *CodeGenerator) ToolFilePath(toolID string) string {
	newName := sanitizePackageName(toolID)
	return filepath.Join(g.config.ToolOutputDir, fmt.Sprintf("user_generated_%s.go", newName))
}

func (g *CodeGenerator) ToolFileExists(toolID string) bool {
	_, err := os.Stat(g.ToolFilePath(toolID))
	return err == nil
}

func (g *CodeGenerator) ensureAgentConfigEntry(agentID string) error {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil
	}

	configPath := filepath.Join(g.config.OutputDir, "config.yaml")
	bts, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	content := string(bts)
	nameRe := regexp.MustCompile(`(?m)^\s*-\s*name:\s*` + regexp.QuoteMeta(agentID) + `\s*$`)
	if nameRe.MatchString(content) {
		return nil
	}

	lines := strings.Split(content, "\n")
	openaiIdx := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "openai_connector:" {
			openaiIdx = i
			break
		}
	}
	if openaiIdx < 0 {
		return nil
	}

	sectionEnd := len(lines)
	for i := openaiIdx + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}
		if !startsWithIndent(lines[i]) {
			sectionEnd = i
			break
		}
	}

	agentsIdx := -1
	for i := openaiIdx + 1; i < sectionEnd; i++ {
		if strings.TrimSpace(lines[i]) == "agents:" {
			agentsIdx = i
			break
		}
	}
	if agentsIdx < 0 {
		return nil
	}

	insertIdx := sectionEnd
	for i := agentsIdx + 1; i < sectionEnd; i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "# - name:") {
			insertIdx = i
			break
		}
	}

	entry := []string{
		"    - name: " + agentID,
		"      server_url: http://127.0.0.1:8200",
	}
	newLines := append([]string{}, lines[:insertIdx]...)
	newLines = append(newLines, entry...)
	newLines = append(newLines, lines[insertIdx:]...)

	newContent := strings.Join(newLines, "\n")
	if newContent == content {
		return nil
	}

	return os.WriteFile(configPath, []byte(newContent), 0644)
}

func startsWithIndent(s string) bool {
	if s == "" {
		return false
	}
	return s[0] == ' ' || s[0] == '\t'
}

func (g *CodeGenerator) generateAgentCode(req *AgentGenerateRequest) string {
	packageName := sanitizePackageName(req.AgentID)

	var buf strings.Builder
	buf.WriteString(fmt.Sprintf("// Code generated by codegen. DO NOT EDIT.\n"))
	buf.WriteString(fmt.Sprintf("// Generated at: %s\n\n", time.Now().Format(time.RFC3339)))
	buf.WriteString(fmt.Sprintf("package %s\n\n", packageName))

	buf.WriteString("import (\n")
	buf.WriteString("\t\"ai/config\"\n")
	buf.WriteString("\t\"ai/pkg/llm\"\n")
	buf.WriteString("\t\"ai/pkg/monitor\"\n")
	buf.WriteString("\t\"ai/pkg/orchestrator\"\n")
	buf.WriteString("\tinternalproto \"ai/pkg/protocol\"\n")
	buf.WriteString("\t\"ai/pkg/storage\"\n")
	buf.WriteString("\tinternaltm \"ai/pkg/taskmanager\"\n")
	buf.WriteString("\t\"ai/pkg/tools\"\n")
	buf.WriteString("\t\"context\"\n")
	buf.WriteString("\t\"fmt\"\n")
	buf.WriteString("\t\"strings\"\n")
	buf.WriteString("\t\"time\"\n")
	buf.WriteString(")\n\n")

	buf.WriteString(g.generateConstants(req))
	buf.WriteString(g.generateAgentStruct(req))
	buf.WriteString(g.generateNewAgent(req))
	buf.WriteString(g.generateProcessInternal(req))
	buf.WriteString(g.generateWorker(req))
	buf.WriteString(g.generateWorkflowBuilder(req))

	return buf.String()
}

func (g *CodeGenerator) generateConstants(req *AgentGenerateRequest) string {
	var buf strings.Builder
	agentIdent := toGoIdent(req.AgentID)
	workflowID := strings.TrimSpace(req.AgentID)
	if req.WorkflowDef != nil {
		if wid := strings.TrimSpace(req.WorkflowDef.WorkflowID); wid != "" {
			workflowID = wid
		}
	}
	buf.WriteString(fmt.Sprintf("const (\n"))
	buf.WriteString(fmt.Sprintf("\t%sWorkflowID = %q\n", agentIdent, workflowID))
	buf.WriteString(fmt.Sprintf("\t%sWorkflowWorkerID = \"%s_worker\"\n", agentIdent, req.AgentID))
	buf.WriteString(fmt.Sprintf("\t%sDefaultTaskType = \"%s_default\"\n", agentIdent, req.AgentID))
	buf.WriteString(")\n\n")

	buf.WriteString("type ctxKeyTaskManager struct{}\n\n")

	buf.WriteString(fmt.Sprintf("var %sNodeTypeByID = map[string]string{\n", agentIdent))
	for _, n := range req.WorkflowDef.Nodes {
		typeLabel := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(n.Type)), "_", "")
		if typeLabel == "" {
			typeLabel = "node"
		}
		buf.WriteString(fmt.Sprintf("\t%q: %q,\n", n.ID, typeLabel))
	}
	buf.WriteString("}\n\n")

	return buf.String()
}

func (g *CodeGenerator) generateAgentStruct(req *AgentGenerateRequest) string {
	var buf strings.Builder
	buf.WriteString(fmt.Sprintf("type Agent struct {\n"))
	buf.WriteString("\torchestratorEngine orchestrator.Engine\n")
	buf.WriteString("\tllmClient          *llm.Client\n")
	buf.WriteString("\tchatModel          string\n")

	for _, tool := range req.Tools {
		fieldName := toGoIdent(tool.ToolID) + "Tool"
		buf.WriteString(fmt.Sprintf("\t%s tools.Tool\n", fieldName))
	}

	buf.WriteString("}\n\n")

	buf.WriteString("type workflowNodeWorker struct {\n")
	buf.WriteString("\tagent *Agent\n")
	buf.WriteString("}\n\n")

	buf.WriteString("type stepReporter struct {\n")
	buf.WriteString("\tagent string\n")
	buf.WriteString("\ttaskID string\n")
	buf.WriteString("\tmanager internaltm.Manager\n")
	buf.WriteString("}\n\n")

	return buf.String()
}

func (g *CodeGenerator) generateNewAgent(req *AgentGenerateRequest) string {
	var buf strings.Builder
	buf.WriteString("func NewAgent() (*Agent, error) {\n")
	buf.WriteString("\tcfg := config.GetMainConfig()\n")
	buf.WriteString("\tagent := &Agent{}\n\n")
	buf.WriteString("\tagent.llmClient = llm.NewClient(cfg.LLM.URL, cfg.LLM.APIKey)\n")
	buf.WriteString("\tagent.chatModel = strings.TrimSpace(cfg.LLM.ChatModel)\n")
	buf.WriteString("\tif agent.chatModel == \"\" {\n")
	buf.WriteString("\t\tagent.chatModel = strings.TrimSpace(cfg.LLM.ReasoningModel)\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tif agent.chatModel == \"\" {\n")
	buf.WriteString("\t\tagent.chatModel = \"qwen3.5-plus\"\n")
	buf.WriteString("\t}\n")

	for _, tool := range req.Tools {
		buf.WriteString(g.generateToolInit(tool))
	}

	buf.WriteString("\n")
	buf.WriteString("\tengineCfg := orchestrator.Config{\n")
	buf.WriteString("\t\tDefaultTaskTimeoutSec: cfg.Orchestrator.DefaultTaskTimeoutSec,\n")
	buf.WriteString("\t\tRetryMaxAttempts:      cfg.Orchestrator.Retry.MaxAttempts,\n")
	buf.WriteString("\t\tRetryBaseBackoffMs:    cfg.Orchestrator.Retry.BaseBackoffMs,\n")
	buf.WriteString("\t\tRetryMaxBackoffMs:     cfg.Orchestrator.Retry.MaxBackoffMs,\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tif engineCfg.DefaultTaskTimeoutSec <= 0 {\n")
	buf.WriteString("\t\tengineCfg.DefaultTaskTimeoutSec = 600\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tif engineCfg.RetryMaxAttempts <= 0 {\n")
	buf.WriteString("\t\tengineCfg.RetryMaxAttempts = 3\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tif engineCfg.RetryBaseBackoffMs <= 0 {\n")
	buf.WriteString("\t\tengineCfg.RetryBaseBackoffMs = 200\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tif engineCfg.RetryMaxBackoffMs <= 0 {\n")
	buf.WriteString("\t\tengineCfg.RetryMaxBackoffMs = 5000\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tmysqlStorage, mysqlErr := storage.GetMySQLStorage()\n")
	buf.WriteString("\tif (mysqlErr != nil || mysqlStorage == nil) && strings.TrimSpace(cfg.MySQL.DSN) != \"\" {\n")
	buf.WriteString("\t\tmysqlStorage, mysqlErr = storage.InitMySQL(strings.TrimSpace(cfg.MySQL.DSN))\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tif mysqlErr == nil && mysqlStorage != nil {\n")
	buf.WriteString("\t\tengineCfg.MonitorService = monitor.NewService(mysqlStorage, nil)\n")
	buf.WriteString("\t} else {\n")
	buf.WriteString("\t}\n\n")

	buf.WriteString("\tagent.orchestratorEngine = orchestrator.NewEngine(engineCfg, orchestrator.NewInMemoryAgentRegistry())\n")
	agentIdent := toGoIdent(req.AgentID)
	buf.WriteString(fmt.Sprintf("\tif err := agent.orchestratorEngine.RegisterWorker(orchestrator.AgentDescriptor{\n"))
	buf.WriteString(fmt.Sprintf("\t\tID:           %sWorkflowWorkerID,\n", agentIdent))
	buf.WriteString(fmt.Sprintf("\t\tName:         \"%s workflow worker\",\n", req.Name))
	buf.WriteString(fmt.Sprintf("\t\tCapabilities: []orchestrator.AgentCapability{\"chat_model\", \"tool\", \"%s\"},\n", req.AgentID))
	buf.WriteString("\t}, &workflowNodeWorker{agent: agent}); err != nil {\n")
	buf.WriteString("\t\treturn nil, err\n")
	buf.WriteString("\t}\n\n")

	buf.WriteString(fmt.Sprintf("\twf, err := build%sWorkflow()\n", agentIdent))
	buf.WriteString("\tif err != nil {\n")
	buf.WriteString("\t\treturn nil, err\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tif err = agent.orchestratorEngine.RegisterWorkflow(wf); err != nil {\n")
	buf.WriteString("\t\treturn nil, err\n")
	buf.WriteString("\t}\n\n")

	buf.WriteString("\treturn agent, nil\n")
	buf.WriteString("}\n\n")

	return buf.String()
}

func (g *CodeGenerator) generateToolInit(tool ToolDefinition) string {
	var buf strings.Builder
	fieldName := toGoIdent(tool.ToolID) + "Tool"
	paramExpr := toolParametersExpr(tool.Parameters)

	switch tool.ToolType {
	case tools.ToolTypeHTTP:
		config := tool.Config
		if config == nil {
			config = make(map[string]any)
		}
		method := getStringFromMap(config, "method", "GET")
		url := getStringFromMap(config, "url", "")
		bodyTemplate := getStringFromMap(config, "body_template", "")
		timeout := getFloatFromMap(config, "timeout", 30)

		buf.WriteString(fmt.Sprintf("\t%sConfig := tools.HTTPToolConfig{\n", fieldName))
		buf.WriteString(fmt.Sprintf("\t\tMethod: \"%s\",\n", method))
		buf.WriteString(fmt.Sprintf("\t\tURL: \"%s\",\n", url))
		buf.WriteString(fmt.Sprintf("\t\tTimeout: time.Duration(%d) * time.Second,\n", int(timeout)))
		buf.WriteString("\t}\n")

		if headers, ok := config["headers"].(map[string]any); ok && len(headers) > 0 {
			buf.WriteString(fmt.Sprintf("\t%sConfig.Headers = map[string]string{\n", fieldName))
			for k, v := range headers {
				buf.WriteString(fmt.Sprintf("\t\t\"%s\": \"%v\",\n", k, v))
			}
			buf.WriteString("\t}\n")
		}
		if strings.TrimSpace(bodyTemplate) != "" {
			buf.WriteString(fmt.Sprintf("\t%sConfig.BodyTemplate = %q\n", fieldName, bodyTemplate))
		}

		buf.WriteString(fmt.Sprintf("\tagent.%s = tools.NewHTTPTool(%q, %q, %s, %sConfig)\n",
			fieldName, tool.Name, tool.Description, paramExpr, fieldName))

	case tools.ToolTypeMCP:
		config := tool.Config
		if config == nil {
			config = make(map[string]any)
		}
		serverURL := getStringFromMap(config, "server_url", "")
		toolName := getStringFromMap(config, "tool_name", tool.Name)

		buf.WriteString(fmt.Sprintf("\t%sConfig := tools.MCPToolConfig{\n", fieldName))
		buf.WriteString(fmt.Sprintf("\t\tServerURL: %q,\n", serverURL))
		buf.WriteString(fmt.Sprintf("\t\tToolName: %q,\n", toolName))
		buf.WriteString("\t}\n")
		buf.WriteString(fmt.Sprintf("\tagent.%s = tools.NewMCPTool(%q, %q, %s, %sConfig)\n",
			fieldName, tool.Name, tool.Description, paramExpr, fieldName))

	default:
		buf.WriteString(fmt.Sprintf("\t// Tool type %s not implemented\n", tool.ToolType))
	}

	return buf.String()
}

func (g *CodeGenerator) generateProcessInternal(req *AgentGenerateRequest) string {
	var buf strings.Builder
	agentIdent := toGoIdent(req.AgentID)
	buf.WriteString("func (a *Agent) ProcessInternal(ctx context.Context, taskID string, initialMsg internalproto.Message,\n")
	buf.WriteString("\tmanager internaltm.Manager) error {\n")
	buf.WriteString("\tif len(initialMsg.Parts) == 0 {\n")
	buf.WriteString("\t\treturn fmt.Errorf(\"invalid input parts\")\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tqueryParts := make([]string, 0, len(initialMsg.Parts))\n")
	buf.WriteString("\tfor _, part := range initialMsg.Parts {\n")
	buf.WriteString("\t\tif part.Type != internalproto.PartTypeText {\n")
	buf.WriteString("\t\t\tcontinue\n")
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t\ttext := strings.TrimSpace(part.Text)\n")
	buf.WriteString("\t\tif text != \"\" {\n")
	buf.WriteString("\t\t\tqueryParts = append(queryParts, text)\n")
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tif len(queryParts) == 0 {\n")
	buf.WriteString("\t\treturn fmt.Errorf(\"invalid input parts\")\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tif a.orchestratorEngine == nil {\n")
	buf.WriteString("\t\treturn fmt.Errorf(\"orchestrator engine not initialized\")\n")
	buf.WriteString("\t}\n\n")

	buf.WriteString("\tctx = withTaskManager(ctx, manager)\n")
	buf.WriteString("\tquery := strings.TrimSpace(strings.Join(queryParts, \"\\n\"))\n\n")
	buf.WriteString("\tuserID := \"\"\n")
	buf.WriteString("\tif initialMsg.Metadata != nil {\n")
	buf.WriteString("\t\tuserID = strings.TrimSpace(fmt.Sprint(initialMsg.Metadata[\"user_id\"]))\n")
	buf.WriteString("\t\tif userID == \"\" || userID == \"<nil>\" {\n")
	buf.WriteString("\t\t\tuserID = strings.TrimSpace(fmt.Sprint(initialMsg.Metadata[\"userId\"]))\n")
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t\tif userID == \"\" || userID == \"<nil>\" {\n")
	buf.WriteString("\t\t\tuserID = strings.TrimSpace(fmt.Sprint(initialMsg.Metadata[\"UserID\"]))\n")
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t\tif userID == \"<nil>\" {\n")
	buf.WriteString("\t\t\tuserID = \"\"\n")
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t}\n\n")

	buf.WriteString(fmt.Sprintf("\trunID, err := a.orchestratorEngine.StartWorkflow(ctx, %sWorkflowID, map[string]any{\n", agentIdent))
	buf.WriteString("\t\t\"task_id\": taskID,\n")
	buf.WriteString("\t\t\"query\":   query,\n")
	buf.WriteString("\t\t\"text\":    query,\n")
	buf.WriteString("\t\t\"input\":   query,\n")
	buf.WriteString("\t\t\"user_id\": userID,\n")
	buf.WriteString(fmt.Sprintf("\t\t\"source_agent_id\": %q,\n", req.AgentID))
	buf.WriteString(fmt.Sprintf("\t\t\"agent_id\": %q,\n", req.AgentID))
	buf.WriteString("\t})\n")
	buf.WriteString("\tif err != nil {\n")
	buf.WriteString(fmt.Sprintf("\t\treturn fmt.Errorf(\"failed to start %s workflow: %%w\", err)\n", req.AgentID))
	buf.WriteString("\t}\n")
	buf.WriteString("\tstopProgress := a.startProgressReporter(ctx, taskID, runID, manager)\n")
	buf.WriteString("\tdefer stopProgress()\n")
	buf.WriteString("\trunResult, err := a.orchestratorEngine.WaitRun(ctx, runID)\n")
	buf.WriteString("\tif err != nil {\n")
	buf.WriteString(fmt.Sprintf("\t\treturn fmt.Errorf(\"failed to wait %s workflow: %%w\", err)\n", req.AgentID))
	buf.WriteString("\t}\n")
	buf.WriteString("\tif runResult.State != orchestrator.RunStateSucceeded {\n")
	buf.WriteString("\t\tif runResult.ErrorMessage != \"\" {\n")
	buf.WriteString(fmt.Sprintf("\t\t\treturn fmt.Errorf(\"%s workflow failed: %%s\", runResult.ErrorMessage)\n", req.AgentID))
	buf.WriteString("\t\t}\n")
	buf.WriteString(fmt.Sprintf("\t\treturn fmt.Errorf(\"%s workflow failed\")\n", req.AgentID))
	buf.WriteString("\t}\n")
	buf.WriteString("\tout, _ := runResult.FinalOutput[\"response\"].(string)\n")
	buf.WriteString("\tout = strings.TrimSpace(out)\n")
	buf.WriteString("\tif out == \"\" {\n")
	buf.WriteString("\t\tout = \"Workflow executed successfully\"\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tif manager != nil {\n")
	buf.WriteString("\t\t_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateCompleted, &internalproto.Message{\n")
	buf.WriteString("\t\t\tRole:  internalproto.MessageRoleAgent,\n")
	buf.WriteString("\t\t\tParts: []internalproto.Part{internalproto.NewTextPart(out)},\n")
	buf.WriteString("\t\t})\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\treturn nil\n")
	buf.WriteString("}\n\n")

	return buf.String()
}

func (g *CodeGenerator) generateWorker(req *AgentGenerateRequest) string {
	var buf strings.Builder
	agentIdent := toGoIdent(req.AgentID)

	buf.WriteString("func (w *workflowNodeWorker) Execute(ctx context.Context, req orchestrator.ExecutionRequest) (orchestrator.ExecutionResult, error) {\n")
	buf.WriteString("\ttaskID, _ := req.Payload[\"task_id\"].(string)\n")
	buf.WriteString("\tquery, _ := req.Payload[\"query\"].(string)\n")
	buf.WriteString("\tvar (\n")
	buf.WriteString("\t\tresponse string\n")
	buf.WriteString("\t\terr error\n")
	buf.WriteString("\t)\n")
	buf.WriteString("\tswitch req.NodeType {\n")
	buf.WriteString("\tcase orchestrator.NodeTypeChatModel:\n")
	buf.WriteString("\t\tresponse, err = w.agent.callChatModel(ctx, taskID, query, req.NodeConfig)\n")
	buf.WriteString("\tcase orchestrator.NodeTypeTool:\n")
	buf.WriteString("\t\tresponse, err = w.agent.callTool(ctx, taskID, query, req.NodeConfig, req.Payload)\n")
	buf.WriteString("\tdefault:\n")
	buf.WriteString("\t\tresponse = strings.TrimSpace(query)\n")
	buf.WriteString("\t\tif response == \"\" {\n")
	buf.WriteString("\t\t\tresponse = \"ok\"\n")
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tif err != nil {\n")
	buf.WriteString("\t\treturn orchestrator.ExecutionResult{}, err\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\treturn orchestrator.ExecutionResult{Output: map[string]any{\"response\": response}}, nil\n")
	buf.WriteString("}\n\n")

	buf.WriteString("func (a *Agent) callChatModel(ctx context.Context, taskID string, query string, nodeCfg map[string]any) (string, error) {\n")
	buf.WriteString("\tquery = strings.TrimSpace(query)\n")
	buf.WriteString("\tif query == \"\" {\n")
	buf.WriteString("\t\treturn \"\", fmt.Errorf(\"query is empty\")\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tbaseURL := strings.TrimSpace(a.llmClient.BaseURL)\n")
	buf.WriteString("\tapiKey := strings.TrimSpace(a.llmClient.APIKey)\n")
	buf.WriteString("\tmodel := strings.TrimSpace(a.chatModel)\n")
	buf.WriteString("\tif nodeCfg != nil {\n")
	buf.WriteString("\t\tif v, ok := nodeCfg[\"url\"].(string); ok && strings.TrimSpace(v) != \"\" {\n")
	buf.WriteString("\t\t\tbaseURL = strings.TrimSpace(v)\n")
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t\tif v, ok := nodeCfg[\"apikey\"].(string); ok && strings.TrimSpace(v) != \"\" {\n")
	buf.WriteString("\t\t\tapiKey = strings.TrimSpace(v)\n")
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t\tif v, ok := nodeCfg[\"model\"].(string); ok && strings.TrimSpace(v) != \"\" {\n")
	buf.WriteString("\t\t\tmodel = strings.TrimSpace(v)\n")
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tif baseURL == \"\" || model == \"\" {\n")
	buf.WriteString("\t\treturn \"\", fmt.Errorf(\"chat_model config missing url/model\")\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tresp, err := llm.NewClient(baseURL, apiKey).ChatCompletion(ctx, model, []llm.Message{{Role: \"user\", Content: query}}, nil, nil)\n")
	buf.WriteString("\tif err != nil {\n")
	buf.WriteString("\t\treturn \"\", err\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tresp = strings.TrimSpace(resp)\n")
	buf.WriteString("\tif resp == \"\" {\n")
	buf.WriteString("\t\tresp = \"(empty LLM response)\"\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\treturn resp, nil\n")
	buf.WriteString("}\n\n")

	buf.WriteString("func resolveGeneratedPayloadValue(payload map[string]any, sourceKey string) (any, bool) {\n")
	buf.WriteString("\ttrimmed := strings.TrimSpace(sourceKey)\n")
	buf.WriteString("\tif trimmed == \"\" || payload == nil {\n")
	buf.WriteString("\t\treturn nil, false\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tif v, ok := payload[trimmed]; ok {\n")
	buf.WriteString("\t\treturn v, true\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tparts := strings.Split(trimmed, \".\")\n")
	buf.WriteString("\tif len(parts) <= 1 {\n")
	buf.WriteString("\t\treturn nil, false\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tcurrent, ok := payload[parts[0]]\n")
	buf.WriteString("\tif !ok {\n")
	buf.WriteString("\t\treturn nil, false\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tfor i := 1; i < len(parts); i++ {\n")
	buf.WriteString("\t\tm, ok := current.(map[string]any)\n")
	buf.WriteString("\t\tif !ok {\n")
	buf.WriteString("\t\t\treturn nil, false\n")
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t\tnext, ok := m[parts[i]]\n")
	buf.WriteString("\t\tif !ok {\n")
	buf.WriteString("\t\t\treturn nil, false\n")
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t\tcurrent = next\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\treturn current, true\n")
	buf.WriteString("}\n\n")

	buf.WriteString("func extractGeneratedStringCandidate(v any, preferredKey string) (string, bool) {\n")
	buf.WriteString("\tif s, ok := v.(string); ok {\n")
	buf.WriteString("\t\treturn s, true\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tif b, ok := v.([]byte); ok {\n")
	buf.WriteString("\t\treturn string(b), true\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tm, ok := v.(map[string]any)\n")
	buf.WriteString("\tif !ok {\n")
	buf.WriteString("\t\treturn \"\", false\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tcandidateKeys := []string{\n")
	buf.WriteString("\t\tstrings.TrimSpace(preferredKey),\n")
	buf.WriteString("\t\t\"query\",\n")
	buf.WriteString("\t\t\"text\",\n")
	buf.WriteString("\t\t\"content\",\n")
	buf.WriteString("\t\t\"result\",\n")
	buf.WriteString("\t\t\"response\",\n")
	buf.WriteString("\t\t\"body\",\n")
	buf.WriteString("\t\t\"value\",\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tfor _, k := range candidateKeys {\n")
	buf.WriteString("\t\tif k == \"\" {\n")
	buf.WriteString("\t\t\tcontinue\n")
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t\tif val, exists := m[k]; exists {\n")
	buf.WriteString("\t\t\tif s, ok := val.(string); ok {\n")
	buf.WriteString("\t\t\t\treturn s, true\n")
	buf.WriteString("\t\t\t}\n")
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tfor _, k := range candidateKeys {\n")
	buf.WriteString("\t\tif k == \"\" {\n")
	buf.WriteString("\t\t\tcontinue\n")
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t\tif val, exists := m[k]; exists {\n")
	buf.WriteString("\t\t\tif nested, ok := val.(map[string]any); ok {\n")
	buf.WriteString("\t\t\t\tif s, ok := nested[preferredKey].(string); ok {\n")
	buf.WriteString("\t\t\t\t\treturn s, true\n")
	buf.WriteString("\t\t\t\t}\n")
	buf.WriteString("\t\t\t\tif s, ok := nested[\"value\"].(string); ok {\n")
	buf.WriteString("\t\t\t\t\treturn s, true\n")
	buf.WriteString("\t\t\t\t}\n")
	buf.WriteString("\t\t\t}\n")
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\treturn \"\", false\n")
	buf.WriteString("}\n\n")

	buf.WriteString("func normalizeGeneratedToolParams(params map[string]any, defs []tools.ToolParameter) map[string]any {\n")
	buf.WriteString("\tif len(params) == 0 || len(defs) == 0 {\n")
	buf.WriteString("\t\treturn params\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tfor _, def := range defs {\n")
	buf.WriteString("\t\tif def.Type != tools.ParamTypeString {\n")
	buf.WriteString("\t\t\tcontinue\n")
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t\tval, exists := params[def.Name]\n")
	buf.WriteString("\t\tif !exists || val == nil {\n")
	buf.WriteString("\t\t\tcontinue\n")
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t\tif _, ok := val.(string); ok {\n")
	buf.WriteString("\t\t\tcontinue\n")
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t\tif normalized, ok := extractGeneratedStringCandidate(val, def.Name); ok {\n")
	buf.WriteString("\t\t\tparams[def.Name] = normalized\n")
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\treturn params\n")
	buf.WriteString("}\n\n")

	buf.WriteString("func (a *Agent) callTool(ctx context.Context, taskID string, query string, nodeCfg map[string]any, payload map[string]any) (string, error) {\n")
	buf.WriteString("\ttoolName := \"\"\n")
	buf.WriteString("\tif nodeCfg != nil {\n")
	buf.WriteString("\t\tif v, ok := nodeCfg[\"tool_name\"].(string); ok {\n")
	buf.WriteString("\t\t\ttoolName = strings.TrimSpace(v)\n")
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tif toolName == \"\" {\n")
	buf.WriteString("\t\treturn \"\", fmt.Errorf(\"tool node missing config.tool_name\")\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tparams := map[string]any{}\n")
	buf.WriteString("\tif nodeCfg != nil {\n")
	buf.WriteString("\t\tif m, ok := nodeCfg[\"input_mapping\"].(map[string]any); ok {\n")
	buf.WriteString("\t\t\tfor targetKey, sourceKey := range m {\n")
	buf.WriteString("\t\t\t\tsrc, _ := sourceKey.(string)\n")
	buf.WriteString("\t\t\t\tsrc = strings.TrimSpace(src)\n")
	buf.WriteString("\t\t\t\tif src == \"query\" {\n")
	buf.WriteString("\t\t\t\t\tparams[targetKey] = strings.TrimSpace(query)\n")
	buf.WriteString("\t\t\t\t\tcontinue\n")
	buf.WriteString("\t\t\t\t}\n")
	buf.WriteString("\t\t\t\tif src == \"task_id\" {\n")
	buf.WriteString("\t\t\t\t\tparams[targetKey] = taskID\n")
	buf.WriteString("\t\t\t\t\tcontinue\n")
	buf.WriteString("\t\t\t\t}\n")
	buf.WriteString("\t\t\t\tif val, exists := resolveGeneratedPayloadValue(payload, src); exists {\n")
	buf.WriteString("\t\t\t\t\tif normalized, ok := extractGeneratedStringCandidate(val, targetKey); ok {\n")
	buf.WriteString("\t\t\t\t\t\tval = normalized\n")
	buf.WriteString("\t\t\t\t\t} else if _, isMap := val.(map[string]any); isMap && payload != nil {\n")
	buf.WriteString("\t\t\t\t\t\tif fallback, fallbackOK := payload[targetKey]; fallbackOK {\n")
	buf.WriteString("\t\t\t\t\t\t\tif _, fallbackIsMap := fallback.(map[string]any); !fallbackIsMap {\n")
	buf.WriteString("\t\t\t\t\t\t\t\tval = fallback\n")
	buf.WriteString("\t\t\t\t\t\t\t}\n")
	buf.WriteString("\t\t\t\t\t\t}\n")
	buf.WriteString("\t\t\t\t\t}\n")
	buf.WriteString("\t\t\t\t\tparams[targetKey] = val\n")
	buf.WriteString("\t\t\t\t\tcontinue\n")
	buf.WriteString("\t\t\t\t}\n")
	buf.WriteString("\t\t\t}\n")
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t\tif m, ok := nodeCfg[\"params\"].(map[string]any); ok {\n")
	buf.WriteString("\t\t\tfor k, v := range m {\n")
	buf.WriteString("\t\t\t\tif _, exists := params[k]; !exists {\n")
	buf.WriteString("\t\t\t\t\tparams[k] = v\n")
	buf.WriteString("\t\t\t\t}\n")
	buf.WriteString("\t\t\t}\n")
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tif _, ok := params[\"query\"]; !ok {\n")
	buf.WriteString("\t\tparams[\"query\"] = strings.TrimSpace(query)\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tif _, ok := params[\"task_id\"]; !ok {\n")
	buf.WriteString("\t\tparams[\"task_id\"] = taskID\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tif strings.EqualFold(toolName, \"tavily\") {\n")
	buf.WriteString("\t\tq, _ := params[\"query\"].(string)\n")
	buf.WriteString("\t\trawQ := q\n")
	buf.WriteString("\t\tif i := strings.LastIndex(q, \"=== 褰撳墠闂 ===\"); i >= 0 {\n")
	buf.WriteString("\t\t\tq = strings.TrimSpace(q[i+len(\"=== 褰撳墠闂 ===\"):])\n")
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t\tif i := strings.LastIndex(q, \"鐢ㄦ埛:\"); i >= 0 {\n")
	buf.WriteString("\t\t\tq = strings.TrimSpace(q[i+len(\"鐢ㄦ埛:\"):])\n")
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t\tif q == \"\" {\n")
	buf.WriteString("\t\t\tq = strings.TrimSpace(rawQ)\n")
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t\tif len(q) > 400 {\n")
	buf.WriteString("\t\t\tq = q[:400]\n")
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t\tparams[\"query\"] = q\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\ttool, err := a.findToolByName(toolName)\n")
	buf.WriteString("\tif err != nil {\n")
	buf.WriteString("\t\treturn \"\", err\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tparams = normalizeGeneratedToolParams(params, tool.Info().Parameters)\n")
	buf.WriteString("\tout, err := tool.Execute(ctx, params)\n")
	buf.WriteString("\tif err != nil {\n")
	buf.WriteString("\t\treturn \"\", err\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tresp := strings.TrimSpace(fmt.Sprintf(\"%v\", out))\n")
	buf.WriteString("\tif resp == \"\" {\n")
	buf.WriteString("\t\tresp = \"(empty tool response)\"\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\treturn resp, nil\n")
	buf.WriteString("}\n\n")

	buf.WriteString("func (a *Agent) findToolByName(name string) (tools.Tool, error) {\n")
	if len(req.Tools) == 0 {
		buf.WriteString("\t_ = name\n")
		buf.WriteString("\treturn nil, fmt.Errorf(\"no tools configured\")\n")
	} else {
		buf.WriteString("\tswitch strings.TrimSpace(name) {\n")
		for _, tool := range req.Tools {
			fieldName := toGoIdent(tool.ToolID) + "Tool"
			buf.WriteString(fmt.Sprintf("\tcase %q:\n", tool.ToolID))
			buf.WriteString(fmt.Sprintf("\t\tif a.%s == nil {\n", fieldName))
			buf.WriteString(fmt.Sprintf("\t\t\treturn nil, fmt.Errorf(\"tool %s is not initialized\")\n", tool.ToolID))
			buf.WriteString("\t\t}\n")
			buf.WriteString(fmt.Sprintf("\t\treturn a.%s, nil\n", fieldName))
		}
		buf.WriteString("\tdefault:\n")
		buf.WriteString("\t\treturn nil, fmt.Errorf(\"tool %s not found\", name)\n")
		buf.WriteString("\t}\n")
	}
	buf.WriteString("}\n\n")

	buf.WriteString("func withTaskManager(ctx context.Context, m internaltm.Manager) context.Context {\n")
	buf.WriteString("\tif ctx == nil || m == nil {\n")
	buf.WriteString("\t\treturn ctx\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\treturn context.WithValue(ctx, ctxKeyTaskManager{}, m)\n")
	buf.WriteString("}\n\n")

	buf.WriteString("func taskManagerFromContext(ctx context.Context) internaltm.Manager {\n")
	buf.WriteString("\tif ctx == nil {\n")
	buf.WriteString("\t\treturn nil\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tm, _ := ctx.Value(ctxKeyTaskManager{}).(internaltm.Manager)\n")
	buf.WriteString("\treturn m\n")
	buf.WriteString("}\n\n")

	buf.WriteString("func (a *Agent) buildNodeProgressMessage(nodeID string, stepState internalproto.StepState) string {\n")
	buf.WriteString("\tnodeID = strings.TrimSpace(nodeID)\n")
	buf.WriteString("\tnodeType := \"node\"\n")
	buf.WriteString(fmt.Sprintf("\tif v := strings.TrimSpace(%sNodeTypeByID[nodeID]); v != \"\" {\n", agentIdent))
	buf.WriteString("\t\tnodeType = v\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\t_ = stepState\n")
	buf.WriteString("\treturn fmt.Sprintf(\"节点名称:%s 节点类型:%s\", nodeID, nodeType)\n")
	buf.WriteString("}\n\n")

	buf.WriteString("func (a *Agent) startProgressReporter(ctx context.Context, taskID string, runID string, manager internaltm.Manager) func() {\n")
	buf.WriteString("\tif manager == nil || a.orchestratorEngine == nil {\n")
	buf.WriteString("\t\treturn func() {}\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tstopCh := make(chan struct{})\n")
	buf.WriteString("\tdoneCh := make(chan struct{})\n")
	buf.WriteString("\tgo func() {\n")
	buf.WriteString("\t\tdefer close(doneCh)\n")
	buf.WriteString("\t\tticker := time.NewTicker(200 * time.Millisecond)\n")
	buf.WriteString("\t\tdefer ticker.Stop()\n")
	buf.WriteString("\t\tstarted := map[string]bool{}\n")
	buf.WriteString("\t\tfinished := map[string]bool{}\n")
	buf.WriteString("\t\tfor {\n")
	buf.WriteString("\t\t\trun, err := a.orchestratorEngine.GetRun(ctx, runID)\n")
	buf.WriteString("\t\t\tif err == nil {\n")
	buf.WriteString("\t\t\t\tnodeID := strings.TrimSpace(run.CurrentNodeID)\n")
	buf.WriteString("\t\t\t\tif nodeID != \"\" && !started[nodeID] {\n")
	buf.WriteString("\t\t\t\t\tstarted[nodeID] = true\n")
	buf.WriteString("\t\t\t\t\tmessageZh := a.buildNodeProgressMessage(nodeID, internalproto.StepStateStart)\n")
	buf.WriteString(fmt.Sprintf("\t\t\t\t\tev := internalproto.NewStepEvent(%q, \"workflow\", nodeID, internalproto.StepStateStart, messageZh)\n", req.AgentID))
	buf.WriteString("\t\t\t\t\ttext := messageZh\n")
	buf.WriteString("\t\t\t\t\tif token, tokenErr := internalproto.EncodeStepToken(ev); tokenErr == nil {\n")
	buf.WriteString("\t\t\t\t\t\ttext = messageZh + \"\\n\" + token\n")
	buf.WriteString("\t\t\t\t\t}\n")
	buf.WriteString("\t\t\t\t\t_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateWorking, &internalproto.Message{\n")
	buf.WriteString("\t\t\t\t\t\tRole:  internalproto.MessageRoleAgent,\n")
	buf.WriteString("\t\t\t\t\t\tParts: []internalproto.Part{internalproto.NewTextPart(text)},\n")
	buf.WriteString("\t\t\t\t\t})\n")
	buf.WriteString("\t\t\t\t}\n")
	buf.WriteString("\t\t\t\tfor _, nr := range run.NodeResults {\n")
	buf.WriteString("\t\t\t\t\tid := strings.TrimSpace(nr.NodeID)\n")
	buf.WriteString("\t\t\t\t\tif id == \"\" || finished[id] {\n")
	buf.WriteString("\t\t\t\t\t\tcontinue\n")
	buf.WriteString("\t\t\t\t\t}\n")
	buf.WriteString("\t\t\t\t\tstepState, ok := generatedToTerminalStepState(nr.State)\n")
	buf.WriteString("\t\t\t\t\tif !ok {\n")
	buf.WriteString("\t\t\t\t\t\tcontinue\n")
	buf.WriteString("\t\t\t\t\t}\n")
	buf.WriteString("\t\t\t\t\tfinished[id] = true\n")
	buf.WriteString("\t\t\t\t\tmessageZh := a.buildNodeProgressMessage(id, stepState)\n")
	buf.WriteString(fmt.Sprintf("\t\t\t\t\tev := internalproto.NewStepEvent(%q, \"workflow\", id, stepState, messageZh)\n", req.AgentID))
	buf.WriteString("\t\t\t\t\tif token, tokenErr := internalproto.EncodeStepToken(ev); tokenErr == nil {\n")
	buf.WriteString("\t\t\t\t\t\ttext := messageZh + \"\\n\" + token\n")
	buf.WriteString("\t\t\t\t\t\t_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateWorking, &internalproto.Message{\n")
	buf.WriteString("\t\t\t\t\t\t\tRole:  internalproto.MessageRoleAgent,\n")
	buf.WriteString("\t\t\t\t\t\t\tParts: []internalproto.Part{internalproto.NewTextPart(text)},\n")
	buf.WriteString("\t\t\t\t\t\t})\n")
	buf.WriteString("\t\t\t\t\t}\n")
	buf.WriteString("\t\t\t\t}\n")
	buf.WriteString("\t\t\t\tif run.State != orchestrator.RunStateRunning {\n")
	buf.WriteString("\t\t\t\t\treturn\n")
	buf.WriteString("\t\t\t\t}\n")
	buf.WriteString("\t\t\t}\n")
	buf.WriteString("\t\t\tselect {\n")
	buf.WriteString("\t\t\tcase <-ctx.Done():\n")
	buf.WriteString("\t\t\t\treturn\n")
	buf.WriteString("\t\t\tcase <-stopCh:\n")
	buf.WriteString("\t\t\t\treturn\n")
	buf.WriteString("\t\t\tcase <-ticker.C:\n")
	buf.WriteString("\t\t\t}\n")
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t}()\n")
	buf.WriteString("\treturn func() {\n")
	buf.WriteString("\t\tclose(stopCh)\n")
	buf.WriteString("\t\t<-doneCh\n")
	buf.WriteString("\t}\n")
	buf.WriteString("}\n\n")

	buf.WriteString("func generatedToTerminalStepState(state orchestrator.TaskState) (internalproto.StepState, bool) {\n")
	buf.WriteString("\tswitch state {\n")
	buf.WriteString("\tcase orchestrator.TaskStateSucceeded:\n")
	buf.WriteString("\t\treturn internalproto.StepStateEnd, true\n")
	buf.WriteString("\tcase orchestrator.TaskStateFailed, orchestrator.TaskStateCanceled:\n")
	buf.WriteString("\t\treturn internalproto.StepStateError, true\n")
	buf.WriteString("\tdefault:\n")
	buf.WriteString("\t\treturn \"\", false\n")
	buf.WriteString("\t}\n")
	buf.WriteString("}\n\n")

	return buf.String()
}

func (g *CodeGenerator) generateWorkflowBuilder(req *AgentGenerateRequest) string {
	var buf strings.Builder
	agentIdent := toGoIdent(req.AgentID)
	routeHints := collectConditionRouteHints(req.WorkflowDef)

	buf.WriteString(fmt.Sprintf("func build%sWorkflow() (*orchestrator.Workflow, error) {\n", agentIdent))
	buf.WriteString(fmt.Sprintf("\twf, err := orchestrator.NewWorkflow(%sWorkflowID, \"%s default workflow\")\n", agentIdent, req.Name))
	buf.WriteString("\tif err != nil {\n")
	buf.WriteString("\t\treturn nil, err\n")
	buf.WriteString("\t}\n\n")

	for _, node := range req.WorkflowDef.Nodes {
		buf.WriteString(g.generateNodeCode(node, req.AgentID, routeHints[node.ID]))
	}

	for _, edge := range req.WorkflowDef.Edges {
		mappingExpr := "nil"
		if len(edge.Mapping) > 0 {
			mappingExpr = edgeMappingToGoLiteral(edge.Mapping)
		}
		buf.WriteString(fmt.Sprintf("\tif err = wf.AddEdgeWithLabel(\"%s\", \"%s\", \"%s\", %s); err != nil {\n", edge.From, edge.To, edge.Label, mappingExpr))
		buf.WriteString("\t\treturn nil, err\n")
		buf.WriteString("\t}\n")
	}

	buf.WriteString("\treturn wf, nil\n")
	buf.WriteString("}\n")

	return buf.String()
}

func (g *CodeGenerator) generateNodeCode(node executor.NodeDef, agentID string, routeHint map[string]string) string {
	var buf strings.Builder
	agentIdent := toGoIdent(agentID)

	buf.WriteString(fmt.Sprintf("\tif err = wf.AddNode(orchestrator.Node{ID: \"%s\", Type: orchestrator.NodeType%s", node.ID, toCamelCase(node.Type)))

	if node.Type == "tool" || node.Type == "chat_model" {
		// Executable nodes in generated user agents run through the locally registered workflow worker.
		buf.WriteString(fmt.Sprintf(", AgentID: %sWorkflowWorkerID", agentIdent))

		taskType := node.TaskType
		if taskType == "" {
			taskType = fmt.Sprintf("%s_default", agentID)
		}
		buf.WriteString(fmt.Sprintf(", TaskType: \"%s\"", taskType))
	}

	if node.Type == "condition" && node.Condition != "" {
		buf.WriteString(fmt.Sprintf(", Condition: \"%s\"", node.Condition))
	}

	if strings.TrimSpace(node.PreInput) != "" {
		buf.WriteString(fmt.Sprintf(", PreInput: %q", node.PreInput))
	}

	if len(node.Config) > 0 {
		buf.WriteString(fmt.Sprintf(", Config: %s", anyToGoLiteral(node.Config)))
	}

	metadata := map[string]string{}
	for k, v := range node.Metadata {
		metadata[k] = v
	}
	if node.Type == "condition" {
		if _, ok := metadata["true_to"]; !ok {
			if to := strings.TrimSpace(routeHint["true_to"]); to != "" {
				metadata["true_to"] = to
			}
		}
		if _, ok := metadata["false_to"]; !ok {
			if to := strings.TrimSpace(routeHint["false_to"]); to != "" {
				metadata["false_to"] = to
			}
		}
	}
	if len(metadata) > 0 {
		buf.WriteString(fmt.Sprintf(", Metadata: %s", mapStringStringToGoLiteral(metadata)))
	}

	if node.Type == "loop" && node.LoopConfig != nil {
		maxIter := getIntFromMap(node.LoopConfig, "max_iterations", 10)
		continueTo := getStringFromMap(node.LoopConfig, "continue_to", "")
		exitTo := getStringFromMap(node.LoopConfig, "exit_to", "")
		buf.WriteString(fmt.Sprintf(", LoopConfig: &orchestrator.LoopConfig{MaxIterations: %d, ContinueTo: \"%s\", ExitTo: \"%s\"}", maxIter, continueTo, exitTo))
	}

	buf.WriteString("}); err != nil {\n")
	buf.WriteString("\t\treturn nil, err\n")
	buf.WriteString("\t}\n")

	return buf.String()
}

func collectConditionRouteHints(def *executor.WorkflowDefinition) map[string]map[string]string {
	out := map[string]map[string]string{}
	if def == nil {
		return out
	}
	cond := map[string]bool{}
	for _, n := range def.Nodes {
		if strings.EqualFold(strings.TrimSpace(n.Type), "condition") {
			cond[n.ID] = true
		}
	}
	for _, e := range def.Edges {
		if !cond[e.From] {
			continue
		}
		label := strings.ToLower(strings.TrimSpace(e.Label))
		if label != "true" && label != "false" {
			continue
		}
		if out[e.From] == nil {
			out[e.From] = map[string]string{}
		}
		key := label + "_to"
		if strings.TrimSpace(out[e.From][key]) == "" {
			out[e.From][key] = e.To
		}
	}
	return out
}

func anyToGoLiteral(v any) string {
	switch vv := v.(type) {
	case nil:
		return "nil"
	case string:
		return fmt.Sprintf("%q", vv)
	case bool:
		if vv {
			return "true"
		}
		return "false"
	case int:
		return fmt.Sprintf("%d", vv)
	case int8:
		return fmt.Sprintf("%d", vv)
	case int16:
		return fmt.Sprintf("%d", vv)
	case int32:
		return fmt.Sprintf("%d", vv)
	case int64:
		return fmt.Sprintf("%d", vv)
	case uint:
		return fmt.Sprintf("%d", vv)
	case uint8:
		return fmt.Sprintf("%d", vv)
	case uint16:
		return fmt.Sprintf("%d", vv)
	case uint32:
		return fmt.Sprintf("%d", vv)
	case uint64:
		return fmt.Sprintf("%d", vv)
	case float32:
		return fmt.Sprintf("%v", vv)
	case float64:
		return fmt.Sprintf("%v", vv)
	case map[string]any:
		return mapStringAnyToGoLiteral(vv)
	case map[string]string:
		return mapStringStringToGoLiteral(vv)
	case []any:
		parts := make([]string, 0, len(vv))
		for _, item := range vv {
			parts = append(parts, anyToGoLiteral(item))
		}
		return "[]any{" + strings.Join(parts, ", ") + "}"
	case []string:
		parts := make([]string, 0, len(vv))
		for _, item := range vv {
			parts = append(parts, fmt.Sprintf("%q", item))
		}
		return "[]string{" + strings.Join(parts, ", ") + "}"
	default:
		return fmt.Sprintf("%q", fmt.Sprintf("%v", vv))
	}
}

func mapStringAnyToGoLiteral(m map[string]any) string {
	if len(m) == 0 {
		return "map[string]any{}"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%q: %s", k, anyToGoLiteral(m[k])))
	}
	return "map[string]any{" + strings.Join(parts, ", ") + "}"
}

func mapStringStringToGoLiteral(m map[string]string) string {
	if len(m) == 0 {
		return "map[string]string{}"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%q: %q", k, m[k]))
	}
	return "map[string]string{" + strings.Join(parts, ", ") + "}"
}

func edgeMappingToGoLiteral(m map[string]any) string {
	if len(m) == 0 {
		return "nil"
	}
	mm := make(map[string]string, len(m))
	for k, v := range m {
		mm[k] = fmt.Sprint(v)
	}
	return mapStringStringToGoLiteral(mm)
}

func (g *CodeGenerator) generateServerCode(req *AgentGenerateRequest) string {
	packageName := sanitizePackageName(req.AgentID)

	var buf strings.Builder
	buf.WriteString("// Code generated by codegen. DO NOT EDIT.\n")
	buf.WriteString(fmt.Sprintf("// Generated at: %s\n\n", time.Now().Format(time.RFC3339)))
	buf.WriteString(fmt.Sprintf("package %s\n\n", packageName))

	buf.WriteString("import (\n")
	buf.WriteString("\t\"context\"\n")
	buf.WriteString("\t\"fmt\"\n")
	buf.WriteString("\t\"net/http\"\n")
	buf.WriteString("\n")
	buf.WriteString("\t\"ai/pkg/protocol\"\n")
	buf.WriteString("\t\"ai/pkg/taskmanager\"\n")
	buf.WriteString("\t\"ai/pkg/transport/httpagent\"\n")
	buf.WriteString(")\n\n")

	buf.WriteString("type internalProcessor struct {\n")
	buf.WriteString("\tagent *Agent\n")
	buf.WriteString("}\n\n")

	buf.WriteString("func (p *internalProcessor) ProcessMessage(ctx context.Context, message protocol.Message,\n")
	buf.WriteString("\tmanager taskmanager.Manager) (string, <-chan protocol.StreamEvent, error) {\n")
	buf.WriteString("\ttaskID, err := manager.BuildTask(message.TaskID, nil)\n")
	buf.WriteString("\tif err != nil {\n")
	buf.WriteString("\t\treturn \"\", nil, fmt.Errorf(\"failed to build task: %w\", err)\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tsubscriber, err := manager.SubscribeTask(ctx, taskID)\n")
	buf.WriteString("\tif err != nil {\n")
	buf.WriteString("\t\treturn \"\", nil, fmt.Errorf(\"failed to subscribe task: %w\", err)\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tgo func() {\n")
	buf.WriteString("\t\t_ = manager.UpdateTaskState(ctx, taskID, protocol.TaskStateWorking, nil)\n")
	buf.WriteString("\t\tif runErr := p.agent.ProcessInternal(ctx, taskID, message, manager); runErr != nil {\n")
	buf.WriteString("\t\t\t_ = manager.UpdateTaskState(ctx, taskID, protocol.TaskStateFailed, &protocol.Message{\n")
	buf.WriteString("\t\t\t\tRole:  protocol.MessageRoleAgent,\n")
	buf.WriteString("\t\t\t\tParts: []protocol.Part{protocol.NewTextPart(runErr.Error())},\n")
	buf.WriteString("\t\t\t})\n")
	buf.WriteString("\t\t}\n")
	buf.WriteString("\t}()\n")
	buf.WriteString("\treturn taskID, subscriber, nil\n")
	buf.WriteString("}\n\n")

	buf.WriteString("func NewHTTPServer(agent *Agent) (http.Handler, error) {\n")
	buf.WriteString("\tcard := protocol.AgentCard{\n")
	buf.WriteString(fmt.Sprintf("\t\tName:        \"%s\",\n", sanitizePackageName(req.AgentID)))
	buf.WriteString(fmt.Sprintf("\t\tDescription: \"%s\",\n", req.Description))
	buf.WriteString("\t\tVersion:     \"0.0.1\",\n")
	buf.WriteString("\t\tProvider:    &protocol.AgentProvider{Organization: \"user_defined\"},\n")
	buf.WriteString("\t\tCapabilities: protocol.AgentCapabilities{\n")
	buf.WriteString("\t\t\tPushNotifications:      boolPtr(true),\n")
	buf.WriteString("\t\t\tStateTransitionHistory: boolPtr(true),\n")
	buf.WriteString("\t\t},\n")
	buf.WriteString("\t\tDefaultInputModes:  []string{\"text\"},\n")
	buf.WriteString("\t\tDefaultOutputModes: []string{\"text\"},\n")
	buf.WriteString(fmt.Sprintf("\t\tSkills: []protocol.AgentSkill{{\n"))
	buf.WriteString(fmt.Sprintf("\t\t\tID:          \"%s\",\n", req.AgentID))
	buf.WriteString(fmt.Sprintf("\t\t\tName:        \"%s\",\n", req.Name))
	buf.WriteString(fmt.Sprintf("\t\t\tDescription: stringPtr(\"%s\"),\n", req.Description))
	buf.WriteString("\t\t\tTags:        []string{\"user_defined\"},\n")
	buf.WriteString("\t\t\tInputModes:  []string{\"text\"},\n")
	buf.WriteString("\t\t\tOutputModes: []string{\"text\"},\n")
	buf.WriteString("\t\t}},\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\tmgr := taskmanager.NewInMemoryManager()\n")
	buf.WriteString("\tsrv, err := httpagent.NewServer(card, mgr, &internalProcessor{agent: agent})\n")
	buf.WriteString("\tif err != nil {\n")
	buf.WriteString("\t\treturn nil, err\n")
	buf.WriteString("\t}\n")
	buf.WriteString("\treturn srv.Handler(), nil\n")
	buf.WriteString("}\n\n")

	buf.WriteString("func stringPtr(s string) *string { return &s }\n")
	buf.WriteString("func boolPtr(b bool) *bool { return &b }\n")

	return buf.String()
}

func (g *CodeGenerator) generateToolCode(tool *ToolDefinition) string {
	var buf strings.Builder
	buf.WriteString("// Code generated by codegen. DO NOT EDIT.\n")
	buf.WriteString(fmt.Sprintf("// Generated at: %s\n\n", time.Now().Format(time.RFC3339)))
	buf.WriteString("package tools\n\n")

	needTimeImport := false
	if tool.ToolType == tools.ToolTypeHTTP && tool.Config != nil {
		_, needTimeImport = tool.Config["timeout"].(float64)
	}
	if needTimeImport {
		buf.WriteString("import (\n")
		buf.WriteString("\t\"time\"\n")
		buf.WriteString(")\n\n")
	}

	buf.WriteString(fmt.Sprintf("// NewUserGenerated%sTool builds a runtime tool from user definition %q.\n", toCamelCase(tool.ToolID), tool.ToolID))
	buf.WriteString(fmt.Sprintf("func NewUserGenerated%sTool() Tool {\n", toCamelCase(tool.ToolID)))
	buf.WriteString("\tparams := []ToolParameter{\n")
	for _, p := range tool.Parameters {
		buf.WriteString("\t\t{\n")
		buf.WriteString(fmt.Sprintf("\t\t\tName: %q,\n", p.Name))
		buf.WriteString(fmt.Sprintf("\t\t\tType: %s,\n", parameterTypeConst(p.Type)))
		buf.WriteString(fmt.Sprintf("\t\t\tRequired: %t,\n", p.Required))
		buf.WriteString(fmt.Sprintf("\t\t\tDescription: %q,\n", p.Description))
		if p.Default != nil {
			buf.WriteString(fmt.Sprintf("\t\t\tDefault: %s,\n", anyToGoLiteral(p.Default)))
		}
		if len(p.Enum) > 0 {
			buf.WriteString(fmt.Sprintf("\t\t\tEnum: %s,\n", anyToGoLiteral(p.Enum)))
		}
		buf.WriteString("\t\t},\n")
	}
	buf.WriteString("\t}\n")
	if tool.ToolType == tools.ToolTypeHTTP {
		buf.WriteString("\tconfig := HTTPToolConfig{\n")
		if tool.Config != nil {
			if method, ok := tool.Config["method"].(string); ok {
				buf.WriteString(fmt.Sprintf("\t\tMethod: %q,\n", method))
			}
			if url, ok := tool.Config["url"].(string); ok {
				buf.WriteString(fmt.Sprintf("\t\tURL: %q,\n", url))
			}
			if bodyTemplate, ok := tool.Config["body_template"].(string); ok && strings.TrimSpace(bodyTemplate) != "" {
				buf.WriteString(fmt.Sprintf("\t\tBodyTemplate: %q,\n", bodyTemplate))
			}
		}
		if timeout, ok := tool.Config["timeout"].(float64); ok {
			buf.WriteString(fmt.Sprintf("\t\tTimeout: time.Duration(%d) * time.Second,\n", int(timeout)))
		}
		buf.WriteString("\t}\n")
		if tool.Config != nil {
			if headers, ok := tool.Config["headers"].(map[string]any); ok && len(headers) > 0 {
				buf.WriteString("\tconfig.Headers = map[string]string{\n")
				for k, v := range headers {
					buf.WriteString(fmt.Sprintf("\t\t%q: %q,\n", k, fmt.Sprintf("%v", v)))
				}
				buf.WriteString("\t}\n")
			}
		}
		buf.WriteString(fmt.Sprintf("\treturn NewHTTPTool(%q, %q, params, config)\n", tool.Name, tool.Description))
	} else if tool.ToolType == tools.ToolTypeMCP {
		buf.WriteString("\tconfig := MCPToolConfig{\n")
		if tool.Config != nil {
			if serverURL, ok := tool.Config["server_url"].(string); ok {
				buf.WriteString(fmt.Sprintf("\t\tServerURL: %q,\n", serverURL))
			}
			if toolName, ok := tool.Config["tool_name"].(string); ok {
				buf.WriteString(fmt.Sprintf("\t\tToolName: %q,\n", toolName))
			}
		}
		buf.WriteString("\t}\n")
		buf.WriteString(fmt.Sprintf("\treturn NewMCPTool(%q, %q, params, config)\n", tool.Name, tool.Description))
	} else {
		buf.WriteString("\treturn nil\n")
	}
	buf.WriteString("}\n\n")

	return buf.String()
}

func parameterTypeConst(pt tools.ParameterType) string {
	switch pt {
	case tools.ParamTypeNumber:
		return "ParamTypeNumber"
	case tools.ParamTypeBoolean:
		return "ParamTypeBoolean"
	case tools.ParamTypeObject:
		return "ParamTypeObject"
	case tools.ParamTypeArray:
		return "ParamTypeArray"
	default:
		return "ParamTypeString"
	}
}

func parameterTypeConstQualified(pt tools.ParameterType) string {
	return "tools." + parameterTypeConst(pt)
}

func toolParametersExpr(params []tools.ToolParameter) string {
	if len(params) == 0 {
		return "[]tools.ToolParameter{}"
	}
	var b strings.Builder
	b.WriteString("[]tools.ToolParameter{")
	for _, p := range params {
		b.WriteString("{")
		b.WriteString(fmt.Sprintf("Name: %q, Type: %s, Required: %t, Description: %q", p.Name, parameterTypeConstQualified(p.Type), p.Required, p.Description))
		if p.Default != nil {
			b.WriteString(fmt.Sprintf(", Default: %s", anyToGoLiteral(p.Default)))
		}
		if len(p.Enum) > 0 {
			b.WriteString(fmt.Sprintf(", Enum: %s", anyToGoLiteral(p.Enum)))
		}
		b.WriteString("},")
	}
	b.WriteString("}")
	return b.String()
}

func sanitizePackageName(name string) string {
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, "-", "_")
	name = strings.ReplaceAll(name, " ", "_")
	name = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '_'
	}, name)
	if name == "" {
		return "user_agent"
	}
	if name[0] >= '0' && name[0] <= '9' {
		name = "p_" + name
	}
	return name
}

func legacySanitizePackageName(name string) string {
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, "-", "_")
	name = strings.ReplaceAll(name, " ", "_")
	name = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '_'
	}, name)
	if name == "" {
		return "user_agent"
	}
	return name
}

func toGoIdent(s string) string {
	id := toCamelCase(s)
	if id == "" {
		return "Generated"
	}
	b := id[0]
	if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || b == '_' {
		return id
	}
	return "X" + id
}

func toCamelCase(s string) string {
	s = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, s)
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '_' })
	for i := range parts {
		if len(parts[i]) > 0 {
			parts[i] = strings.ToUpper(string(parts[i][0])) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

func getStringFromMap(m map[string]any, key string, defaultValue string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return defaultValue
}

func getFloatFromMap(m map[string]any, key string, defaultValue float64) float64 {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return n
		case float32:
			return float64(n)
		case int:
			return float64(n)
		case int64:
			return float64(n)
		}
	}
	return defaultValue
}

func getIntFromMap(m map[string]any, key string, defaultValue int) int {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case int:
			return n
		case int64:
			return int(n)
		case float64:
			return int(n)
		}
	}
	return defaultValue
}
