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
	"compress/zlib"
	"context"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"ai/pkg/logger"
	"ai/pkg/memory"
	"github.com/ledongthuc/pdf"
	"github.com/nguyenthenguyen/docx"
	"github.com/xuri/excelize/v2"

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

const (
	maxUploadSizeBytes     = 20 * 1024 * 1024
	maxExtractedTextRunes  = 12000
	uploadStorageDirectory = "uploads"
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
	engine.POST("/v1/files/upload", s.uploadFilesHandler)
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

type uploadedFileResult struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Size          int64  `json:"size"`
	Type          string `json:"type"`
	ExtractedText string `json:"extracted_text,omitempty"`
	Warning       string `json:"warning,omitempty"`
}

func (s *Server) uploadFilesHandler(c *gin.Context) {
	_, authUserID, ok := authenticateRequestUser(c)
	if !ok {
		return
	}

	if err := c.Request.ParseMultipartForm(maxUploadSizeBytes * 5); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid multipart form"})
		return
	}

	fileHeaders := c.Request.MultipartForm.File["files"]
	if len(fileHeaders) == 0 {
		if single := c.Request.MultipartForm.File["file"]; len(single) > 0 {
			fileHeaders = single
		}
	}
	if len(fileHeaders) == 0 {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "no files uploaded"})
		return
	}

	uploadDir := filepath.Join(uploadStorageDirectory, authUserID)
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "failed to create upload directory"})
		return
	}

	results := make([]uploadedFileResult, 0, len(fileHeaders))
	supportedCount := 0
	for _, header := range fileHeaders {
		fileExt := strings.ToLower(filepath.Ext(header.Filename))
		if !isSupportedUploadExtension(fileExt) {
			results = append(results, uploadedFileResult{
				ID:      uuid.NewString(),
				Name:    header.Filename,
				Size:    header.Size,
				Type:    header.Header.Get("Content-Type"),
				Warning: "unsupported file type: only PDF/DOCX/XLSX are allowed",
			})
			continue
		}

		if header.Size > maxUploadSizeBytes {
			results = append(results, uploadedFileResult{
				ID:      uuid.NewString(),
				Name:    header.Filename,
				Size:    header.Size,
				Type:    header.Header.Get("Content-Type"),
				Warning: "file exceeds 20MB limit and was skipped",
			})
			continue
		}

		f, err := header.Open()
		if err != nil {
			results = append(results, uploadedFileResult{ID: uuid.NewString(), Name: header.Filename, Size: header.Size, Type: header.Header.Get("Content-Type"), Warning: "failed to open upload"})
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(f, maxUploadSizeBytes+1))
		_ = f.Close()
		if readErr != nil {
			results = append(results, uploadedFileResult{ID: uuid.NewString(), Name: header.Filename, Size: header.Size, Type: header.Header.Get("Content-Type"), Warning: "failed to read upload"})
			continue
		}
		if int64(len(data)) > maxUploadSizeBytes {
			results = append(results, uploadedFileResult{ID: uuid.NewString(), Name: header.Filename, Size: int64(len(data)), Type: header.Header.Get("Content-Type"), Warning: "file exceeds 20MB limit and was skipped"})
			continue
		}

		fileID := uuid.NewString()
		savedPath := filepath.Join(uploadDir, fileID+fileExt)
		if writeErr := os.WriteFile(savedPath, data, 0o644); writeErr != nil {
			results = append(results, uploadedFileResult{ID: fileID, Name: header.Filename, Size: int64(len(data)), Type: header.Header.Get("Content-Type"), Warning: "failed to save upload"})
			continue
		}

		supportedCount++
		extracted, warning := extractTextFromUpload(header.Filename, header.Header.Get("Content-Type"), data)
		logger.Infof("[TRACE] upload.extract file=%s ext=%s size=%d extracted_len=%d warning=%q", header.Filename, fileExt, len(data), len(extracted), warning)
		results = append(results, uploadedFileResult{
			ID:            fileID,
			Name:          header.Filename,
			Size:          int64(len(data)),
			Type:          header.Header.Get("Content-Type"),
			ExtractedText: extracted,
			Warning:       warning,
		})
	}
	if supportedCount == 0 {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "only PDF/DOCX/XLSX files are supported", "files": results})
		return
	}

	c.JSON(http.StatusOK, gin.H{"files": results})
}

