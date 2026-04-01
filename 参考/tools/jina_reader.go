//go:build reference
// +build reference

package tools

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

type JinaConfig struct {
	APIKey     string
	HTTPClient *http.Client
}

// validate validates the configuration and sets default values if not provided.
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
	return &JinaReader{
		config: config,
	}, nil
}

type JinaReader struct {
	config *JinaConfig
}

type ExtractRequest struct {
	URL string `json:"url" jsonschema_description:"required,description=The url to extract read."`
}

type ExtractResponse struct {
	Content string `json:"content" jsonschema_description:"url content"`
}

func (tool *JinaReader) Tool() (tool.BaseTool, error) {
	searchTool, err := utils.InferTool("extract", "将 URL 转换为大模型友好输入",
		func(ctx context.Context, input ExtractRequest) (output ExtractResponse, err error) {
			content, err := tool.Read(ctx, input.URL)
			if err != nil {
				return ExtractResponse{}, fmt.Errorf("failed to read url: %w", err)
			}
			return ExtractResponse{Content: content}, nil
		})
	if err != nil {
		return nil, fmt.Errorf("failed to infer tool: %w", err)
	}
	return searchTool, nil
}

func (tool *JinaReader) Read(ctx context.Context, url string) (string, error) {
	// httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
	// 	"https://r.jina.ai/"+url, nil)
	// if err != nil {
	// 	return "", fmt.Errorf("new request: %w", err)
	// }
	// if tool.config.APIKey != "" {
	// 	httpReq.Header.Set("Authorization", "Bearer "+tool.config.APIKey)
	// }
	// httpRsp, err := tool.config.HTTPClient.Do(httpReq)
	// if err != nil {
	// 	return "", fmt.Errorf("do http fail: %w", err)
	// }
	// defer httpRsp.Body.Close()

	// responseBodyBytes, err := io.ReadAll(httpRsp.Body)
	// if err != nil {
	// 	return "", fmt.Errorf("read http body fail: %w", err)
	// }
	// return string(responseBodyBytes), nil

	return "⚠️ 当前无法读取文章内容，请稍后再试或提供其他信息。", nil
}
