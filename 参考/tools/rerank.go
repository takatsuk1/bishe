//go:build reference
// +build reference

package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type DashScopeRerankTool struct {
	apikey string
	model  string
}

func NewDashScopeRerankTool(apiKey, model string) (*DashScopeRerankTool, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("DASHSCOPE_API_KEY is empty")
	}

	// client := dashscopego.NewTongyiClient(model, apiKey).
	// 	SetUploadCache(qwen.NewMemoryFileCache())

	return &DashScopeRerankTool{
		apikey: apiKey,
		model:  model,
	}, nil
}

type DashScopeRerankRequest struct {
	URL  string   `json:"url" jsonschema_description:"required,description=The url to use dashscopeRerank."`
	Text string   `json:"text" jsonschema_description:"required,description=The text to use dashscope."`
	Docs []string `json:"docs" jsonschema_description:"required,description=The documents to use for reranking."`
}

type DashScopeRerankResponse struct {
	Content string `json:"content" jsonschema_description:"url content"`
}

// Call 执行工具调用
func (t *DashScopeRerankTool) Call(ctx context.Context, url string, query string, docs []string) ([]string, error) {

	if query == "" {
		return nil, fmt.Errorf("missing required parameter: query")
	}
	if docs == nil || len(docs) == 0 {
		return nil, fmt.Errorf("missing required parameter: docs")
	}
	// 请求
	requestBody := map[string]interface{}{
		"model": "gte-rerank-v2",
		"input": map[string]interface{}{
			"query":     query,
			"documents": docs,
		},
		"parameters": map[string]interface{}{
			"return_documents": true,
			"top_n":            len(docs), // 默认返回所有文档，如果需要限制可以修改
		},
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %v", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.apikey) // 假设apiKey是DashScopeRerankTool的一个字段
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("request failed with status code %d: %s", resp.StatusCode, string(body))
	}

	// 响应
	var response struct {
		Output struct {
			Results []struct {
				Document struct {
					Text string `json:"text"`
				} `json:"document"`
				Index          int     `json:"index"`
				RelevanceScore float64 `json:"relevance_score"`
			} `json:"results"`
			Usage struct {
				TotalTokens int `json:"total_tokens"`
			} `json:"usage"`
			RequestID string `json:"request_id"`
		} `json:"output"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to decode response: %v", err)
	}

	// 构造返回结果字符串
	var results []string
	for _, item := range response.Output.Results {
		results = append(results, item.Document.Text)
	}

	return results, nil
}
