package chat

import (
	"ai/api/chat/api"
	"ai/api/orchestrator"
	"ai/config"
	authsvc "ai/pkg/auth"
	"ai/pkg/authz"
	internalproto "ai/pkg/protocol"
	"ai/pkg/storage"
	"ai/pkg/transport/httpagent"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"ai/pkg/logger"
	"ai/pkg/memory"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var (
	memoryFactory     memory.Factory
	memoryFactoryOnce sync.Once
	memoryFactoryErr  error
	authService       *authsvc.Service
	authServiceOnce   sync.Once
	authServiceErr    error
)

func getMemoryFactory() (memory.Factory, error) {
	memoryFactoryOnce.Do(func() {
		cfg := config.GetMainConfig()
		redisURL := cfg.Redis.URL
		maxWindowSize := cfg.Redis.MaxWindowSize
		if redisURL == "" {
			redisURL = "redis://127.0.0.1:6379"
		}
		if maxWindowSize <= 0 {
			maxWindowSize = memory.DefaultMaxWindowSize
		}
		memoryFactory, memoryFactoryErr = memory.NewRedisMemoryFactory(redisURL, maxWindowSize)
		if memoryFactoryErr != nil {
			logger.Errorf("Failed to create memory factory: %v", memoryFactoryErr)
		}
	})
	return memoryFactory, memoryFactoryErr
}

func getAuthService() (*authsvc.Service, error) {
	authServiceOnce.Do(func() {
		st, err := storage.GetMySQLStorage()
		if err != nil {
			authServiceErr = err
			return
		}
		cfg := config.GetMainConfig()
		authService, authServiceErr = authsvc.NewService(
			st,
			cfg.Auth.JWTSecret,
			time.Duration(cfg.Auth.AccessTokenTTLMinutes)*time.Minute,
			time.Duration(cfg.Auth.RefreshTokenTTLHours)*time.Hour,
		)
	})
	return authService, authServiceErr
}

func handleTaskStatusUpdateEvent(ctx context.Context, req api.ChatRequest, ch chan<- any,
	event internalproto.TaskStatusUpdateEvent) {
	var content string
	if event.Status.Message != nil {
		if len(event.Status.Message.Parts) > 0 {
			part := event.Status.Message.Parts[0]
			if part.Type == internalproto.PartTypeText {
				content = part.Text
			}
		}
	}
	finalState := isFinalState(event.Status.State)
	if finalState {
		content = content + "[](task://done)"
	}
	res := api.ChatResponse{
		Model:     req.Model,
		CreatedAt: time.Now().UTC(),
		Message:   api.Message{Role: "assistant", Content: content},
	}
	ch <- res
}

type Server struct {
}

func NewOpenAIServer() (http.Handler, error) {
	_, err := getMemoryFactory()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w. Please ensure Redis is running", err)
	}
	logger.Infof("Redis memory factory initialized successfully")

	cfg := config.GetMainConfig()
	dsn := cfg.MySQL.DSN
	if dsn == "" {
		return nil, fmt.Errorf("mysql dsn is not configured")
	}
	_, err = storage.InitMySQL(dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to MySQL: %w. Please ensure MySQL is running and the database exists", err)
	}
	logger.Infof("MySQL storage initialized successfully")

	logger.Infof("Initializing agent workflows...")
	if err := orchestrator.InitAgentWorkflows(); err != nil {
		logger.Errorf("Failed to initialize agent workflows: %v", err)
	} else {
		logger.Infof("Agent workflows initialized successfully")
	}

	s := &Server{}

	// Keep custom [TRACE] logs only; suppress Gin's default access log lines.
	gin.DefaultWriter = io.Discard

	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		// Minimal CORS for the web UI (Vite dev server runs on a different origin).
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
		c.Header("Access-Control-Expose-Headers", "Content-Type")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	})
	engine.ContextWithFallback = true
	engine.POST("/v1/chat/completions", ChatMiddleware(), s.chatHandler)
	engine.Any("/v1/orchestrator/*path", s.proxyOrchestrator)
	engine.Any("/v1/monitor/*path", s.proxyMonitor)
	engine.Any("/v1/auth/*path", s.proxyAuth)
	engine.Any("/v1/admin/*path", s.proxyAdmin)
	return engine.Handler(), nil
}

