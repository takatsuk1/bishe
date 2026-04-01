package httpagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"ai/pkg/logger"
	"ai/pkg/protocol"
	"ai/pkg/taskmanager"
)

type Processor interface {
	ProcessMessage(ctx context.Context, message protocol.Message, manager taskmanager.Manager) (string, <-chan protocol.StreamEvent, error)
}

type Server struct {
	card      protocol.AgentCard
	manager   taskmanager.Manager
	processor Processor
}

func NewServer(card protocol.AgentCard, manager taskmanager.Manager, processor Processor) (*Server, error) {
	if manager == nil {
		return nil, errors.New("manager is nil")
	}
	if processor == nil {
		return nil, errors.New("processor is nil")
	}
	return &Server{card: card, manager: manager, processor: processor}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/agent.json", s.handleAgentCard)
	mux.HandleFunc("/v1/tasks/send", s.handleSendMessage)
	mux.HandleFunc("/v1/tasks/", s.handleTaskOps)
	return mux
}

func (s *Server) handleAgentCard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, s.card)
}

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rid := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if rid == "" {
		rid = fmt.Sprintf("rid-%d", time.Now().UnixNano())
	}
	w.Header().Set("X-Request-ID", rid)
	// IMPORTANT: detach task execution from the HTTP request context.
	// The request context is canceled as soon as we finish this handler, but the
	// task workflow must continue running in the background.
	ctx := WithRequestID(context.Background(), rid)
	start := time.Now()
	logger.Infof("[TRACE] httpagent.Server /v1/tasks/send start rid=%s from=%s", rid, r.RemoteAddr)

	var req protocol.SendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.Infof("[TRACE] httpagent.Server /v1/tasks/send rid=%s decode_failed err=%v", rid, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := req.Message.Validate(); err != nil {
		logger.Infof("[TRACE] httpagent.Server /v1/tasks/send rid=%s validate_failed err=%v", rid, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	logger.Infof("[TRACE] httpagent.Server /v1/tasks/send rid=%s in_task=%v role=%s", rid, req.Message.TaskID, req.Message.Role)
	taskID, _, err := s.processor.ProcessMessage(ctx, req.Message, s.manager)
	if err != nil {
		logger.Infof("[TRACE] httpagent.Server /v1/tasks/send rid=%s process_failed dur=%s err=%v", rid, time.Since(start), err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logger.Infof("[TRACE] httpagent.Server /v1/tasks/send done rid=%s assigned_task=%s dur=%s", rid, taskID, time.Since(start))
	writeJSON(w, http.StatusOK, protocol.SendMessageResponse{TaskID: taskID})
}

func (s *Server) handleTaskOps(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/tasks/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	taskID := parts[0]

	if len(parts) == 1 && r.Method == http.MethodGet {
		s.handleGetTask(w, r, taskID)
		return
	}
	if len(parts) == 2 && parts[1] == "cancel" && r.Method == http.MethodPost {
		s.handleCancelTask(w, r, taskID)
		return
	}
	if len(parts) == 2 && parts[1] == "events" && r.Method == http.MethodGet {
		s.handleStreamEvents(w, r, taskID)
		return
	}
	http.NotFound(w, r)
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request, taskID string) {
	task, err := s.manager.GetTask(r.Context(), taskID)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, taskmanager.ErrTaskNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleCancelTask(w http.ResponseWriter, r *http.Request, taskID string) {
	if err := s.manager.CancelTask(r.Context(), taskID); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, taskmanager.ErrTaskNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	task, err := s.manager.GetTask(r.Context(), taskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleStreamEvents(w http.ResponseWriter, r *http.Request, taskID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	rid := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if rid == "" {
		rid = fmt.Sprintf("rid-%d", time.Now().UnixNano())
	}
	w.Header().Set("X-Request-ID", rid)
	ctx := WithRequestID(r.Context(), rid)
	start := time.Now()
	logger.Infof("[TRACE] httpagent.Server /events start rid=%s task=%s from=%s", rid, taskID, r.RemoteAddr)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Emit current snapshot first so late subscribers still observe terminal state.
	if task, err := s.manager.GetTask(ctx, taskID); err == nil {
		initial := protocol.NewTaskStatusEvent(taskID, task.Status)
		if b, marshalErr := json.Marshal(initial); marshalErr == nil {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
		if task.Status.State.IsTerminal() {
			logger.Infof("[TRACE] httpagent.Server /events rid=%s task=%s terminal_snapshot state=%s dur=%s", rid, taskID, task.Status.State, time.Since(start))
			return
		}
	}

	eventCh, err := s.manager.SubscribeTask(ctx, taskID)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, taskmanager.ErrTaskNotFound) {
			status = http.StatusNotFound
		}
		logger.Infof("[TRACE] httpagent.Server /events rid=%s subscribe_failed task=%s dur=%s err=%v", rid, taskID, time.Since(start), err)
		http.Error(w, err.Error(), status)
		return
	}

	logger.Infof("[TRACE] httpagent.Server /events subscribed rid=%s task=%s dur=%s", rid, taskID, time.Since(start))
	for {
		select {
		case <-r.Context().Done():
			logger.Infof("[TRACE] httpagent.Server /events done rid=%s task=%s total=%s err=%v", rid, taskID, time.Since(start), r.Context().Err())
			return
		case ev, ok := <-eventCh:
			if !ok {
				logger.Infof("[TRACE] httpagent.Server /events ch_closed rid=%s task=%s total=%s", rid, taskID, time.Since(start))
				return
			}
			b, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
			if ev.TaskStatusUpdate != nil && ev.TaskStatusUpdate.Status.State.IsTerminal() {
				logger.Infof("[TRACE] httpagent.Server /events terminal rid=%s task=%s state=%s total=%s", rid, taskID, ev.TaskStatusUpdate.Status.State, time.Since(start))
				return
			}
		case <-time.After(25 * time.Second):
			_, _ = fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
