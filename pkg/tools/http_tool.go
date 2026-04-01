package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"ai/pkg/logger"
)

var quotedTemplateTokenRE = regexp.MustCompile(`"\{\{\s*(\w+)\s*\}\}"`)

var templateTokenRE = regexp.MustCompile(`\{\{\s*(\w+)\s*\}\}`)

type HTTPToolConfig struct {
	Method       string            `json:"method"`
	URL          string            `json:"url"`
	Headers      map[string]string `json:"headers,omitempty"`
	BodyTemplate string            `json:"body_template,omitempty"`
	Timeout      time.Duration     `json:"timeout,omitempty"`
}

type HTTPTool struct {
	*BaseTool
	config     HTTPToolConfig
	httpClient *http.Client
}

func NewHTTPTool(name string, description string, parameters []ToolParameter, config HTTPToolConfig) *HTTPTool {
	if config.Timeout <= 0 {
		config.Timeout = 30 * time.Second
	}
	if config.Method == "" {
		config.Method = "GET"
	}
	config.Method = strings.ToUpper(config.Method)

	return &HTTPTool{
		BaseTool:   NewBaseTool(name, ToolTypeHTTP, description, parameters),
		config:     config,
		httpClient: &http.Client{Timeout: config.Timeout},
	}
}

func (t *HTTPTool) Execute(ctx context.Context, params map[string]any) (map[string]any, error) {
	start := time.Now()
	logger.Infof("[TRACE] HTTPTool.Execute start name=%s method=%s url=%s", t.info.Name, t.config.Method, t.config.URL)

	if err := ValidateParameters(params, t.info.Parameters); err != nil {
		return nil, err
	}
	params = ApplyDefaults(params, t.info.Parameters)

	resolvedURL := resolveTemplate(t.config.URL, params)
	resolvedBody := resolveTemplate(t.config.BodyTemplate, params)
	if strings.TrimSpace(t.config.BodyTemplate) != "" {
		if jsonBody, err := resolveJSONTemplate(t.config.BodyTemplate, params); err == nil {
			resolvedBody = jsonBody
		}
	}

	var bodyReader io.Reader
	if resolvedBody != "" && (t.config.Method == "POST" || t.config.Method == "PUT" || t.config.Method == "PATCH") {
		bodyReader = strings.NewReader(resolvedBody)
	}

	req, err := http.NewRequestWithContext(ctx, t.config.Method, resolvedURL, bodyReader)
	if err != nil {
		logger.Infof("[TRACE] HTTPTool.Execute new_request_failed err=%v", err)
		return nil, fmt.Errorf("create request failed: %w", err)
	}

	for key, value := range t.config.Headers {
		resolvedValue := resolveTemplate(value, params)
		req.Header.Set(key, resolvedValue)
	}
	if bodyReader != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := t.httpClient.Do(req)
	if err != nil {
		logger.Infof("[TRACE] HTTPTool.Execute http_do_failed dur=%s err=%v", time.Since(start), err)
		return nil, fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		logger.Infof("[TRACE] HTTPTool.Execute read_body_failed dur=%s err=%v", time.Since(start), err)
		return nil, fmt.Errorf("read response body failed: %w", err)
	}

	result := map[string]any{
		"status_code": resp.StatusCode,
		"headers":     resp.Header,
		"body":        string(respBody),
	}

	var jsonBody map[string]any
	if err := json.Unmarshal(respBody, &jsonBody); err == nil {
		result["json"] = jsonBody
	}

	logger.Infof("[TRACE] HTTPTool.Execute done name=%s status=%d dur=%s", t.info.Name, resp.StatusCode, time.Since(start))

	if resp.StatusCode >= 400 {
		return result, fmt.Errorf("http error: status %d, body: %s", resp.StatusCode, string(respBody))
	}

	return result, nil
}

func resolveTemplate(template string, params map[string]any) string {
	if template == "" {
		return ""
	}

	re := regexp.MustCompile(`\{\{\s*(\w+)\s*\}\}`)
	result := re.ReplaceAllStringFunc(template, func(match string) string {
		submatches := re.FindStringSubmatch(match)
		if len(submatches) > 1 {
			key := submatches[1]
			if val, ok := params[key]; ok {
				return fmt.Sprintf("%v", val)
			}
		}
		return match
	})

	return result
}

func resolveJSONTemplate(template string, params map[string]any) (string, error) {
	resolved := quotedTemplateTokenRE.ReplaceAllStringFunc(template, func(match string) string {
		sub := quotedTemplateTokenRE.FindStringSubmatch(match)
		if len(sub) != 2 {
			return match
		}
		val, ok := params[sub[1]]
		if !ok {
			return match
		}
		b, err := json.Marshal(val)
		if err != nil {
			return match
		}
		return string(b)
	})

	resolved = templateTokenRE.ReplaceAllStringFunc(resolved, func(match string) string {
		sub := templateTokenRE.FindStringSubmatch(match)
		if len(sub) != 2 {
			return match
		}
		val, ok := params[sub[1]]
		if !ok {
			return match
		}
		b, err := json.Marshal(val)
		if err != nil {
			return match
		}
		return string(b)
	})

	var probe any
	if err := json.Unmarshal([]byte(resolved), &probe); err != nil {
		return "", err
	}
	return resolved, nil
}

type HTTPToolDefinition struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Parameters  []ToolParameter   `json:"parameters"`
	Config      HTTPToolConfig    `json:"config"`
	Headers     map[string]string `json:"headers,omitempty"`
}

func NewHTTPToolFromDefinition(def HTTPToolDefinition) *HTTPTool {
	if def.Headers != nil {
		if def.Config.Headers == nil {
			def.Config.Headers = make(map[string]string)
		}
		for k, v := range def.Headers {
			def.Config.Headers[k] = v
		}
	}
	return NewHTTPTool(def.Name, def.Description, def.Parameters, def.Config)
}

func (t *HTTPTool) ToDefinition(id string) HTTPToolDefinition {
	return HTTPToolDefinition{
		ID:          id,
		Name:        t.info.Name,
		Description: t.info.Description,
		Parameters:  t.info.Parameters,
		Config:      t.config,
	}
}

type RawHTTPTool struct {
	*BaseTool
	executeFunc func(ctx context.Context, params map[string]any) (map[string]any, error)
}

func NewRawHTTPTool(name string, description string, parameters []ToolParameter, executeFunc func(ctx context.Context, params map[string]any) (map[string]any, error)) *RawHTTPTool {
	return &RawHTTPTool{
		BaseTool:    NewBaseTool(name, ToolTypeHTTP, description, parameters),
		executeFunc: executeFunc,
	}
}

func (t *RawHTTPTool) Execute(ctx context.Context, params map[string]any) (map[string]any, error) {
	return t.executeFunc(ctx, params)
}

func BuildJSONBody(template string, params map[string]any) (string, error) {
	resolved := resolveTemplate(template, params)
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(resolved), "", "  "); err != nil {
		return resolved, nil
	}
	return buf.String(), nil
}