func (s *Server) proxyOrchestrator(c *gin.Context) {
	_ = s
	s.proxyHostRequest(c, "/v1/orchestrator")
}

func (s *Server) proxyMonitor(c *gin.Context) {
	_ = s
	s.proxyHostRequest(c, "/v1/monitor")
}

func (s *Server) proxyAuth(c *gin.Context) {
	_ = s
	s.proxyHostRequest(c, "/v1/auth")
}

func (s *Server) proxyAdmin(c *gin.Context) {
	_ = s
	s.proxyHostRequest(c, "/v1/admin")
}

func (s *Server) proxyHostRequest(c *gin.Context, routePrefix string) {
	// Resolve host server URL from config so the browser only talks to openai_connector.
	var hostURL string
	if cfg := config.GetMainConfig(); cfg != nil {
		for _, agent := range cfg.OpenAIConnector.Agents {
			if agent.Name == "host" {
				hostURL = agent.ServerURL
				break
			}
		}
	}
	if strings.TrimSpace(hostURL) == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "host agent is not configured in openai_connector.agents"})
		return
	}
	base, err := url.Parse(hostURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid host agent server_url"})
		return
	}

	path := routePrefix + c.Param("path")
	if path == routePrefix {
		path = routePrefix + "/"
	}
	target := *base
	target.Path = strings.TrimRight(target.Path, "/") + path
	target.RawQuery = c.Request.URL.RawQuery

	body, _ := io.ReadAll(c.Request.Body)
	req, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if ct := c.GetHeader("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if auth := c.GetHeader("Authorization"); auth != "" {
		req.Header.Set("Authorization", auth)
	}

	// User workflow test runs may contain multiple LLM/tool nodes and can exceed 2 minutes.
	// Keep proxy timeout long enough to avoid canceling in-flight orchestrator execution.
	httpClient := &http.Client{Timeout: 10 * time.Minute}
	resp, err := httpClient.Do(req)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	c.Data(resp.StatusCode, contentType, respBody)
}

// isFinalState checks if a TaskState represents a terminal state.
func isFinalState(state internalproto.TaskState) bool {
	return state == internalproto.TaskStateCompleted ||
		state == internalproto.TaskStateFailed ||
		state == internalproto.TaskStateCanceled
}

