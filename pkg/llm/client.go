// package llm 包含大语言模型客户端相关功能，用于与 OpenAI 兼容的 API 进行交互
package llm

import (
	"bytes"         // 字节缓冲区
	"context"       // 上下文管理
	"encoding/json" // JSON 编解码
	"errors"
	"fmt"      // 格式化输出
	"io"       // 输入输出
	"net/http" // HTTP 客户端
	"net/url"  // URL 解析
	"path"     // 路径处理
	"strings"  // 字符串处理
	"time"     // 时间处理
)

// Message 定义了聊天消息的结构
type Message struct {
	Role    string `json:"role"`    // 消息角色（system、user、assistant）
	Content string `json:"content"` // 消息内容
}

// Client 定义了 LLM 客户端的结构
type Client struct {
	BaseURL string       // API 基础 URL
	APIKey  string       // API 密钥
	HTTP    *http.Client // HTTP 客户端
}

// NewClient 创建一个新的 LLM 客户端实例
func NewClient(baseURL, apiKey string) *Client {
	// 清理基础 URL
	baseURL = strings.TrimSpace(baseURL)
	return &Client{
		BaseURL: baseURL,
		APIKey:  strings.TrimSpace(apiKey),
		HTTP:    &http.Client{Timeout: 180 * time.Second}, // 设置 180 秒超时，降低大模型首包超时概率
	}
}

// chatCompletionRequest 定义了聊天完成请求的结构
type chatCompletionRequest struct {
	Model       string    `json:"model"`                 // 模型名称
	Messages    []Message `json:"messages"`              // 消息列表
	Stream      bool      `json:"stream"`                // 是否流式返回
	MaxTokens   *int      `json:"max_tokens,omitempty"`  // 最大 token 数
	Temperature *float64  `json:"temperature,omitempty"` // 温度参数
}

// chatCompletionResponse 定义了聊天完成响应的结构
type chatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content any `json:"content"` // 内容（可能是字符串或其他类型）
		} `json:"message"`
	} `json:"choices"`
}

// embeddingsRequest 定义了嵌入请求的结构
type embeddingsRequest struct {
	Model string   `json:"model"` // 模型名称
	Input []string `json:"input"` // 输入文本列表
}

// embeddingsResponse 定义了嵌入响应的结构
type embeddingsResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"` // 嵌入向量
		Index     int       `json:"index"`     // 索引
	} `json:"data"`
}

// ChatCompletion 调用聊天完成 API
func (c *Client) ChatCompletion(ctx context.Context, model string, messages []Message, maxTokens *int, temperature *float64) (string, error) {
	// 检查基础 URL 是否为空
	if strings.TrimSpace(c.BaseURL) == "" {
		return "", fmt.Errorf("llm baseURL is empty")
	}
	// 构建端点 URL
	endpoint, err := joinURL(c.BaseURL, "/v1/chat/completions")
	if err != nil {
		return "", err
	}

	// 构建请求体
	reqBody := chatCompletionRequest{
		Model:       model,
		Messages:    messages,
		Stream:      false, // 非流式
		MaxTokens:   maxTokens,
		Temperature: temperature,
	}
	// 序列化请求体
	bts, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	// 创建 HTTP 请求
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bts))
	if err != nil {
		return "", err
	}
	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	// 发送请求
	start := time.Now()
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || (ctx != nil && ctx.Err() == context.DeadlineExceeded) {
			return "", fmt.Errorf("llm timeout model=%s url=%s elapsed=%s: %w", strings.TrimSpace(model), endpoint, time.Since(start).Round(time.Millisecond), err)
		}
		return "", err
	}
	defer resp.Body.Close()

	// 读取响应体
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	// 检查响应状态码
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("llm http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	// 解析响应
	var parsed chatCompletionResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("llm invalid json: %w", err)
	}
	// 检查是否有选择项
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("llm empty choices")
	}

	// 获取内容
	content := parsed.Choices[0].Message.Content
	// 处理不同类型的内容
	switch v := content.(type) {
	case string:
		return strings.TrimSpace(v), nil
	default:
		// 尽力将内容转换为字符串
		b, _ := json.Marshal(v)
		return strings.TrimSpace(string(b)), nil
	}
}

// Embeddings 调用 OpenAI 兼容的嵌入端点，为每个输入字符串返回一个向量
// 向量以 float32 类型返回，以匹配 Pinecone QueryByVectorValues 的期望
func (c *Client) Embeddings(ctx context.Context, model string, input []string) ([][]float32, error) {
	// 检查基础 URL 是否为空
	if strings.TrimSpace(c.BaseURL) == "" {
		return nil, fmt.Errorf("llm baseURL is empty")
	}
	// 清理模型名称
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, fmt.Errorf("embedding model is empty")
	}
	// 检查输入是否为空
	if len(input) == 0 {
		return nil, fmt.Errorf("embedding input is empty")
	}
	// 构建端点 URL
	endpoint, err := joinURL(c.BaseURL, "/v1/embeddings")
	if err != nil {
		return nil, err
	}

	// 构建请求体
	reqBody := embeddingsRequest{Model: model, Input: input}
	// 序列化请求体
	bts, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	// 创建 HTTP 请求
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bts))
	if err != nil {
		return nil, err
	}
	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	// 发送请求
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// 读取响应体
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	// 检查响应状态码
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("llm embeddings http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	// 解析响应
	var parsed embeddingsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("llm embeddings invalid json: %w", err)
	}
	// 检查是否有数据
	if len(parsed.Data) == 0 {
		return nil, fmt.Errorf("llm embeddings empty data")
	}

	// 构建输出向量
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

// joinURL 连接基础 URL 和路径
func joinURL(base, p string) (string, error) {
	// 解析基础 URL
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return "", err
	}
	// 如果基础 URL 已经以 /v1 结尾，避免重复
	if strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/v1") && strings.HasPrefix(p, "/v1/") {
		p = strings.TrimPrefix(p, "/v1")
	}
	// 连接路径
	u.Path = path.Join(u.Path, p)
	return u.String(), nil
}
