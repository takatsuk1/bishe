package financehelper

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai/pkg/llm"
	"ai/pkg/orchestrator"
	"ai/pkg/protocol"
	"ai/pkg/taskmanager"
	"ai/pkg/tools"
	"ai/pkg/transport/httpagent"
)

func TestBuildFinanceHelperWorkflow(t *testing.T) {
	wf, err := buildFinanceHelperWorkflow()
	if err != nil {
		t.Fatalf("build workflow failed: %v", err)
	}
	if wf.StartNodeID != "N_start" {
		t.Fatalf("unexpected start node: %s", wf.StartNodeID)
	}
	if len(wf.Nodes) != 12 {
		t.Fatalf("unexpected node count: %d", len(wf.Nodes))
	}
}

func TestFinanceHelperProcessInternal_Ledger(t *testing.T) {
	llmServer := newMockFinanceLLMServer(t, func(prompt string) string {
		switch {
		case strings.Contains(prompt, "You are financehelper planner."):
			return `{"action":"ledger","summary":"记录午餐账单","ensure_table_sql":"CREATE TABLE IF NOT EXISTS __USER_BILL_TABLE__ (id BIGINT PRIMARY KEY AUTO_INCREMENT, bill_date DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, amount DECIMAL(18,2) NOT NULL DEFAULT 0, category VARCHAR(64) NOT NULL DEFAULT '餐饮', bill_type VARCHAR(16) NOT NULL DEFAULT 'expense', note VARCHAR(255) NOT NULL DEFAULT '', raw_text TEXT NULL, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP)","sql_statements":["INSERT INTO __USER_BILL_TABLE__ (bill_date, amount, category, bill_type, note, raw_text) VALUES ('2026-03-31 12:00:00', 30.00, '餐饮', 'expense', '午饭', '今天午饭30元')"]}`
		case strings.Contains(prompt, "You are financehelper responder."):
			return "已记录账单：今天午饭 30 元，分类为餐饮支出。"
		default:
			return ""
		}
	})

	sqlCalls := make([]string, 0, 2)
	mysqlTool := tools.NewRawHTTPTool("mysql_exec", "mock mysql", []tools.ToolParameter{
		{Name: "sql", Type: tools.ParamTypeString, Required: true, Description: "sql"},
	}, func(ctx context.Context, params map[string]any) (map[string]any, error) {
		_ = ctx
		sql := strings.TrimSpace(params["sql"].(string))
		sqlCalls = append(sqlCalls, sql)
		if strings.HasPrefix(strings.ToUpper(sql), "CREATE TABLE") {
			return map[string]any{"content": `{"type":"exec","rows_affected":0}`}, nil
		}
		return map[string]any{"content": `{"type":"exec","rows_affected":1,"last_insert_id":1}`}, nil
	})

	a := &Agent{
		llmClient: llm.NewClient(llmServer.URL, ""),
		chatModel: "mock",
		MySQLTool: mysqlTool,
		AkshareTool: tools.NewRawHTTPTool("akshare-one-mcp", "mock akshare", nil, func(ctx context.Context, params map[string]any) (map[string]any, error) {
			return map[string]any{"content": `[]`}, nil
		}),
	}
	setupFinanceHelperEngine(t, a)

	ctx := context.Background()
	mgr := taskmanager.NewInMemoryManager()
	taskID, err := mgr.BuildTask(nil, nil)
	if err != nil {
		t.Fatalf("build task failed: %v", err)
	}
	_ = mgr.UpdateTaskState(ctx, taskID, protocol.TaskStateWorking, nil)

	msg := protocol.Message{
		Role:     protocol.MessageRoleUser,
		Parts:    []protocol.Part{protocol.NewTextPart("今天午饭30元")},
		Metadata: map[string]any{"user_id": "user_01"},
	}
	if err = a.ProcessInternal(ctx, taskID, msg, mgr); err != nil {
		t.Fatalf("ProcessInternal failed: %v", err)
	}
	task, err := mgr.GetTask(ctx, taskID)
	if err != nil {
		t.Fatalf("get task failed: %v", err)
	}
	if task.Status.State != protocol.TaskStateCompleted {
		t.Fatalf("unexpected task state: %s", task.Status.State)
	}
	if task.Status.Message == nil || task.Status.Message.FirstText() != "已记录账单：今天午饭 30 元，分类为餐饮支出。" {
		t.Fatalf("unexpected task output: %#v", task.Status.Message)
	}
	if len(sqlCalls) < 2 {
		t.Fatalf("unexpected sql call count: %d", len(sqlCalls))
	}
	hasCreate := false
	hasInsert := false
	for _, sql := range sqlCalls {
		if strings.Contains(sql, "finance_bill_user_01") && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(sql)), "CREATE TABLE") {
			hasCreate = true
		}
		if strings.Contains(sql, "finance_bill_user_01") && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(sql)), "INSERT INTO") {
			hasInsert = true
		}
	}
	if !hasCreate || !hasInsert {
		t.Fatalf("user table name not applied: %#v", sqlCalls)
	}
}

