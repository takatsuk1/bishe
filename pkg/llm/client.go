package llm

import (
	"ai/pkg/logger"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

func NewClient(baseURL, apiKey string) *Client {
	baseURL = strings.TrimSpace(baseURL)
	return &Client{
		BaseURL: baseURL,
		APIKey:  strings.TrimSpace(apiKey),
		HTTP:    &http.Client{Timeout: 180 * time.Second},
	}
}

type chatCompletionRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Stream      bool      `json:"stream"`
	MaxTokens   *int      `json:"max_tokens,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content any `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

type chatCompletionStreamResponse struct {
	Choices []struct {
		Delta struct {
			Content any `json:"content"`
		} `json:"delta"`
		Message struct {
			Content any `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

type embeddingsRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingsResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
}

func (c *Client) ChatCompletion(ctx context.Context, model string, messages []Message, maxTokens *int, temperature *float64) (string, error) {
	if strings.TrimSpace(c.BaseURL) == "" {
		return "", fmt.Errorf("llm baseURL is empty")
	}
	endpoint, err := joinURL(c.BaseURL, "/v1/chat/completions")
	if err != nil {
		return "", err
	}

	reqBody := chatCompletionRequest{
		Model:       model,
		Messages:    messages,
		Stream:      false,
		MaxTokens:   maxTokens,
		Temperature: temperature,
	}
	bts, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bts))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	start := time.Now()
	promptRunes, messageLens := messageLengthSummary(messages)
	logger.Infof("[TRACE] llm.ChatCompletion start model=%s url=%s message_count=%d prompt_runes=%d message_runes=%s max_tokens=%s temperature=%s", strings.TrimSpace(model), endpoint, len(messages), promptRunes, messageLens, optionalInt(maxTokens), optionalFloat(temperature))
	resp, err := c.HTTP.Do(req)
	if err != nil {
		logger.Warnf("[TRACE] llm.ChatCompletion failed model=%s url=%s elapsed=%s prompt_runes=%d err=%v", strings.TrimSpace(model), endpoint, time.Since(start).Round(time.Millisecond), promptRunes, err)
		if errors.Is(err, context.DeadlineExceeded) || (ctx != nil && ctx.Err() == context.DeadlineExceeded) {
			return "", fmt.Errorf("llm timeout model=%s url=%s elapsed=%s: %w", strings.TrimSpace(model), endpoint, time.Since(start).Round(time.Millisecond), err)
		}
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Warnf("[TRACE] llm.ChatCompletion read_failed model=%s url=%s elapsed=%s prompt_runes=%d err=%v", strings.TrimSpace(model), endpoint, time.Since(start).Round(time.Millisecond), promptRunes, err)
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		logger.Warnf("[TRACE] llm.ChatCompletion http_error model=%s url=%s elapsed=%s prompt_runes=%d status=%d body_len=%d", strings.TrimSpace(model), endpoint, time.Since(start).Round(time.Millisecond), promptRunes, resp.StatusCode, len(body))
		return "", fmt.Errorf("llm http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed chatCompletionResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		logger.Warnf("[TRACE] llm.ChatCompletion invalid_json model=%s url=%s elapsed=%s prompt_runes=%d body_len=%d err=%v", strings.TrimSpace(model), endpoint, time.Since(start).Round(time.Millisecond), promptRunes, len(body), err)
		return "", fmt.Errorf("llm invalid json: %w", err)
	}
	if len(parsed.Choices) == 0 {
		logger.Warnf("[TRACE] llm.ChatCompletion empty_choices model=%s url=%s elapsed=%s prompt_runes=%d body_len=%d", strings.TrimSpace(model), endpoint, time.Since(start).Round(time.Millisecond), promptRunes, len(body))
		return "", fmt.Errorf("llm empty choices")
	}

	content := contentToString(parsed.Choices[0].Message.Content)
	logger.Infof("[TRACE] llm.ChatCompletion done model=%s url=%s elapsed=%s prompt_runes=%d response_runes=%d", strings.TrimSpace(model), endpoint, time.Since(start).Round(time.Millisecond), promptRunes, len([]rune(strings.TrimSpace(content))))
	return content, nil
}

func (c *Client) ChatCompletionStream(
	ctx context.Context,
	model string,
	messages []Message,
	maxTokens *int,
	temperature *float64,
	onDelta func(delta string) error,
) (string, error) {
	if strings.TrimSpace(c.BaseURL) == "" {
		return "", fmt.Errorf("llm baseURL is empty")
	}
	endpoint, err := joinURL(c.BaseURL, "/v1/chat/completions")
	if err != nil {
		return "", err
	}

	reqBody := chatCompletionRequest{
		Model:       model,
		Messages:    messages,
		Stream:      true,
		MaxTokens:   maxTokens,
		Temperature: temperature,
	}
	bts, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bts))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	start := time.Now()
	promptRunes, messageLens := messageLengthSummary(messages)
	logger.Infof("[TRACE] llm.ChatCompletionStream start model=%s url=%s message_count=%d prompt_runes=%d message_runes=%s max_tokens=%s temperature=%s", strings.TrimSpace(model), endpoint, len(messages), promptRunes, messageLens, optionalInt(maxTokens), optionalFloat(temperature))
	resp, err := c.HTTP.Do(req)
	if err != nil {
		logger.Warnf("[TRACE] llm.ChatCompletionStream failed model=%s url=%s elapsed=%s prompt_runes=%d err=%v", strings.TrimSpace(model), endpoint, time.Since(start).Round(time.Millisecond), promptRunes, err)
		if errors.Is(err, context.DeadlineExceeded) || (ctx != nil && ctx.Err() == context.DeadlineExceeded) {
			return "", fmt.Errorf("llm timeout model=%s url=%s elapsed=%s: %w", strings.TrimSpace(model), endpoint, time.Since(start).Round(time.Millisecond), err)
		}
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		logger.Warnf("[TRACE] llm.ChatCompletionStream http_error model=%s url=%s elapsed=%s prompt_runes=%d status=%d body_len=%d", strings.TrimSpace(model), endpoint, time.Since(start).Round(time.Millisecond), promptRunes, resp.StatusCode, len(body))
		return "", fmt.Errorf("llm http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	reader := bufio.NewReader(resp.Body)
	var full strings.Builder
	var eventData strings.Builder
	firstDeltaAt := time.Duration(0)
	chunkCount := 0

	flushEvent := func() error {
		payload := strings.TrimSpace(eventData.String())
		eventData.Reset()
		if payload == "" {
			return nil
		}
		if payload == "[DONE]" {
			return io.EOF
		}

		var parsed chatCompletionStreamResponse
		if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
			return fmt.Errorf("llm stream invalid json: %w", err)
		}
		for _, choice := range parsed.Choices {
			delta := contentToString(choice.Delta.Content)
			if delta == "" {
				delta = contentToString(choice.Message.Content)
			}
			if delta == "" {
				continue
			}
			chunkCount++
			if firstDeltaAt == 0 {
				firstDeltaAt = time.Since(start)
				logger.Infof("[TRACE] llm.ChatCompletionStream first_delta model=%s url=%s after=%s prompt_runes=%d delta_runes=%d", strings.TrimSpace(model), endpoint, firstDeltaAt.Round(time.Millisecond), promptRunes, len([]rune(strings.TrimSpace(delta))))
			}
			full.WriteString(delta)
			if onDelta != nil {
				if err := onDelta(delta); err != nil {
					return err
				}
			}
		}
		return nil
	}

	for {
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			logger.Warnf("[TRACE] llm.ChatCompletionStream read_failed model=%s url=%s elapsed=%s prompt_runes=%d err=%v", strings.TrimSpace(model), endpoint, time.Since(start).Round(time.Millisecond), promptRunes, err)
			return "", err
		}

		trimmed := strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(trimmed) == "" {
			if ferr := flushEvent(); ferr != nil {
				if errors.Is(ferr, io.EOF) {
					result := strings.TrimSpace(full.String())
					logger.Infof("[TRACE] llm.ChatCompletionStream done model=%s url=%s elapsed=%s first_delta=%s prompt_runes=%d response_runes=%d chunks=%d", strings.TrimSpace(model), endpoint, time.Since(start).Round(time.Millisecond), firstDeltaAt.Round(time.Millisecond), promptRunes, len([]rune(result)), chunkCount)
					return result, nil
				}
				logger.Warnf("[TRACE] llm.ChatCompletionStream flush_failed model=%s url=%s elapsed=%s prompt_runes=%d err=%v", strings.TrimSpace(model), endpoint, time.Since(start).Round(time.Millisecond), promptRunes, ferr)
				return "", ferr
			}
		} else if strings.HasPrefix(trimmed, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
			if data != "" {
				if eventData.Len() > 0 {
					eventData.WriteByte('\n')
				}
				eventData.WriteString(data)
			}
		}

		if errors.Is(err, io.EOF) {
			break
		}
	}

	if err := flushEvent(); err != nil && !errors.Is(err, io.EOF) {
		logger.Warnf("[TRACE] llm.ChatCompletionStream final_flush_failed model=%s url=%s elapsed=%s prompt_runes=%d err=%v", strings.TrimSpace(model), endpoint, time.Since(start).Round(time.Millisecond), promptRunes, err)
		return "", err
	}
	result := strings.TrimSpace(full.String())
	logger.Infof("[TRACE] llm.ChatCompletionStream done model=%s url=%s elapsed=%s first_delta=%s prompt_runes=%d response_runes=%d chunks=%d", strings.TrimSpace(model), endpoint, time.Since(start).Round(time.Millisecond), firstDeltaAt.Round(time.Millisecond), promptRunes, len([]rune(result)), chunkCount)
	return result, nil
}

func (c *Client) Embeddings(ctx context.Context, model string, input []string) ([][]float32, error) {
	if strings.TrimSpace(c.BaseURL) == "" {
		return nil, fmt.Errorf("llm baseURL is empty")
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, fmt.Errorf("embedding model is empty")
	}
	if len(input) == 0 {
		return nil, fmt.Errorf("embedding input is empty")
	}
	endpoint, err := joinURL(c.BaseURL, "/v1/embeddings")
	if err != nil {
		return nil, err
	}

	reqBody := embeddingsRequest{Model: model, Input: input}
	bts, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bts))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("llm embeddings http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed embeddingsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("llm embeddings invalid json: %w", err)
	}
	if len(parsed.Data) == 0 {
		return nil, fmt.Errorf("llm embeddings empty data")
	}

	out := make([][]float32, 0, len(parsed.Data))
	for _, item := range parsed.Data {
		vec := make([]float32, 0, len(item.Embedding))
		for _, v := range item.Embedding {
			vec = append(vec, float32(v))
		}
		out = append(out, vec)
	}
	return out, nil
}

func contentToString(content any) string {
	if content == nil {
		return ""
	}
	switch v := content.(type) {
	case string:
		s := strings.TrimSpace(v)
		if strings.EqualFold(s, "null") || s == "<nil>" {
			return ""
		}
		return v
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if item == nil {
				continue
			}
			switch t := item.(type) {
			case map[string]any:
				txt := fmt.Sprint(t["text"])
				if strings.TrimSpace(txt) != "" && txt != "<nil>" && !strings.EqualFold(strings.TrimSpace(txt), "null") {
					parts = append(parts, txt)
				}
			default:
				txt := fmt.Sprint(item)
				if strings.TrimSpace(txt) != "" && txt != "<nil>" && !strings.EqualFold(strings.TrimSpace(txt), "null") {
					parts = append(parts, txt)
				}
			}
		}
		return strings.Join(parts, "")
	default:
		b, _ := json.Marshal(v)
		s := strings.TrimSpace(string(b))
		if s == "" || strings.EqualFold(s, "null") || s == "<nil>" {
			return ""
		}
		return s
	}
}

func joinURL(base, p string) (string, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/v1") && strings.HasPrefix(p, "/v1/") {
		p = strings.TrimPrefix(p, "/v1")
	}
	u.Path = path.Join(u.Path, p)
	return u.String(), nil
}

func messageLengthSummary(messages []Message) (int, string) {
	if len(messages) == 0 {
		return 0, "[]"
	}
	total := 0
	parts := make([]string, 0, len(messages))
	for i, msg := range messages {
		runes := len([]rune(strings.TrimSpace(msg.Content)))
		total += runes
		role := strings.TrimSpace(msg.Role)
		if role == "" {
			role = "unknown"
		}
		parts = append(parts, fmt.Sprintf("%d:%s=%d", i, role, runes))
	}
	return total, "[" + strings.Join(parts, ",") + "]"
}

func optionalInt(v *int) string {
	if v == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%d", *v)
}

func optionalFloat(v *float64) string {
	if v == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%.3f", *v)
}