func authenticateRequestUser(c *gin.Context) (*authsvc.Service, string, bool) {
	authService, authErr := getAuthService()
	if authErr != nil {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "auth service unavailable"})
		return nil, "", false
	}
	token, tokenErr := bearerTokenFromHeader(c.GetHeader("Authorization"))
	if tokenErr != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return nil, "", false
	}
	authUser, userErr := authService.AuthenticateAccessToken(c.Request.Context(), token)
	if userErr != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return nil, "", false
	}
	return authService, authUser.UserID, true
}

func extractTextFromUpload(filename string, contentType string, data []byte) (string, string) {
	ext := normalizeUploadExt(filename, contentType)
	var text string
	var warning string

	switch ext {
	case ".pdf":
		parsed, err := extractTextFromPDF(data)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "ocr required") {
				warning = "pdf has no text layer; OCR required"
			} else {
				warning = "pdf parse failed"
			}
		} else {
			text = parsed
		}
	case ".docx":
		parsed, err := extractTextFromDOCX(data)
		if err == nil {
			text = parsed
		} else {
			warning = "docx parse failed"
		}
	case ".xlsx":
		parsed, err := extractTextFromXLSX(data)
		if err == nil {
			text = parsed
		} else {
			warning = "xlsx parse failed"
		}
	case ".doc":
		warning = "legacy .doc is not supported; please convert to .docx"
	case ".xls":
		warning = "legacy .xls is not supported; please convert to .xlsx"
	default:
		warning = "unsupported file type: only PDF/DOCX/XLSX are allowed"
	}

	text = normalizeExtractedText(text)
	if utf8.RuneCountInString(text) > maxExtractedTextRunes {
		runes := []rune(text)
		text = strings.TrimSpace(string(runes[:maxExtractedTextRunes])) + "\n...(truncated)"
	}
	if text == "" && warning == "" {
		warning = "no readable text extracted"
	}
	if text != "" {
		if isErrType, reason := isExtractionErrorTypeText(ext, text); isErrType {
			logger.Infof("[TRACE] upload.extract.error_type file=%s ext=%s reason=%s", filename, ext, reason)
			warning = appendWarning(warning, "file upload has extraction-error text")
			text = ""
		}
	}
	if text != "" {
		text = sanitizeExtractedText(text)
		if ok, reason := isHighQualityExtractedText(ext, text); !ok {
			if reason == "bad_char_ratio" {
				if repaired := salvageReadableExtractedText(text); repaired != "" {
					if ok2, _ := isHighQualityExtractedText(ext, repaired); ok2 {
						text = repaired
						return text, appendWarning(warning, "text extraction was partially repaired")
					}
				}
			}
			logger.Infof("[TRACE] upload.extract.quality_fail file=%s ext=%s reason=%s", filename, ext, reason)
			if reason == "pdf_object_tokens" {
				warning = appendWarning(warning, "file upload has extraction-error text")
			} else {
				warning = appendWarning(warning, "text extraction quality is low")
			}
			text = ""
		}
	}

	return text, warning
}

func sanitizeExtractedText(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range text {
		if r == '\uFFFD' || r == 0 {
			continue
		}
		if r == '\n' || r == '\r' || r == '\t' || r >= 0x20 {
			b.WriteRune(r)
		}
	}
	return normalizeExtractedText(b.String())
}

func salvageReadableExtractedText(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		s := strings.TrimSpace(line)
		if s == "" {
			continue
		}
		total := 0
		good := 0
		for _, r := range s {
			total++
			if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsSpace(r) || unicode.IsPunct(r) {
				good++
			}
		}
		if total > 0 && float64(good)/float64(total) >= 0.75 {
			kept = append(kept, s)
		}
	}
	return normalizeExtractedText(strings.Join(kept, "\n"))
}

func appendWarning(prev string, next string) string {
	prev = strings.TrimSpace(prev)
	next = strings.TrimSpace(next)
	if prev == "" {
		return next
	}
	if next == "" {
		return prev
	}
	return prev + "; " + next
}

