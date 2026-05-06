package main

import (
	"ai/api/auth"
	monitorapi "ai/api/monitor"
	"ai/api/orchestrator"
	"ai/config"
	authsvc "ai/pkg/auth"
	"ai/pkg/monitor"
	"ai/pkg/storage"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

type endpointSpec struct {
	Name        string
	Method      string
	Path        string
	Body        []byte
	NeedsAuth   bool
	ExpectCodes map[int]bool
}

type endpointResult struct {
	Name           string        `json:"name"`
	Method         string        `json:"method"`
	Path           string        `json:"path"`
	Concurrency    int           `json:"concurrency"`
	Duration       time.Duration `json:"duration"`
	TotalRequests  int64         `json:"totalRequests"`
	Successes      int64         `json:"successes"`
	Failures       int64         `json:"failures"`
	RequestsPerSec float64       `json:"requestsPerSec"`
	AvgLatencyMs   float64       `json:"avgLatencyMs"`
	P50LatencyMs   float64       `json:"p50LatencyMs"`
	P95LatencyMs   float64       `json:"p95LatencyMs"`
	P99LatencyMs   float64       `json:"p99LatencyMs"`
	MaxLatencyMs   float64       `json:"maxLatencyMs"`
}

type envData struct {
	Username   string
	Password   string
	UserID     string
	Token      string
	WorkflowID string
	RunID      string
}

func main() {
	gin.SetMode(gin.ReleaseMode)
	config.CmdlineFlags.ConfigProvider = "file"
	config.CmdlineFlags.MainConfigFilename = "config.yaml"
	config.Init()

	cfg := config.GetMainConfig()
	ctx := context.Background()

	rawDB, err := sql.Open("mysql", cfg.MySQL.DSN)
	if err != nil {
		fatalf("open mysql failed: %v", err)
	}
	defer rawDB.Close()
	if err := rawDB.PingContext(ctx); err != nil {
		fatalf("ping mysql failed: %v", err)
	}

	projectRoot, err := os.Getwd()
	if err != nil {
		fatalf("getwd failed: %v", err)
	}
	if err := applySchemaFiles(ctx, rawDB, []string{
		filepath.Join(projectRoot, "scripts", "schema.sql"),
		filepath.Join(projectRoot, "scripts", "monitor_schema.sql"),
	}); err != nil {
		fatalf("apply schemas failed: %v", err)
	}

	mysqlStorage, err := storage.InitMySQL(cfg.MySQL.DSN)
	if err != nil {
		fatalf("init mysql storage failed: %v", err)
	}
	if err := mysqlStorage.EnsureDefaultRoles(ctx); err != nil {
		fatalf("ensure default roles failed: %v", err)
	}

	authService, err := authsvc.NewService(
		mysqlStorage,
		cfg.Auth.JWTSecret,
		time.Duration(cfg.Auth.AccessTokenTTLMinutes)*time.Minute,
		time.Duration(cfg.Auth.RefreshTokenTTLHours)*time.Hour,
	)
	if err != nil {
		fatalf("init auth service failed: %v", err)
	}

	testEnv, cleanup, err := seedTestData(ctx, mysqlStorage, authService)
	if err != nil {
		fatalf("seed test data failed: %v", err)
	}
	defer cleanup()

	server := httptest.NewServer(buildHandler(authService, mysqlStorage))
	defer server.Close()

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        256,
			MaxIdleConnsPerHost: 256,
			MaxConnsPerHost:     0,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	loginPayload, _ := json.Marshal(map[string]string{
		"username": testEnv.Username,
		"password": testEnv.Password,
	})

	specs := []endpointSpec{
		{
			Name:   "auth_login",
			Method: http.MethodPost,
			Path:   "/v1/auth/login",
			Body:   loginPayload,
			ExpectCodes: map[int]bool{
				http.StatusOK: true,
			},
		},
		{
			Name:      "auth_me",
			Method:    http.MethodGet,
			Path:      "/v1/auth/me",
			NeedsAuth: true,
			ExpectCodes: map[int]bool{
				http.StatusOK: true,
			},
		},
		{
			Name:      "orchestrator_agents",
			Method:    http.MethodGet,
			Path:      "/v1/orchestrator/agents",
			NeedsAuth: true,
			ExpectCodes: map[int]bool{
				http.StatusOK: true,
			},
		},
		{
			Name:      "orchestrator_workflows",
			Method:    http.MethodGet,
			Path:      "/v1/orchestrator/workflows",
			NeedsAuth: true,
			ExpectCodes: map[int]bool{
				http.StatusOK: true,
			},
		},
		{
			Name:      "monitor_overview",
			Method:    http.MethodGet,
			Path:      "/v1/monitor/overview",
			NeedsAuth: true,
			ExpectCodes: map[int]bool{
				http.StatusOK: true,
			},
		},
	}

	const concurrency = 32
	const duration = 8 * time.Second

	results := make([]endpointResult, 0, len(specs))
	for _, spec := range specs {
		fmt.Printf("Running %s %s for %s at concurrency=%d\n", spec.Method, spec.Path, duration, concurrency)
		result, runErr := runLoad(client, server.URL, spec, testEnv.Token, concurrency, duration)
		if runErr != nil {
			fatalf("load test failed for %s: %v", spec.Name, runErr)
		}
		results = append(results, result)
	}

	fmt.Println()
	fmt.Printf("Load test timestamp: %s\n", time.Now().Format(time.RFC3339))
	fmt.Printf("Database: MySQL reachable, Redis not used in this run\n")
	fmt.Printf("Profile: duration=%s concurrency=%d\n", duration, concurrency)
	fmt.Println()
	fmt.Println("RESULTS_JSON_START")
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(results)
	fmt.Println("RESULTS_JSON_END")
	fmt.Println()
	printSummaryTable(results)
}

func buildHandler(authService *authsvc.Service, mysqlStorage *storage.MySQLStorage) http.Handler {
	authRouter := gin.New()
	auth.NewAPI(authService).RegisterRoutes(authRouter)

	authMiddleware := authsvc.Middleware(authService)
	orchestratorHandler := authMiddleware(orchestrator.NewOrchestratorAPI().Handler())
	monitorHandler := authMiddleware(monitorapi.NewAPI(monitor.NewService(mysqlStorage, nil)).Handler())

	root := http.NewServeMux()
	root.Handle("/v1/auth/", authRouter)
	root.Handle("/v1/orchestrator/", orchestratorHandler)
	root.Handle("/v1/monitor/", monitorHandler)
	return root
}

func runLoad(client *http.Client, baseURL string, spec endpointSpec, token string, concurrency int, duration time.Duration) (endpointResult, error) {
	if concurrency <= 0 {
		return endpointResult{}, fmt.Errorf("concurrency must be positive")
	}
	if duration <= 0 {
		return endpointResult{}, fmt.Errorf("duration must be positive")
	}

	stopAt := time.Now().Add(duration)
	latencies := make([]time.Duration, 0, 2048)
	var latMu sync.Mutex
	var totalReq int64
	var okReq int64
	var failReq int64
	var totalLatencyNs int64

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(stopAt) {
				req, err := http.NewRequest(spec.Method, baseURL+spec.Path, bytes.NewReader(spec.Body))
				if err != nil {
					atomic.AddInt64(&failReq, 1)
					continue
				}
				if len(spec.Body) > 0 {
					req.Header.Set("Content-Type", "application/json")
				}
				if spec.NeedsAuth {
					req.Header.Set("Authorization", "Bearer "+token)
				}

				start := time.Now()
				resp, err := client.Do(req)
				latency := time.Since(start)

				atomic.AddInt64(&totalReq, 1)
				atomic.AddInt64(&totalLatencyNs, latency.Nanoseconds())
				latMu.Lock()
				latencies = append(latencies, latency)
				latMu.Unlock()

				if err != nil {
					atomic.AddInt64(&failReq, 1)
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()

				if spec.ExpectCodes[resp.StatusCode] {
					atomic.AddInt64(&okReq, 1)
				} else {
					atomic.AddInt64(&failReq, 1)
				}
			}
		}()
	}
	wg.Wait()

	total := atomic.LoadInt64(&totalReq)
	okCount := atomic.LoadInt64(&okReq)
	failCount := atomic.LoadInt64(&failReq)
	if total == 0 {
		return endpointResult{}, fmt.Errorf("no requests recorded for %s", spec.Name)
	}

	latMu.Lock()
	sorted := append([]time.Duration(nil), latencies...)
	latMu.Unlock()
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	totalLatencyNsValue := atomic.LoadInt64(&totalLatencyNs)
	avgLatencyMs := float64(totalLatencyNsValue) / float64(total) / float64(time.Millisecond)

	return endpointResult{
		Name:           spec.Name,
		Method:         spec.Method,
		Path:           spec.Path,
		Concurrency:    concurrency,
		Duration:       duration,
		TotalRequests:  total,
		Successes:      okCount,
		Failures:       failCount,
		RequestsPerSec: float64(total) / duration.Seconds(),
		AvgLatencyMs:   avgLatencyMs,
		P50LatencyMs:   percentileMs(sorted, 0.50),
		P95LatencyMs:   percentileMs(sorted, 0.95),
		P99LatencyMs:   percentileMs(sorted, 0.99),
		MaxLatencyMs:   float64(sorted[len(sorted)-1]) / float64(time.Millisecond),
	}, nil
}

