//go:build reference
// +build reference

package tools

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/devinyf/dashscopego"
	"github.com/devinyf/dashscopego/qwen"
)

type DashScopeVLTool struct {
	client *dashscopego.TongyiClient
	model  string
}

func NewDashScopeVLTool(apiKey, model string) (*DashScopeVLTool, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("DASHSCOPE_API_KEY is empty")
	}

	client := dashscopego.NewTongyiClient(model, apiKey).
		SetUploadCache(qwen.NewMemoryFileCache())

	return &DashScopeVLTool{
		client: client,
		model:  model,
	}, nil
}

type DashScopeRequest struct {
	URL  string `json:"url" jsonschema_description:"required,description=The url to use dashscope."`
	Text string `json:"text" jsonschema_description:"required,description=The text to use dashscope."`
}

type DashScopeResponse struct {
	Content string `json:"content" jsonschema_description:"url content"`
}

// Info 返回工具信息
func (tool *DashScopeVLTool) Tool() (tool.BaseTool, error) {
	dashTool, err := utils.InferTool("dashScope", "使用DashScope进行图像理解",
		func(ctx context.Context, input DashScopeRequest) (output DashScopeResponse, err error) {
			content, err := tool.Call(ctx, input.URL, input.Text)
			if err != nil {
				return DashScopeResponse{}, fmt.Errorf("failed to read url: %w", err)
			}
			return DashScopeResponse{Content: content}, nil
		})
	if err != nil {
		return nil, fmt.Errorf("failed to infer tool: %w", err)
	}
	return dashTool, nil
}

// Call 执行工具调用
func (t *DashScopeVLTool) Call(ctx context.Context, url string, text string) (string, error) {
	// 解析输入参数
	var userInput string
	var imagePath string

	userInput = text
	imagePath = url

	if userInput == "" {
		return "", fmt.Errorf("missing required parameter: text")
	}
	if imagePath == "" {
		return "", fmt.Errorf("missing required parameter: image")
	}

	sysContent := qwen.VLContentList{
		{
			Text: "你是一名专业的图像理解专家，你需要根据用户的输入，对图像进行专业的解读.",
		},
	}

	userContent := qwen.VLContentList{
		{
			Text: userInput,
		},
		{
			Image: "file://" + imagePath,
		},
	}

	vlInput := dashscopego.VLInput{
		Messages: []dashscopego.VLMessage{
			{Role: qwen.RoleSystem, Content: &sysContent},
			{Role: qwen.RoleUser, Content: &userContent},
		},
	}

	// 执行调用
	req := &dashscopego.VLRequest{
		Input: vlInput,
		// 如果不使用流式输出，可以不设置StreamingFn
	}

	resp, err := t.client.CreateVLCompletion(ctx, req)
	if err != nil {
		return "", fmt.Errorf("dashscope VL call failed: %w", err)
	}

	// 处理响应
	if len(resp.Output.Choices) == 0 || resp.Output.Choices[0].Message.Content == nil {
		return "", fmt.Errorf("empty response from dashscope")
	}

	result := resp.Output.Choices[0].Message.Content.ToString()

	return result, nil
}
