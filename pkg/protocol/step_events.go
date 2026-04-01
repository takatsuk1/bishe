package protocol

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// StepEvent 是一个轻量级的、面向 UI 的进度事件，
// 旨在作为令牌嵌入到现有的聊天/任务文本流中：[] (step://<base64url(json)>).
//
// 前端解码此 JSON 并渲染步骤时间线。
// 保持此模式稳定且向后兼容。
//
// 注意：这不是用于核心执行的；它用于可观察性。
//
// 令牌格式（精确）：[](step://<payload>)
// 其中 <payload> 是不带填充的 base64url 编码的 JSON。
//
// 示例：
// [](step://eyJ0cyI6IjIwMjYtMDMtMjJUMDg6MDA6MDBaIiwiYWdlbnQiOiJob3N0IiwicGhhc2UiOiJkZWxlZ2F0ZSIsIm5hbWUiOiJob3N0LmRlbGVnYXRlIiwic3RhdGUiOiJzdGFydCIsIm1lc3NhZ2VaSCI6IuWkp+WutumInuW4j+W6lOWIhuWPkeaJi+eUqCJ9)

// StepState 表示步骤的状态
type StepState string

// 步骤状态常量定义
const (
	// StepStateStart 表示步骤开始
	StepStateStart StepState = "start"
	// StepStateEnd 表示步骤结束
	StepStateEnd   StepState = "end"
	// StepStateInfo 表示信息状态
	StepStateInfo  StepState = "info"
	// StepStateError 表示错误状态
	StepStateError StepState = "error"
)

// StepEvent 表示一个步骤事件
type StepEvent struct {
	// TS 时间戳，格式为 RFC3339Nano
	TS        string    `json:"ts"`
	// Agent 代理名称
	Agent     string    `json:"agent"`
	// Phase 阶段
	Phase     string    `json:"phase"`
	// Name 步骤名称
	Name      string    `json:"name"`
	// State 步骤状态
	State     StepState `json:"state"`
	// MessageZh 中文消息
	MessageZh string    `json:"messageZh"`

	// Round 轮次（可选）
	Round   int    `json:"round,omitempty"`
	// Keyword 关键词（可选）
	Keyword string `json:"keyword,omitempty"`
	// Query 查询内容（可选）
	Query   string `json:"query,omitempty"`
	// URL URL（可选）
	URL     string `json:"url,omitempty"`
	// Model 模型名称（可选）
	Model   string `json:"model,omitempty"`
}

// NewStepEvent 创建一个新的步骤事件
// agent: 代理名称
// phase: 阶段
// name: 步骤名称
// state: 步骤状态
// messageZh: 中文消息
// 返回创建的步骤事件
func NewStepEvent(agent, phase, name string, state StepState, messageZh string) StepEvent {
	return StepEvent{
		TS:        time.Now().UTC().Format(time.RFC3339Nano),
		Agent:     strings.TrimSpace(agent),
		Phase:     strings.TrimSpace(phase),
		Name:      strings.TrimSpace(name),
		State:     state,
		MessageZh: strings.TrimSpace(messageZh),
	}
}

// EncodeStepPayload 编码步骤事件的负载
// ev: 步骤事件
// 返回编码后的负载和可能的错误
func EncodeStepPayload(ev StepEvent) (string, error) {
	// 序列化步骤事件为 JSON
	b, err := json.Marshal(ev)
	if err != nil {
		return "", err
	}
	// 使用 base64url 编码 JSON 数据（无填充）
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// EncodeStepToken 编码步骤事件的令牌
// ev: 步骤事件
// 返回编码后的令牌和可能的错误
func EncodeStepToken(ev StepEvent) (string, error) {
	// 编码步骤事件的负载
	payload, err := EncodeStepPayload(ev)
	if err != nil {
		return "", err
	}
	// 按照格式构建令牌
	return fmt.Sprintf("[](step://%s)", payload), nil
}