func (s *Server) chatHandler(c *gin.Context) {
	requestID := c.GetHeader("X-Request-ID")
	if requestID == "" {
		requestID = uuid.New().String()
	}
	c.Header("X-Request-ID", requestID)

	var req api.ChatRequest
	err := c.ShouldBindJSON(&req)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(req.Messages) == 0 {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "messages is required"})
		return
	}

	authService, authErr := getAuthService()
	if authErr != nil {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "auth service unavailable"})
		return
	}
	token, tokenErr := bearerTokenFromHeader(c.GetHeader("Authorization"))
	if tokenErr != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	authUser, userErr := authService.AuthenticateAccessToken(c.Request.Context(), token)
	if userErr != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	authUserID := authUser.UserID

	agentConfig, err := resolveAgentConfig(req.Model, authUserID, requestID)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ch := make(chan any)
	go func() {
		defer close(ch)

		ctx := c.Request.Context()

		factory, memErr := getMemoryFactory()
		if memErr != nil {
			ch <- gin.H{"error": fmt.Sprintf("memory service unavailable: %v", memErr)}
			return
		}

		userID := authUserID

		mem, err := factory.Get(ctx, userID)
		if err != nil {
			ch <- gin.H{"error": fmt.Sprintf("failed to get memory: %v", err)}
			return
		}
		state, _ := mem.GetState(ctx)
		clientConvID := strings.TrimSpace(req.ConversationID)

		var conv memory.Conversation
		if clientConvID != "" {
			mapKey := "sk:conv_map:" + clientConvID
			if mappedID := strings.TrimSpace(state[mapKey]); mappedID != "" {
				if existing, getErr := mem.GetConversation(ctx, mappedID); getErr == nil {
					conv = existing
				}
			}
			if conv == nil {
				conv, err = mem.NewConversation(ctx)
				if err != nil {
					ch <- gin.H{"error": fmt.Sprintf("failed to create conversation: %v", err)}
					return
				}
				if setErr := mem.SetState(ctx, mapKey, conv.GetID(ctx)); setErr != nil {
				}
			}
		} else {
			// Backward-compatible fallback: if client doesn't send conversation_id
			// and this request only carries the latest user message, treat it as a new chat.
			if len(req.Messages) <= 1 {
				conv, err = mem.NewConversation(ctx)
				if err != nil {
					ch <- gin.H{"error": fmt.Sprintf("failed to create conversation: %v", err)}
					return
				}
			} else {
				conv, err = mem.GetCurrentConversation(ctx)
				if err != nil {
					conv, err = mem.NewConversation(ctx)
					if err != nil {
						ch <- gin.H{"error": fmt.Sprintf("failed to create conversation: %v", err)}
						return
					}
				}
			}
		}
		convID := conv.GetID(ctx)

		lastUserMsg := req.Messages[len(req.Messages)-1]
		userMsgID := uuid.New().String()
		userMsg := &memory.Message{
			Role:    lastUserMsg.Role,
			Content: lastUserMsg.Content,
		}
		if err := conv.Append(ctx, userMsgID, userMsg); err != nil {
		}

		historyMsgs, err := conv.GetMessages(ctx)
		if err != nil {
		}

		var historyParts []internalproto.Part
		if len(historyMsgs) > 0 {
			historyText := "=== 对话历史 ===\n"
			for _, msg := range historyMsgs {
				if msg.Role == "user" {
					historyText += fmt.Sprintf("用户: %s\n", msg.Content)
				} else if msg.Role == "assistant" {
					historyText += fmt.Sprintf("助手: %s\n", msg.Content)
				}
			}
			historyText += "=== 当前问题 ===\n"
			historyParts = append(historyParts, internalproto.NewTextPart(historyText))
		}

		taskStateKey := memory.StateKeyCurrentTaskID + ":" + convID
		var taskID string
		if tid, ok := state[taskStateKey]; ok && tid != "" {
			taskID = tid
		}

		if taskID == "" {
			taskID = uuid.New().String()
			res := api.ChatResponse{
				Model:     req.Model,
				CreatedAt: time.Now().UTC(),
				Message:   api.Message{Role: "assistant", Content: fmt.Sprintf("[](task://%s)\n", taskID)},
				Done:      false,
			}
			ch <- res
		}

		if err := mem.SetState(ctx, taskStateKey, taskID, memory.StateKeyCurrentUserEventID, ""); err != nil {
		}

		var allParts []internalproto.Part
		allParts = append(allParts, historyParts...)
		allParts = append(allParts, internalproto.NewTextPart(lastUserMsg.Content))

		initMessage := internalproto.Message{
			Role:  internalproto.MessageRoleUser,
			Parts: allParts,
			Metadata: map[string]interface{}{
				"user_id":         userID,
				"userId":          userID,
				"UserID":          userID,
				"conversation_id": convID,
				"ConversationID":  convID,
				"history_count":   len(historyMsgs),
				"HistoryCount":    len(historyMsgs),
			},
			TaskID: &taskID,
		}

		client := httpagent.NewClient(agentConfig.ServerURL, time.Minute*10)
		ctx = httpagent.WithRequestID(ctx, requestID)

		taskID, err = client.SendMessage(ctx, initMessage)
		if err != nil {
			ch <- gin.H{"error": err.Error()}
			return
		}
		taskChan, errChan := client.StreamTaskEvents(ctx, taskID)

		var assistantContent strings.Builder
		lastState := internalproto.TaskState("")
		for v := range taskChan {
			if v.TaskStatusUpdate != nil {
				st := v.TaskStatusUpdate.Status.State
				if st != lastState {
					lastState = st
				}
				handleTaskStatusUpdateEvent(ctx, req, ch, *v.TaskStatusUpdate)
				if v.TaskStatusUpdate.Status.Message != nil {
					for _, part := range v.TaskStatusUpdate.Status.Message.Parts {
						if part.Type == internalproto.PartTypeText {
							assistantContent.WriteString(part.Text)
						}
					}
				}
			}
		}
		if streamErr := <-errChan; streamErr != nil {
			ch <- gin.H{"error": streamErr.Error()}
			return
		}

		assistantMsgID := uuid.New().String()
		assistantMsg := &memory.Message{
			Role:    "assistant",
			Content: assistantContent.String(),
		}
		if err := conv.Append(ctx, assistantMsgID, assistantMsg); err != nil {
		}

		if isFinalState(lastState) {
			if err := mem.SetState(ctx, taskStateKey, ""); err != nil {
			}
		}

		res := api.ChatResponse{
			Model:      req.Model,
			CreatedAt:  time.Now().UTC(),
			Message:    api.Message{Role: "assistant", Content: ""},
			Done:       true,
			DoneReason: "stop",
		}
		ch <- res
	}()

	streamResponse(c, ch)
}

