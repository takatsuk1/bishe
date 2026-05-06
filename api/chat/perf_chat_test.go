package chat

import (
	"ai/api/chat/api"
	"ai/config"
	authsvc "ai/pkg/auth"
	"ai/pkg/memory"
	"ai/pkg/protocol"
	"ai/pkg/storage"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type inMemoryFactory struct {
	mu      sync.Mutex
	memories map[string]*inMemoryStore
}

type inMemoryStore struct {
	userID         string
	mu             sync.Mutex
	state          map[string]string
	conversations  map[string]*inMemoryConversation
	currentID      string
	conversationSeq int64
}

type inMemoryConversation struct {
	mem      *inMemoryStore
	id       string
	messages []*memory.Message
}

type perfResult struct {
	Concurrency    int     `json:"concurrency"`
	DurationSec    int     `json:"durationSec"`
	TotalRequests  int64   `json:"totalRequests"`
	Successes      int64   `json:"successes"`
	Failures       int64   `json:"failures"`
	RequestsPerSec float64 `json:"requestsPerSec"`
	AvgLatencyMs   float64 `json:"avgLatencyMs"`
	P50LatencyMs   float64 `json:"p50LatencyMs"`
	P95LatencyMs   float64 `json:"p95LatencyMs"`
	P99LatencyMs   float64 `json:"p99LatencyMs"`
	MaxLatencyMs   float64 `json:"maxLatencyMs"`
}

func (f *inMemoryFactory) Get(ctx context.Context, userID string) (memory.Memory, error) {
	_ = ctx
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.memories == nil {
		f.memories = make(map[string]*inMemoryStore)
	}
	if mem, ok := f.memories[userID]; ok {
		return mem, nil
	}
	mem := &inMemoryStore{
		userID:        userID,
		state:         make(map[string]string),
		conversations: make(map[string]*inMemoryConversation),
	}
	f.memories[userID] = mem
	return mem, nil
}

func (m *inMemoryStore) GetUserID(ctx context.Context) string {
	_ = ctx
	return m.userID
}

func (m *inMemoryStore) GetState(ctx context.Context) (map[string]string, error) {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]string, len(m.state))
	for k, v := range m.state {
		out[k] = v
	}
	return out, nil
}

func (m *inMemoryStore) SetState(ctx context.Context, fields ...string) error {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := 0; i+1 < len(fields); i += 2 {
		m.state[fields[i]] = fields[i+1]
	}
	return nil
}

func (m *inMemoryStore) GetConversation(ctx context.Context, id string) (memory.Conversation, error) {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	conv, ok := m.conversations[id]
	if !ok {
		return nil, fmt.Errorf("conversation not found: %s", id)
	}
	return conv, nil
}

func (m *inMemoryStore) GetCurrentConversation(ctx context.Context) (memory.Conversation, error) {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.currentID == "" {
		return m.newConversationLocked(), nil
	}
	conv, ok := m.conversations[m.currentID]
	if !ok {
		return m.newConversationLocked(), nil
	}
	return conv, nil
}

func (m *inMemoryStore) NewConversation(ctx context.Context) (memory.Conversation, error) {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.newConversationLocked(), nil
}

func (m *inMemoryStore) newConversationLocked() *inMemoryConversation {
	m.conversationSeq++
	id := fmt.Sprintf("conv-%d", m.conversationSeq)
	conv := &inMemoryConversation{mem: m, id: id, messages: make([]*memory.Message, 0, 8)}
	m.conversations[id] = conv
	m.currentID = id
	return conv
}

func (m *inMemoryStore) ListConversations(ctx context.Context) ([]string, error) {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.conversations))
	for id := range m.conversations {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

func (m *inMemoryStore) DeleteConversation(ctx context.Context, id string) error {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.conversations, id)
	if m.currentID == id {
		m.currentID = ""
	}
	return nil
}

func (c *inMemoryConversation) GetMemory(ctx context.Context) memory.Memory {
	_ = ctx
	return c.mem
}

func (c *inMemoryConversation) GetID(ctx context.Context) string {
	_ = ctx
	return c.id
}

func (c *inMemoryConversation) Append(ctx context.Context, msgID string, msg *memory.Message) error {
	_ = ctx
	_ = msgID
	c.mem.mu.Lock()
	defer c.mem.mu.Unlock()
	if msg != nil {
		cp := *msg
		c.messages = append(c.messages, &cp)
	}
	c.mem.currentID = c.id
	return nil
}

func (c *inMemoryConversation) GetMessages(ctx context.Context) ([]*memory.Message, error) {
	_ = ctx
	c.mem.mu.Lock()
	defer c.mem.mu.Unlock()
	out := make([]*memory.Message, 0, len(c.messages))
	for _, msg := range c.messages {
		cp := *msg
		out = append(out, &cp)
	}
	return out, nil
}