func isExtractionErrorTypeText(ext string, text string) (bool, string) {
	s := strings.ToLower(strings.TrimSpace(text))
	if s == "" {
		return false, ""
	}
	// Typical parser/object garbage; treat as upload extraction failure.
	pdfMarkers := 0
	for _, marker := range []string{"endobj", "stream", "endstream", "xref", "/type", "/catalog"} {
		pdfMarkers += strings.Count(s, marker)
	}
	if ext == ".pdf" && pdfMarkers >= 6 {
		return true, "pdf_object_tokens"
	}
	for _, marker := range []string{
		"traceback (most recent call last)",
		"java.lang.",
		"exception:",
		"stack trace",
		"xml parsing error",
		"fatal error",
	} {
		if strings.Contains(s, marker) {
			return true, "parser_error_text"
		}
	}
	return false, ""
}

func isHighQualityExtractedText(ext string, text string) (bool, string) {
	s := strings.TrimSpace(text)
	if s == "" {
		return false, "empty"
	}
	if utf8.RuneCountInString(s) < 20 {
		return false, "too_short"
	}
	lower := strings.ToLower(s)
	pdfNoise := 0
	for _, marker := range []string{"endobj", "stream", "endstream", "xref", "obj", "/type", "catalog", "contents"} {
		pdfNoise += strings.Count(lower, marker)
	}
	if ext == ".pdf" && pdfNoise >= 6 {
		return false, "pdf_object_tokens"
	}

	total := 0
	bad := 0
	letters := 0
	for _, r := range s {
		total++
		if r == '\uFFFD' || r == 0 {
			bad++
			continue
		}
		if r < 0x08 || (r > 0x0D && r < 0x20) {
			bad++
			continue
		}
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			letters++
		}
	}
	if total == 0 {
		return false, "empty"
	}
	badRatio := float64(bad) / float64(total)
	if badRatio > 0.06 {
		return false, "bad_char_ratio"
	}
	if float64(letters)/float64(total) < 0.12 {
		return false, "low_text_density"
	}
	nonEmptyLines := make([]string, 0, 64)
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(ln)
		if ln != "" {
			nonEmptyLines = append(nonEmptyLines, ln)
		}
	}
	if len(nonEmptyLines) >= 24 {
		sum := 0
		short := 0
		for _, ln := range nonEmptyLines {
			l := utf8.RuneCountInString(ln)
			sum += l
			if l <= 3 {
				short++
			}
		}
		avg := float64(sum) / float64(len(nonEmptyLines))
		if avg < 5.2 && float64(short)/float64(len(nonEmptyLines)) > 0.4 {
			return false, "fragmented_lines"
		}
	}
	return true, "ok"
}

func isSupportedUploadExtension(ext string) bool {
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case ".pdf", ".docx", ".xlsx":
		return true
	default:
		return false
	}
}

func normalizeUploadExt(filename string, contentType string) string {
	ext := strings.ToLower(strings.TrimSpace(filepath.Ext(filename)))
	if ext != "" {
		return ext
	}
	ct := strings.ToLower(strings.TrimSpace(contentType))
	switch {
	case strings.Contains(ct, "pdf"):
		return ".pdf"
	case strings.Contains(ct, "word"):
		return ".docx"
	case strings.Contains(ct, "excel") || strings.Contains(ct, "spreadsheet"):
		return ".xlsx"
	default:
		return ""
	}
}

func extractTextFromDOCX(data []byte) (string, error) {
	tmp, err := os.CreateTemp("", "upload-*.docx")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}

	docReader, err := docx.ReadDocxFile(tmpPath)
	if err != nil {
		return "", err
	}
	defer docReader.Close()
	xmlText := strings.TrimSpace(docReader.Editable().GetContent())
	text := strings.TrimSpace(extractTextFromWordprocessingML(xmlText))
	if text == "" {
		return "", fmt.Errorf("docx text is empty")
	}
	return text, nil
}

func extractTextFromWordprocessingML(xmlText string) string {
	if strings.TrimSpace(xmlText) == "" {
		return ""
	}
	dec := xml.NewDecoder(strings.NewReader(xmlText))
	var b strings.Builder

	writeNewline := func() {
		if b.Len() == 0 {
			return
		}
		s := b.String()
		if strings.HasSuffix(s, "\n") {
			return
		}
		b.WriteByte('\n')
	}

	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch strings.ToLower(t.Name.Local) {
			case "br", "cr":
				writeNewline()
			case "tab":
				b.WriteByte('\t')
			}
		case xml.EndElement:
			switch strings.ToLower(t.Name.Local) {
			case "p", "tr":
				writeNewline()
			}
		case xml.CharData:
			txt := string(t)
			if strings.TrimSpace(txt) != "" {
				b.WriteString(txt)
			}
		}
	}
	return normalizeExtractedText(b.String())
}