func resolveAgentConfig(model string, userID string, requestID string) (config.AgentConfig, error) {
	if st, err := storage.GetMySQLStorage(); err == nil {
		allowAllRead := false
		authzService := authz.NewService(st)
		if allowed, checkErr := authzService.CanAccess(context.Background(), authz.CheckRequest{
			UserID:        userID,
			Resource:      "orchestrator.agent.read",
			RequiredScope: authz.ScopeAll,
		}); checkErr == nil && allowed {
			allowAllRead = true
		}

		visible := make([]storage.UserAgentDefinition, 0)
		if allowAllRead {
			if allAgents, listErr := st.ListUserAgents(context.Background(), ""); listErr == nil {
				visible = allAgents
			} else {
			}
		} else {
			merged := make(map[string]storage.UserAgentDefinition)
			if ownAgents, ownErr := st.ListUserAgents(context.Background(), userID); ownErr == nil {
				for _, a := range ownAgents {
					merged[a.AgentID] = a
				}
			}
			if systemAgents, sysErr := st.ListUserAgents(context.Background(), "system"); sysErr == nil {
				for _, a := range systemAgents {
					merged[a.AgentID] = a
				}
			}
			visible = make([]storage.UserAgentDefinition, 0, len(merged))
			for _, a := range merged {
				visible = append(visible, a)
			}
		}

		for _, a := range visible {
			if a.Name != model && a.AgentID != model {
				continue
			}
			if a.Status != storage.AgentStatusPublished {
				return config.AgentConfig{}, fmt.Errorf("agent %s is not published", model)
			}
			if a.Port <= 0 {
				return config.AgentConfig{}, fmt.Errorf("agent %s is not running", model)
			}

			serverURL := fmt.Sprintf("http://127.0.0.1:%d", a.Port)
			if err := validateAgentEndpoint(context.Background(), serverURL); err != nil {
				_ = st.UpdateAgentStatus(context.Background(), a.AgentID, storage.AgentStatusStopped, 0, 0)
				return config.AgentConfig{}, fmt.Errorf("agent %s is not running (stale publish state), please republish or restart", model)
			}

			return config.AgentConfig{
				Name:      a.Name,
				ServerURL: serverURL,
			}, nil
		}
	}

	for _, agent := range config.GetMainConfig().OpenAIConnector.Agents {
		if agent.Name == model {
			return agent, nil
		}
	}

	return config.AgentConfig{}, fmt.Errorf("agent %s not found", model)
}

func bearerTokenFromHeader(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", fmt.Errorf("missing authorization header")
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(v, prefix) {
		return "", fmt.Errorf("invalid authorization header")
	}
	token := strings.TrimSpace(strings.TrimPrefix(v, prefix))
	if token == "" {
		return "", fmt.Errorf("missing bearer token")
	}
	return token, nil
}

func validateAgentEndpoint(ctx context.Context, serverURL string) error {
	ctx, cancel := context.WithTimeout(ctx, 1200*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(serverURL, "/")+"/.well-known/agent.json", nil)
	if err != nil {
		return err
	}

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check returned status %d", resp.StatusCode)
	}

	return nil
}

func streamResponse(c *gin.Context, ch chan any) {
	c.Header("Content-Type", "application/x-ndjson")
	c.Stream(func(w io.Writer) bool {
		val, ok := <-ch
		if !ok {
			return false
		}

		bts, err := json.Marshal(val)
		if err != nil {
			logger.InfoContextf(c, "streamResponse: json.Marshal failed with %v", err)
			return false
		}

		//log.InfoContextf(c, "write: %s", bts)

		// Delineate chunks with new-line delimiter
		bts = append(bts, '\n')
		if _, err := w.Write(bts); err != nil {
			logger.InfoContextf(c, "streamResponse: w.Write failed with %v", err)
			return false
		}

		return true
	})
}