func (c *inMemoryConversation) GetMessage(ctx context.Context, msgID string) (*memory.Message, error) {
	_ = ctx
	_ = msgID
	c.mem.mu.Lock()
	defer c.mem.mu.Unlock()
	if len(c.messages) == 0 {
		return nil, fmt.Errorf("message not found")
	}
	cp := *c.messages[len(c.messages)-1]
	return &cp, nil
}

func TestChatPerformanceProfile(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	origLogWriter := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(origLogWriter)

	config.CmdlineFlags.ConfigProvider = "file"
	config.CmdlineFlags.MainConfigFilename = filepath.Join("..", "..", "config.yaml")
	config.Init()

	cfg := config.GetMainConfig()
	ctx := context.Background()

	rawDB, err := sql.Open("mysql", cfg.MySQL.DSN)
	if err != nil {
		t.Fatalf("open mysql failed: %v", err)
	}
	defer rawDB.Close()
	if err := rawDB.PingContext(ctx); err != nil {
		t.Fatalf("ping mysql failed: %v", err)
	}

	projectRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	projectRoot = filepath.Clean(filepath.Join(projectRoot, "..", ".."))

	if err := applyChatPerfSchemas(ctx, rawDB, []string{
		filepath.Join(projectRoot, "scripts", "schema.sql"),
		filepath.Join(projectRoot, "scripts", "monitor_schema.sql"),
	}); err != nil {
		t.Fatalf("apply schemas failed: %v", err)
	}

	mysqlStorage, err := storage.InitMySQL(cfg.MySQL.DSN)
	if err != nil {
		t.Fatalf("init mysql failed: %v", err)
	}

	authSvc, err := authsvc.NewService(
		mysqlStorage,
		cfg.Auth.JWTSecret,
		time.Duration(cfg.Auth.AccessTokenTTLMinutes)*time.Minute,
		time.Duration(cfg.Auth.RefreshTokenTTLHours)*time.Hour,
	)
	if err != nil {
		t.Fatalf("init auth service failed: %v", err)
	}

	username := fmt.Sprintf("chat_perf_%d", time.Now().UnixNano())
	password := "Perf123456"
	user, tokens, err := authSvc.Register(ctx, username, password, "Chat Perf User")
	if err != nil {
		t.Fatalf("register user failed: %v", err)
	}
	defer cleanupChatPerfUser(ctx, rawDB, user.UserID)
	defer cleanupChatPerfRedis(ctx, user.UserID)

	mockAgent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/.well-known/agent.json":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(protocol.AgentCard{Name: "mockchat"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tasks/send":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(protocol.SendMessageResponse{TaskID: fmt.Sprintf("task-%d", time.Now().UnixNano())})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/tasks/") && strings.HasSuffix(r.URL.Path, "/events"):
			taskID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/tasks/"), "/events")
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			writeEvent := func(ev protocol.StreamEvent) {
				b, _ := json.Marshal(ev)
				_, _ = fmt.Fprintf(w, "data: %s\n\n", string(b))
				if flusher != nil {
					flusher.Flush()
				}
			}
			writeEvent(protocol.NewTaskStatusEvent(taskID, protocol.TaskStatus{
				State: protocol.TaskStateWorking,
				Message: &protocol.Message{
					Role:  protocol.MessageRoleAgent,
					Parts: []protocol.Part{protocol.NewTextPart("mock-reply-")},
				},
				UpdatedAt: time.Now().UnixMilli(),
			}))
			writeEvent(protocol.NewTaskStatusEvent(taskID, protocol.TaskStatus{
				State: protocol.TaskStateCompleted,
				Message: &protocol.Message{
					Role:  protocol.MessageRoleAgent,
					Parts: []protocol.Part{protocol.NewTextPart("done")},
				},
				UpdatedAt: time.Now().UnixMilli(),
			}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockAgent.Close()

	origAgents := append([]config.AgentConfig(nil), cfg.OpenAIConnector.Agents...)
	cfg.OpenAIConnector.Agents = []config.AgentConfig{{Name: "mockchat", ServerURL: mockAgent.URL}}
	defer func() {
		cfg.OpenAIConnector.Agents = origAgents
	}()

	origAuthService := authService
	origAuthServiceErr := authServiceErr
	origAuthOnce := authServiceOnce
	defer func() {
		authService = origAuthService
		authServiceErr = origAuthServiceErr
		authServiceOnce = origAuthOnce
	}()

	memoryFactoryOnce = sync.Once{}
	memoryFactory = nil
	memoryFactoryErr = nil
	if _, err := getMemoryFactory(); err != nil {
		t.Fatalf("init redis memory factory failed: %v", err)
	}

	authServiceOnce = sync.Once{}
	authServiceOnce.Do(func() {})
	authService = authSvc
	authServiceErr = nil

	engine := gin.New()
	srv := &Server{}
	engine.POST("/v1/chat/completions", ChatMiddleware(), srv.chatHandler)
	httpSrv := httptest.NewServer(engine)
	defer httpSrv.Close()

	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        256,
			MaxIdleConnsPerHost: 256,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	concurrencyLevels := []int{1, 8, 16, 32, 48, 64, 96, 128, 160, 192, 256}
	duration := 6 * time.Second
	results := make([]perfResult, 0, len(concurrencyLevels))
	for _, cc := range concurrencyLevels {
		t.Logf("running chat perf with concurrency=%d duration=%s", cc, duration)
		res := runChatPerf(t, client, httpSrv.URL, tokens.AccessToken, cc, duration)
		results = append(results, res)
	}

	t.Log("CHAT_PERF_RESULTS_JSON_START")
	buf, _ := json.MarshalIndent(results, "", "  ")
	t.Log(string(buf))
	t.Log("CHAT_PERF_RESULTS_JSON_END")
	for _, r := range results {
		t.Logf("chat concurrency=%d req=%d ok=%d fail=%d qps=%.2f avg=%.2fms p95=%.2fms p99=%.2fms max=%.2fms",
			r.Concurrency, r.TotalRequests, r.Successes, r.Failures, r.RequestsPerSec, r.AvgLatencyMs, r.P95LatencyMs, r.P99LatencyMs, r.MaxLatencyMs)
	}
}

func runChatPerf(t *testing.T, client *http.Client, baseURL, token string, concurrency int, duration time.Duration) perfResult {
	t.Helper()

	stopAt := time.Now().Add(duration)
	latencies := make([]time.Duration, 0, 1024)
	var latMu sync.Mutex
	var totalReq int64
	var okReq int64
	var failReq int64
	var totalLatencyNs int64

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			reqSeq := 0
			for time.Now().Before(stopAt) {
				reqSeq++
				payload, _ := json.Marshal(api.ChatRequest{
					Model:          "mockchat",
					ConversationID: fmt.Sprintf("perf-%d-%d", worker, reqSeq),
					Messages: []api.Message{
						{Role: "user", Content: "hello"},
					},
					Stream: boolPtr(true),
				})

				req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/chat/completions", bytes.NewReader(payload))
				if err != nil {
					atomic.AddInt64(&failReq, 1)
					continue
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+token)

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

				_, _ = io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					atomic.AddInt64(&failReq, 1)
					continue
				}
				atomic.AddInt64(&okReq, 1)
			}
		}(i)
	}
	wg.Wait()

	total := atomic.LoadInt64(&totalReq)
	if total == 0 {
		t.Fatalf("no chat requests recorded")
	}
	okCount := atomic.LoadInt64(&okReq)
	failCount := atomic.LoadInt64(&failReq)

	latMu.Lock()
	sorted := append([]time.Duration(nil), latencies...)
	latMu.Unlock()
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	return perfResult{
		Concurrency:    concurrency,
		DurationSec:    int(duration.Seconds()),
		TotalRequests:  total,
		Successes:      okCount,
		Failures:       failCount,
		RequestsPerSec: float64(total) / duration.Seconds(),
		AvgLatencyMs:   float64(atomic.LoadInt64(&totalLatencyNs)) / float64(total) / float64(time.Millisecond),
		P50LatencyMs:   durationPercentileMs(sorted, 0.50),
		P95LatencyMs:   durationPercentileMs(sorted, 0.95),
		P99LatencyMs:   durationPercentileMs(sorted, 0.99),
		MaxLatencyMs:   float64(sorted[len(sorted)-1]) / float64(time.Millisecond),
	}
}

