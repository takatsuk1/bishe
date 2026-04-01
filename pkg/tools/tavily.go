package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ai/pkg/logger"
)

type TavilyConfig struct {
	APIKey     string
	HTTPClient *http.Client
}

func NewTavilyClient(_ context.Context, config *TavilyConfig) (*TavilyClient, error) {
	if config == nil {
		config = &TavilyConfig{}
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	return &TavilyClient{config: config}, nil
}

func (conf *TavilyConfig) validate() error {
	if conf == nil {
		return fmt.Errorf("config is nil")
	}
	if conf.APIKey == "" {
		return fmt.Errorf("apikey is nil")
	}
	if conf.HTTPClient == nil {
		conf.HTTPClient = &http.Client{Timeout: time.Second * 10}
	}
	return nil
}

type TavilyClient struct {
	config *TavilyConfig
}

type SearchRequest struct {
	Query       string `json:"query"`
	SearchDepth string `json:"search_depth,omitempty"`
	MaxResults  int    `json:"max_results,omitempty"`
}

type SearchResponse struct {
	Results      []Result `json:"results"`
	Query        string   `json:"query"`
	ResponseTime float64  `json:"response_time"`
}

type Result struct {
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

type ErrorResponse struct {
	Detail struct {
		Error string `json:"error"`
	} `json:"detail"`
}

func (cli *TavilyClient) Search(ctx context.Context, request *SearchRequest) (*SearchResponse, error) {
	start := time.Now()
	q := ""
	depth := ""
	max := 0
	if request != nil {
		q = request.Query
		depth = request.SearchDepth
		max = request.MaxResults
	}
	logger.Infof("[TRACE] tavily.Search start query=%q depth=%q max=%d", q, depth, max)

	requestBodyBytes, err := json.Marshal(request)
	if err != nil {
		logger.Infof("[TRACE] tavily.Search marshal_failed dur=%s err=%v", time.Since(start), err)
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.tavily.com/search", bytes.NewBuffer(requestBodyBytes))
	if err != nil {
		logger.Infof("[TRACE] tavily.Search new_request_failed dur=%s err=%v", time.Since(start), err)
		return nil, fmt.Errorf("new request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+cli.config.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpRsp, err := cli.config.HTTPClient.Do(httpReq)
	if err != nil {
		logger.Infof("[TRACE] tavily.Search http_do_failed dur=%s err=%v", time.Since(start), err)
		return nil, fmt.Errorf("do http fail: %w", err)
	}
	defer httpRsp.Body.Close()
	logger.Infof("[TRACE] tavily.Search http_status=%d dur=%s", httpRsp.StatusCode, time.Since(start))

	if httpRsp.StatusCode >= http.StatusMultipleChoices {
		responseBodyBytes, err := io.ReadAll(httpRsp.Body)
		if err != nil {
			logger.Infof("[TRACE] tavily.Search read_err_body_failed dur=%s err=%v", time.Since(start), err)
			return nil, fmt.Errorf("read http body fail: %w", err)
		}
		errorResponse := &ErrorResponse{}
		if err = json.Unmarshal(responseBodyBytes, errorResponse); err != nil {
			logger.Infof("[TRACE] tavily.Search unmarshal_err_body_failed dur=%s err=%v body=%q", time.Since(start), err, string(responseBodyBytes))
			return nil, fmt.Errorf("unmarshal body fail: %w", err)
		}
		logger.Infof("[TRACE] tavily.Search http_not_ok dur=%s status=%s detail=%q", time.Since(start), httpRsp.Status, errorResponse.Detail.Error)
		return nil, fmt.Errorf("http stats not ok: %s, code: %d, detail: %s", httpRsp.Status, httpRsp.StatusCode, errorResponse.Detail.Error)
	}

	responseBodyBytes, err := io.ReadAll(httpRsp.Body)
	if err != nil {
		logger.Infof("[TRACE] tavily.Search read_body_failed dur=%s err=%v", time.Since(start), err)
		return nil, fmt.Errorf("read http body fail: %w", err)
	}
	searchResponse := &SearchResponse{}
	if err = json.Unmarshal(responseBodyBytes, searchResponse); err != nil {
		logger.Infof("[TRACE] tavily.Search unmarshal_body_failed dur=%s err=%v body_prefix=%q", time.Since(start), err, string(responseBodyBytes[:min(256, len(responseBodyBytes))]))
		return nil, fmt.Errorf("unmarshal body fail: %w", err)
	}

	count := 0
	if searchResponse != nil {
		count = len(searchResponse.Results)
	}
	if count > 0 {
		preview := make([]string, 0, min(3, count))
		for i := 0; i < count && i < 3; i++ {
			r := searchResponse.Results[i]
			title := strings.TrimSpace(r.Title)
			if title == "" {
				title = r.URL
			}
			preview = append(preview, fmt.Sprintf("%s (%s)", title, r.URL))
		}
		logger.Infof("[TRACE] tavily.Search done dur=%s results=%d top=%q", time.Since(start), count, preview)
	} else {
		logger.Infof("[TRACE] tavily.Search done dur=%s results=0", time.Since(start))
	}
	return searchResponse, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
