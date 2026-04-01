package httpagent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ai/pkg/logger"
	"ai/pkg/protocol"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
}

func NewClient(baseURL string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func (c *Client) SendMessage(ctx context.Context, message protocol.Message) (string, error) {
	start := time.Now()
	reqID := RequestIDFromContext(ctx)
	logger.Infof("[TRACE] httpagent.SendMessage start rid=%s base=%s task=%v role=%s", reqID, c.baseURL, message.TaskID, message.Role)

	payload, err := json.Marshal(protocol.SendMessageRequest{Message: message})
	if err != nil {
		logger.Infof("[TRACE] httpagent.SendMessage rid=%s marshal_failed err=%v", reqID, err)
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/tasks/send", bytes.NewReader(payload))
	if err != nil {
		logger.Infof("[TRACE] httpagent.SendMessage rid=%s new_request_failed err=%v", reqID, err)
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if reqID != "" {
		req.Header.Set("X-Request-ID", reqID)
	}
	if token := AuthorizationTokenFromContext(ctx); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		logger.Infof("[TRACE] httpagent.SendMessage rid=%s do_failed dur=%s err=%v", reqID, time.Since(start), err)
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		logger.Infof("[TRACE] httpagent.SendMessage rid=%s http_status=%d dur=%s body=%q", reqID, resp.StatusCode, time.Since(start), string(body))
		return "", fmt.Errorf("send message failed: %s", string(body))
	}
	var out protocol.SendMessageResponse
	if err = json.NewDecoder(resp.Body).Decode(&out); err != nil {
		logger.Infof("[TRACE] httpagent.SendMessage rid=%s decode_failed dur=%s err=%v", reqID, time.Since(start), err)
		return "", err
	}
	if out.TaskID == "" {
		logger.Infof("[TRACE] httpagent.SendMessage rid=%s empty_task_id dur=%s", reqID, time.Since(start))
		return "", fmt.Errorf("empty task id")
	}
	logger.Infof("[TRACE] httpagent.SendMessage done rid=%s assigned_task=%s dur=%s", reqID, out.TaskID, time.Since(start))
	return out.TaskID, nil
}

func (c *Client) GetTask(ctx context.Context, taskID string) (*protocol.Task, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/tasks/"+taskID, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get task failed: %s", string(body))
	}
	var out protocol.Task
	if err = json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) CancelTask(ctx context.Context, taskID string) (*protocol.Task, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/tasks/"+taskID+"/cancel", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("cancel task failed: %s", string(body))
	}
	var out protocol.Task
	if err = json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) StreamTaskEvents(ctx context.Context, taskID string) (<-chan protocol.StreamEvent, <-chan error) {
	eventCh := make(chan protocol.StreamEvent, 32)
	errCh := make(chan error, 1)
	go func() {
		defer close(eventCh)
		defer close(errCh)
		start := time.Now()
		reqID := RequestIDFromContext(ctx)
		logger.Infof("[TRACE] httpagent.StreamTaskEvents start rid=%s base=%s task=%s", reqID, c.baseURL, taskID)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/tasks/"+taskID+"/events", nil)
		if err != nil {
			logger.Infof("[TRACE] httpagent.StreamTaskEvents rid=%s new_request_failed err=%v", reqID, err)
			errCh <- err
			return
		}
		if reqID != "" {
			req.Header.Set("X-Request-ID", reqID)
		}
		if token := AuthorizationTokenFromContext(ctx); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			logger.Infof("[TRACE] httpagent.StreamTaskEvents rid=%s do_failed dur=%s err=%v", reqID, time.Since(start), err)
			errCh <- err
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			logger.Infof("[TRACE] httpagent.StreamTaskEvents rid=%s http_status=%d dur=%s body=%q", reqID, resp.StatusCode, time.Since(start), string(body))
			errCh <- fmt.Errorf("stream task events failed: %s", string(body))
			return
		}
		logger.Infof("[TRACE] httpagent.StreamTaskEvents connected rid=%s dur=%s", reqID, time.Since(start))
		scanner := bufio.NewScanner(resp.Body)
		seenAny := false
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			var ev protocol.StreamEvent
			if err = json.Unmarshal([]byte(data), &ev); err != nil {
				logger.Infof("[TRACE] httpagent.StreamTaskEvents rid=%s unmarshal_failed err=%v data=%q", reqID, err, data)
				errCh <- err
				return
			}
			if !seenAny {
				seenAny = true
				logger.Infof("[TRACE] httpagent.StreamTaskEvents first_event rid=%s after=%s", reqID, time.Since(start))
			}
			eventCh <- ev
			if ev.TaskStatusUpdate != nil && ev.TaskStatusUpdate.Status.State.IsTerminal() {
				logger.Infof("[TRACE] httpagent.StreamTaskEvents terminal rid=%s state=%s total=%s", reqID, ev.TaskStatusUpdate.Status.State, time.Since(start))
				return
			}
		}
		if err = scanner.Err(); err != nil {
			logger.Infof("[TRACE] httpagent.StreamTaskEvents scanner_err rid=%s total=%s err=%v", reqID, time.Since(start), err)
			errCh <- err
			return
		}
		logger.Infof("[TRACE] httpagent.StreamTaskEvents eof rid=%s total=%s", reqID, time.Since(start))
	}()
	return eventCh, errCh
}