func percentileMs(values []time.Duration, ratio float64) float64 {
	if len(values) == 0 {
		return 0
	}
	if ratio <= 0 {
		return float64(values[0]) / float64(time.Millisecond)
	}
	if ratio >= 1 {
		return float64(values[len(values)-1]) / float64(time.Millisecond)
	}
	index := int(float64(len(values)-1) * ratio)
	return float64(values[index]) / float64(time.Millisecond)
}

func seedTestData(ctx context.Context, mysqlStorage *storage.MySQLStorage, authService *authsvc.Service) (*envData, func(), error) {
	username := fmt.Sprintf("perf_user_%d", time.Now().UnixNano())
	password := "Perf123456"
	displayName := "Perf User"

	user, tokens, err := authService.Register(ctx, username, password, displayName)
	if err != nil {
		return nil, nil, err
	}

	workflowID := fmt.Sprintf("perf_workflow_%d", time.Now().UnixNano())
	runID := fmt.Sprintf("perf_run_%d", time.Now().UnixNano())
	now := time.Now().UTC()

	workflowDef := &storage.WorkflowDefinition{
		WorkflowID:  workflowID,
		UserID:      user.UserID,
		Name:        "Performance Workflow",
		Description: "Seeded workflow for API performance testing",
		StartNodeID: "start",
		Nodes: []storage.NodeDef{
			{ID: "start", Type: "start"},
			{ID: "end", Type: "end"},
		},
		Edges: []storage.EdgeDef{
			{From: "start", To: "end"},
		},
	}
	if err := mysqlStorage.SaveWorkflow(ctx, workflowDef); err != nil {
		return nil, nil, err
	}

	run := &storage.MonitorRun{
		RunID:      runID,
		WorkflowID: workflowID,
		UserID:     user.UserID,
		Status:     "succeeded",
		StartedAt:  now.Add(-2 * time.Minute),
		DurationMs: 120,
		AlertCount: 1,
	}
	if err := mysqlStorage.CreateMonitorRun(ctx, run); err != nil {
		return nil, nil, err
	}
	if err := mysqlStorage.FinishMonitorRun(ctx, runID, "succeeded", now.Add(-2*time.Minute+120*time.Millisecond), 120, ""); err != nil {
		return nil, nil, err
	}

	event := &storage.MonitorEvent{
		EventID:    fmt.Sprintf("perf_event_%d", time.Now().UnixNano()),
		RunID:      runID,
		WorkflowID: workflowID,
		UserID:     user.UserID,
		NodeID:     "end",
		EventType:  "node_completed",
		Status:     "succeeded",
		Message:    "seeded event",
		DurationMs: 120,
	}
	if err := mysqlStorage.CreateMonitorEvent(ctx, event); err != nil {
		return nil, nil, err
	}

	alert := &storage.MonitorAlert{
		AlertID:     fmt.Sprintf("perf_alert_%d", time.Now().UnixNano()),
		RunID:       runID,
		WorkflowID:  workflowID,
		NodeID:      "end",
		AlertType:   "slow_node",
		Severity:    "low",
		Title:       "Seeded alert",
		Content:     "seeded for performance test",
		Status:      "open",
		TriggeredAt: now.Add(-90 * time.Second),
	}
	if err := mysqlStorage.CreateMonitorAlert(ctx, alert); err != nil {
		return nil, nil, err
	}

	cleanup := func() {
		rawDB, err := sql.Open("mysql", config.GetMainConfig().MySQL.DSN)
		if err != nil {
			return
		}
		defer rawDB.Close()

		statements := []struct {
			query string
			args  []any
		}{
			{"DELETE FROM monitor_alert WHERE run_id = ?", []any{runID}},
			{"DELETE FROM monitor_event WHERE run_id = ?", []any{runID}},
			{"DELETE FROM monitor_run WHERE run_id = ?", []any{runID}},
			{"DELETE FROM workflow_edge WHERE workflow_id = ?", []any{workflowID}},
			{"DELETE FROM workflow_node WHERE workflow_id = ?", []any{workflowID}},
			{"DELETE FROM user_workflow WHERE workflow_id = ?", []any{workflowID}},
			{"DELETE FROM user_refresh_token WHERE user_id = ?", []any{user.UserID}},
			{"DELETE FROM users WHERE user_id = ?", []any{user.UserID}},
		}
		for _, stmt := range statements {
			_, _ = rawDB.ExecContext(context.Background(), stmt.query, stmt.args...)
		}
	}

	return &envData{
		Username:   username,
		Password:   password,
		UserID:     user.UserID,
		Token:      tokens.AccessToken,
		WorkflowID: workflowID,
		RunID:      runID,
	}, cleanup, nil
}

func applySchemaFiles(ctx context.Context, db *sql.DB, paths []string) error {
	for _, path := range paths {
		buf, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read schema %s: %w", path, err)
		}
		chunks := strings.Split(string(buf), ";")
		for _, chunk := range chunks {
			stmt := strings.TrimSpace(chunk)
			if stmt == "" {
				continue
			}
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("exec schema %s: %w", path, err)
			}
		}
	}
	return nil
}

func printSummaryTable(results []endpointResult) {
	fmt.Printf("%-24s %-8s %-8s %-8s %-10s %-10s %-10s %-10s %-10s\n",
		"endpoint", "req", "ok", "fail", "qps", "avg_ms", "p95_ms", "p99_ms", "max_ms")
	for _, r := range results {
		fmt.Printf("%-24s %-8d %-8d %-8d %-10.2f %-10.2f %-10.2f %-10.2f %-10.2f\n",
			r.Name, r.TotalRequests, r.Successes, r.Failures, r.RequestsPerSec, r.AvgLatencyMs, r.P95LatencyMs, r.P99LatencyMs, r.MaxLatencyMs)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