func TestFinanceHelperHTTPServer_Advice(t *testing.T) {
	llmServer := newMockFinanceLLMServer(t, func(prompt string) string {
		switch {
		case strings.Contains(prompt, "You are financehelper planner."):
			return `{"action":"advice","summary":"生成理财建议","ensure_table_sql":"CREATE TABLE IF NOT EXISTS __USER_BILL_TABLE__ (id BIGINT PRIMARY KEY AUTO_INCREMENT, bill_date DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, amount DECIMAL(18,2) NOT NULL DEFAULT 0, category VARCHAR(64) NOT NULL DEFAULT '未分类', bill_type VARCHAR(16) NOT NULL DEFAULT 'expense', note VARCHAR(255) NOT NULL DEFAULT '', raw_text TEXT NULL, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP)","sql_statements":["SELECT * FROM __USER_BILL_TABLE__ ORDER BY bill_date DESC LIMIT 5"],"akshare_tool_name":"stock_news_main_cx","akshare_arguments":{"query":"宏观经济 市场 利率","limit":5}}`
		case strings.Contains(prompt, "You are financehelper responder."):
			return "根据你的近期收支与当前市场信息，建议先保留 3-6 个月应急资金，再分批配置稳健型资产。"
		default:
			return ""
		}
	})

	mysqlTool := tools.NewRawHTTPTool("mysql_exec", "mock mysql", []tools.ToolParameter{
		{Name: "sql", Type: tools.ParamTypeString, Required: true, Description: "sql"},
	}, func(ctx context.Context, params map[string]any) (map[string]any, error) {
		_ = ctx
		sql := strings.TrimSpace(params["sql"].(string))
		if strings.HasPrefix(strings.ToUpper(sql), "CREATE TABLE") {
			return map[string]any{"content": `{"type":"exec","rows_affected":0}`}, nil
		}
		return map[string]any{"content": `{"type":"rows","rows":[{"bill_type":"expense","category":"餐饮","amount":"30.00"}]}`}, nil
	})
	akshareTool := tools.NewRawHTTPTool("akshare-one-mcp", "mock akshare", []tools.ToolParameter{
		{Name: "tool_name", Type: tools.ParamTypeString, Required: true, Description: "tool"},
	}, func(ctx context.Context, params map[string]any) (map[string]any, error) {
		_ = ctx
		return map[string]any{"content": `[{"title":"央行政策信号","summary":"市场关注利率路径"}]`}, nil
	})

	a := &Agent{
		llmClient:   llm.NewClient(llmServer.URL, ""),
		chatModel:   "mock",
		MySQLTool:   mysqlTool,
		AkshareTool: akshareTool,
	}
	setupFinanceHelperEngine(t, a)

	h, err := NewHTTPServer(a)
	if err != nil {
		t.Fatalf("NewHTTPServer failed: %v", err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := httpagent.NewClient(srv.URL, 5*time.Second)
	ctx := context.Background()
	taskID, err := client.SendMessage(ctx, protocol.Message{
		Role:     protocol.MessageRoleUser,
		Parts:    []protocol.Part{protocol.NewTextPart("请结合我最近账单给出理财建议")},
		Metadata: map[string]any{"user_id": "u100"},
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		task, getErr := client.GetTask(ctx, taskID)
		if getErr == nil && task != nil && task.Status.State.IsTerminal() {
			if task.Status.State != protocol.TaskStateCompleted {
				t.Fatalf("unexpected task state: %s", task.Status.State)
			}
			if task.Status.Message == nil || task.Status.Message.FirstText() != "根据你的近期收支与当前市场信息，建议先保留 3-6 个月应急资金，再分批配置稳健型资产。" {
				t.Fatalf("unexpected task output: %#v", task.Status.Message)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("task did not finish in time")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func setupFinanceHelperEngine(t *testing.T, a *Agent) {
	t.Helper()
	eng := orchestrator.NewEngine(orchestrator.Config{DefaultTaskTimeoutSec: 30, RetryMaxAttempts: 1}, orchestrator.NewInMemoryAgentRegistry())
	a.orchestratorEngine = eng
	if err := a.orchestratorEngine.RegisterWorker(orchestrator.AgentDescriptor{ID: FinanceHelperWorkflowWorkerID, Name: "test", Capabilities: []orchestrator.AgentCapability{"chat_model", "tool"}}, &workflowNodeWorker{agent: a}); err != nil {
		t.Fatalf("register worker failed: %v", err)
	}
	wf, err := buildFinanceHelperWorkflow()
	if err != nil {
		t.Fatalf("build workflow failed: %v", err)
	}
	if err = a.orchestratorEngine.RegisterWorkflow(wf); err != nil {
		t.Fatalf("register workflow failed: %v", err)
	}
}

func newMockFinanceLLMServer(t *testing.T, pick func(prompt string) string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		prompt := ""
		if len(req.Messages) > 0 {
			prompt = req.Messages[len(req.Messages)-1].Content
		}
		resp := map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"content": pick(prompt)},
			}},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(server.Close)
	return server
}