func durationPercentileMs(values []time.Duration, ratio float64) float64 {
	if len(values) == 0 {
		return 0
	}
	idx := int(float64(len(values)-1) * ratio)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(values) {
		idx = len(values) - 1
	}
	return float64(values[idx]) / float64(time.Millisecond)
}

func boolPtr(v bool) *bool {
	return &v
}

func applyChatPerfSchemas(ctx context.Context, db *sql.DB, paths []string) error {
	for _, path := range paths {
		buf, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, chunk := range strings.Split(string(buf), ";") {
			stmt := strings.TrimSpace(chunk)
			if stmt == "" {
				continue
			}
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
	}
	return nil
}

func cleanupChatPerfUser(ctx context.Context, db *sql.DB, userID string) {
	_, _ = db.ExecContext(ctx, "DELETE FROM user_refresh_token WHERE user_id = ?", userID)
	_, _ = db.ExecContext(ctx, "DELETE FROM users WHERE user_id = ?", userID)
}

func cleanupChatPerfRedis(ctx context.Context, userID string) {
	if strings.TrimSpace(userID) == "" {
		return
	}
	if memoryFactory != nil {
		if mem, err := memoryFactory.Get(ctx, userID); err == nil {
			if convIDs, listErr := mem.ListConversations(ctx); listErr == nil {
				for _, convID := range convIDs {
					_ = mem.DeleteConversation(ctx, convID)
				}
			}
		}
	}
	if cli, err := storage.GetRedisClient(); err == nil {
		_, _ = cli.Del(ctx, fmt.Sprintf("{user:%s}:state", userID), fmt.Sprintf("{user:%s}:conversations", userID)).Result()
	}
}
