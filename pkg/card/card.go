package card // 包名 card

import (
	"context"       // 上下文包
	"encoding/json" // JSON 编解码
	"fmt"           // 格式化输出
	"net/http"      // HTTP 客户端
	"time"          // 时间相关

	"ai/pkg/protocol" // 导入协议包
)

// Fetch 从 .well-known 端点获取 agent card
func Fetch(cardURL string) (*protocol.AgentCard, error) {
	// 创建带超时的上下文（5秒）
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel() // 函数结束时取消上下文

	// 构造 HTTP GET 请求
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cardURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err) // 创建请求失败
	}

	// 发送 HTTP 请求
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch agent card: %w", err) // 请求失败
	}
	defer resp.Body.Close() // 函数结束时关闭响应体

	// 检查响应状态码
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode) // 状态码异常
	}

	// 解码响应体为 AgentCard 结构体
	var card protocol.AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		return nil, fmt.Errorf("failed to decode agent card: %w", err) // 解码失败
	}

	return &card, nil // 返回结果
}