func extractTextFromXLSX(data []byte) (string, error) {
	wb, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	defer wb.Close()

	parts := make([]string, 0, 16)
	for _, sheet := range wb.GetSheetList() {
		rows, err := wb.GetRows(sheet)
		if err != nil {
			continue
		}
		for _, row := range rows {
			line := strings.TrimSpace(strings.Join(row, "\t"))
			if line != "" {
				parts = append(parts, line)
			}
		}
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("xlsx workbook text not found")
	}
	return strings.Join(parts, "\n"), nil
}

func extractTextFromPDF(data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("empty pdf")
	}

	// Prefer a mature parser first (text-layer extraction).
	if text, err := extractTextFromPDFViaLibrary(data); err == nil {
		text = normalizeExtractedText(text)
		if strings.TrimSpace(text) != "" {
			return text, nil
		}
	}

	chunks := make([]string, 0, 16)

	streams := extractPDFStreams(data)
	logger.Infof("[TRACE] upload.pdf streams=%d size=%d", len(streams), len(data))
	textStreamCount := 0
	for _, stream := range streams {
		decoded := stream
		if maybeFlatePDFStream(stream) {
			if out, err := inflatePDFStream(stream); err == nil && len(out) > 0 {
				decoded = out
			}
		}
		if !isLikelyPDFTextStream(decoded) {
			continue
		}
		textStreamCount++
		text := extractPDFLiteralText(decoded)
		if text != "" {
			chunks = append(chunks, text)
		}
	}

	out := strings.TrimSpace(strings.Join(chunks, "\n"))
	logger.Infof("[TRACE] upload.pdf text_streams=%d extracted_chunks=%d out_len=%d", textStreamCount, len(chunks), len(out))
	if out == "" {
		lower := strings.ToLower(string(data))
		if strings.Contains(lower, "/subtype /image") {
			return "", fmt.Errorf("pdf has no textual layer (ocr required)")
		}
		return "", fmt.Errorf("no textual content found in pdf")
	}
	return out, nil
}

