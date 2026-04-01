package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ai/pkg/logger"
)

type JinaConfig struct {
	APIKey     string
	HTTPClient *http.Client
}

func (conf *JinaConfig) validate() error {
	if conf == nil {
		return fmt.Errorf("config is nil")
	}
	if conf.HTTPClient == nil {
		conf.HTTPClient = &http.Client{Timeout: time.Second * 60}
	}
	return nil
}

func NewJinaReader(_ context.Context, config *JinaConfig) (*JinaReader, error) {
	if config == nil {
		config = &JinaConfig{}
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	return &JinaReader{config: config}, nil
}

type JinaReader struct {
	config *JinaConfig
}

func (reader *JinaReader) Read(ctx context.Context, url string) (string, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		return "", fmt.Errorf("url is empty")
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return "", fmt.Errorf("url must start with http:// or https://")
	}
	start := time.Now()
	logger.Infof("[TRACE] jina.Read start url=%q", url)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://r.jina.ai/"+url, nil)
	if err != nil {
		logger.Infof("[TRACE] jina.Read new_request_failed dur=%s err=%v", time.Since(start), err)
		return "", fmt.Errorf("new request: %w", err)
	}
	if reader.config != nil && strings.TrimSpace(reader.config.APIKey) != "" {
		httpReq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(reader.config.APIKey))
	}
	httpReq.Header.Set("Accept", "text/plain")

	httpRsp, err := reader.config.HTTPClient.Do(httpReq)
	if err != nil {
		logger.Infof("[TRACE] jina.Read http_do_failed dur=%s err=%v", time.Since(start), err)
		return "", fmt.Errorf("do http fail: %w", err)
	}
	defer httpRsp.Body.Close()
	logger.Infof("[TRACE] jina.Read http_status=%d dur=%s", httpRsp.StatusCode, time.Since(start))

	// Avoid reading unbounded content.
	body, err := io.ReadAll(io.LimitReader(httpRsp.Body, 4<<20))
	if err != nil {
		logger.Infof("[TRACE] jina.Read read_body_failed dur=%s err=%v", time.Since(start), err)
		return "", fmt.Errorf("read http body fail: %w", err)
	}
	if httpRsp.StatusCode < 200 || httpRsp.StatusCode >= 300 {
		logger.Infof("[TRACE] jina.Read not_ok dur=%s body_prefix=%q", time.Since(start), string(body[:minInt(256, len(body))]))
		return "", fmt.Errorf("jina http %d: %s", httpRsp.StatusCode, strings.TrimSpace(string(body)))
	}
	content := strings.TrimSpace(string(body))
	logger.Infof("[TRACE] jina.Read done dur=%s len=%d", time.Since(start), len(content))
	return content, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