func extractTextFromPDFViaLibrary(data []byte) (string, error) {
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	plain, err := reader.GetPlainText()
	if err != nil {
		return "", err
	}
	raw, err := io.ReadAll(plain)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func isLikelyPDFTextStream(stream []byte) bool {
	if len(stream) == 0 {
		return false
	}
	printable := 0
	for _, b := range stream {
		if b == '\n' || b == '\r' || b == '\t' || (b >= 32 && b <= 126) {
			printable++
		}
	}
	if float64(printable)/float64(len(stream)) < 0.35 {
		return false
	}
	lower := strings.ToLower(string(stream))
	if !strings.Contains(lower, "bt") || !strings.Contains(lower, "et") {
		return false
	}
	return strings.Contains(lower, " tj") ||
		strings.Contains(lower, "]tj") ||
		strings.Contains(lower, ")tj") ||
		strings.Contains(lower, ">tj")
}

func extractPDFStreams(data []byte) [][]byte {
	out := make([][]byte, 0, 8)
	cursor := 0
	for {
		si := bytes.Index(data[cursor:], []byte("stream"))
		if si < 0 {
			break
		}
		si += cursor
		start := si + len("stream")
		if start < len(data) && data[start] == '\r' {
			start++
		}
		if start < len(data) && data[start] == '\n' {
			start++
		}
		ei := bytes.Index(data[start:], []byte("endstream"))
		if ei < 0 {
			break
		}
		ei += start
		if ei > start {
			buf := make([]byte, ei-start)
			copy(buf, data[start:ei])
			out = append(out, buf)
		}
		cursor = ei + len("endstream")
		if cursor >= len(data) {
			break
		}
	}
	return out
}

func maybeFlatePDFStream(stream []byte) bool {
	if len(stream) < 2 {
		return false
	}
	if stream[0] == 0x78 {
		return true
	}
	return false
}

func inflatePDFStream(stream []byte) ([]byte, error) {
	zr, err := zlib.NewReader(bytes.NewReader(stream))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

func extractPDFLiteralText(data []byte) string {
	parts := make([]string, 0, 32)
	for i := 0; i < len(data); i++ {
		if data[i] == '(' {
			if s, next := parsePDFParenString(data, i); next > i {
				if !hasNearbyPDFTextOperator(data, next) {
					i = next - 1
					continue
				}
				s = strings.TrimSpace(s)
				if isLikelyReadablePDFSnippet(s) {
					parts = append(parts, s)
				}
				i = next - 1
			}
			continue
		}
		if data[i] == '<' && i+1 < len(data) && data[i+1] != '<' {
			if s, next := parsePDFHexString(data, i); next > i {
				if !hasNearbyPDFTextOperator(data, next) {
					i = next - 1
					continue
				}
				s = strings.TrimSpace(s)
				if isLikelyReadablePDFSnippet(s) {
					parts = append(parts, s)
				}
				i = next - 1
			}
		}
	}
	return strings.Join(parts, "\n")
}

func hasNearbyPDFTextOperator(data []byte, from int) bool {
	if from < 0 || from >= len(data) {
		return false
	}
	end := from + 24
	if end > len(data) {
		end = len(data)
	}
	seg := strings.ToLower(string(data[from:end]))
	return strings.Contains(seg, "tj")
}

func isLikelyReadablePDFSnippet(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	total := 0
	good := 0
	for _, r := range s {
		total++
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsSpace(r) || unicode.IsPunct(r) {
			good++
		}
	}
	if total == 0 {
		return false
	}
	return float64(good)/float64(total) >= 0.7
}

func parsePDFParenString(data []byte, start int) (string, int) {
	if start >= len(data) || data[start] != '(' {
		return "", start
	}
	depth := 1
	i := start + 1
	b := strings.Builder{}
	for i < len(data) {
		ch := data[i]
		if ch == '\\' {
			if i+1 >= len(data) {
				i++
				continue
			}
			next := data[i+1]
			switch next {
			case 'n':
				b.WriteByte('\n')
				i += 2
				continue
			case 'r':
				b.WriteByte('\r')
				i += 2
				continue
			case 't':
				b.WriteByte('\t')
				i += 2
				continue
			case 'b':
				b.WriteByte('\b')
				i += 2
				continue
			case 'f':
				b.WriteByte('\f')
				i += 2
				continue
			case '\\', '(', ')':
				b.WriteByte(next)
				i += 2
				continue
			}
			if next >= '0' && next <= '7' {
				j := i + 1
				oct := make([]byte, 0, 3)
				for j < len(data) && len(oct) < 3 && data[j] >= '0' && data[j] <= '7' {
					oct = append(oct, data[j])
					j++
				}
				if len(oct) > 0 {
					if v, err := strconv.ParseInt(string(oct), 8, 32); err == nil {
						b.WriteByte(byte(v))
					}
					i = j
					continue
				}
			}
			b.WriteByte(next)
			i += 2
			continue
		}
		if ch == '(' {
			depth++
			b.WriteByte(ch)
			i++
			continue
		}
		if ch == ')' {
			depth--
			if depth == 0 {
				return b.String(), i + 1
			}
			b.WriteByte(ch)
			i++
			continue
		}
		b.WriteByte(ch)
		i++
	}
	return b.String(), i
}

func parsePDFHexString(data []byte, start int) (string, int) {
	i := start + 1
	b := strings.Builder{}
	for i < len(data) && data[i] != '>' {
		if (data[i] >= '0' && data[i] <= '9') || (data[i] >= 'a' && data[i] <= 'f') || (data[i] >= 'A' && data[i] <= 'F') {
			b.WriteByte(data[i])
		}
		i++
	}
	hexStr := b.String()
	if len(hexStr)%2 == 1 {
		hexStr += "0"
	}
	decoded, err := hex.DecodeString(hexStr)
	if err != nil {
		return "", i + 1
	}
	if bytes.HasPrefix(decoded, []byte{0xFE, 0xFF}) && len(decoded) >= 4 {
		runes := make([]rune, 0, len(decoded)/2)
		for j := 2; j+1 < len(decoded); j += 2 {
			r := rune(decoded[j])<<8 | rune(decoded[j+1])
			runes = append(runes, r)
		}
		return string(runes), i + 1
	}
	return string(decoded), i + 1
}

func normalizeExtractedText(in string) string {
	text := strings.ReplaceAll(in, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	text = strings.Join(lines, "\n")
	spaceRe := regexp.MustCompile(`[\t ]+`)
	text = spaceRe.ReplaceAllString(text, " ")
	multiBlank := regexp.MustCompile(`\n{3,}`)
	text = multiBlank.ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
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
