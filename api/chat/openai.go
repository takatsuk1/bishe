package chat

import (
	"ai/api/chat/api"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

var finishReasonToolCalls = "tool_calls"

type Error struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   any     `json:"param"`
	Code    *string `json:"code"`
}

type ErrorResponse struct {
	Error Error `json:"error"`
}

type Message struct {
	Role             string     `json:"role"`
	Content          any        `json:"content"`
	ReasoningContent string     `json:"reasoning_content"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
}

type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason *string `json:"finish_reason"`
}

type ChunkChoice struct {
	Index        int     `json:"index"`
	Delta        Message `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

type CompleteChunkChoice struct {
	Text         string  `json:"text"`
	Index        int     `json:"index"`
	FinishReason *string `json:"finish_reason"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type ResponseFormat struct {
	Type       string      `json:"type"`
	JsonSchema *JsonSchema `json:"json_schema,omitempty"`
}

type JsonSchema struct {
	Schema json.RawMessage `json:"schema"`
}

type EmbedRequest struct {
	Input any    `json:"input"`
	Model string `json:"model"`
}

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type ChatCompletionRequest struct {
	Model            string          `json:"model"`
	ConversationID   string          `json:"conversation_id,omitempty"`
	Messages         []Message       `json:"messages"`
	Stream           bool            `json:"stream"`
	StreamOptions    *StreamOptions  `json:"stream_options"`
	MaxTokens        *int            `json:"max_tokens"`
	Seed             *int            `json:"seed"`
	Stop             any             `json:"stop"`
	Temperature      *float64        `json:"temperature"`
	FrequencyPenalty *float64        `json:"frequency_penalty"`
	PresencePenalty  *float64        `json:"presence_penalty"`
	TopP             *float64        `json:"top_p"`
	ResponseFormat   *ResponseFormat `json:"response_format"`
	Tools            []api.Tool      `json:"tools"`
}

type ChatCompletion struct {
	Id                string   `json:"id"`
	Object            string   `json:"object"`
	Created           int64    `json:"created"`
	Model             string   `json:"model"`
	SystemFingerprint string   `json:"system_fingerprint"`
	Choices           []Choice `json:"choices"`
	Usage             Usage    `json:"usage,omitempty"`
}

type ChatCompletionChunk struct {
	Id                string        `json:"id"`
	Object            string        `json:"object"`
	Created           int64         `json:"created"`
	Model             string        `json:"model"`
	SystemFingerprint string        `json:"system_fingerprint"`
	Choices           []ChunkChoice `json:"choices"`
	Usage             *Usage        `json:"usage,omitempty"`
}

// TODO (https://github.com/ollama/ollama/issues/5259): support []string, []int and [][]int
type CompletionRequest struct {
	Model            string         `json:"model"`
	Prompt           string         `json:"prompt"`
	FrequencyPenalty float32        `json:"frequency_penalty"`
	MaxTokens        *int           `json:"max_tokens"`
	PresencePenalty  float32        `json:"presence_penalty"`
	Seed             *int           `json:"seed"`
	Stop             any            `json:"stop"`
	Stream           bool           `json:"stream"`
	StreamOptions    *StreamOptions `json:"stream_options"`
	Temperature      *float32       `json:"temperature"`
	TopP             float32        `json:"top_p"`
	Suffix           string         `json:"suffix"`
}

type Completion struct {
	Id                string                `json:"id"`
	Object            string                `json:"object"`
	Created           int64                 `json:"created"`
	Model             string                `json:"model"`
	SystemFingerprint string                `json:"system_fingerprint"`
	Choices           []CompleteChunkChoice `json:"choices"`
	Usage             Usage                 `json:"usage,omitempty"`
}

type CompletionChunk struct {
	Id                string                `json:"id"`
	Object            string                `json:"object"`
	Created           int64                 `json:"created"`
	Choices           []CompleteChunkChoice `json:"choices"`
	Model             string                `json:"model"`
	SystemFingerprint string                `json:"system_fingerprint"`
	Usage             *Usage                `json:"usage,omitempty"`
}

type ToolCall struct {
	ID       string `json:"id"`
	Index    int    `json:"index"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type Model struct {
	Id      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type Embedding struct {
	Object    string    `json:"object"`
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

type ListCompletion struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

type EmbeddingList struct {
	Object string         `json:"object"`
	Data   []Embedding    `json:"data"`
	Model  string         `json:"model"`
	Usage  EmbeddingUsage `json:"usage,omitempty"`
}

type EmbeddingUsage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// NewError 根据 HTTP 状态码构造 OpenAI 风格错误响应。
func NewError(code int, message string) ErrorResponse {
	var etype string
	switch code {
	case http.StatusBadRequest:
		etype = "invalid_request_error"
	case http.StatusNotFound:
		etype = "not_found_error"
	default:
		etype = "api_error"
	}

	return ErrorResponse{Error{Type: etype, Message: message}}
}

// toUsage 把内部 chat 响应里的 token 统计映射成 OpenAI usage 结构。
func toUsage(r api.ChatResponse) Usage {
	return Usage{
		PromptTokens:     r.PromptEvalCount,
		CompletionTokens: r.EvalCount,
		TotalTokens:      r.PromptEvalCount + r.EvalCount,
	}
}

// toolCallId 生成一个假的 OpenAI 风格 tool_call ID。
func toolCallId() string {
	const letterBytes = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	for i := range b {
		// 从字母数字集合里随机挑字符，拼成短 ID。
		b[i] = letterBytes[rand.Intn(len(letterBytes))]
	}
	return "call_" + strings.ToLower(string(b))
}

// toToolCalls 把内部工具调用结构转换成 OpenAI 响应里的 tool_calls。
func toToolCalls(tc []api.ToolCall) []ToolCall {
	toolCalls := make([]ToolCall, len(tc))
	for i, tc := range tc {
		// 每个内部 tool call 都补齐 OpenAI 需要的 id/type/name/index 字段。
		toolCalls[i].ID = toolCallId()
		toolCalls[i].Type = "function"
		toolCalls[i].Function.Name = tc.Function.Name
		toolCalls[i].Index = tc.Function.Index

		// OpenAI 协议要求 arguments 是 JSON 字符串，这里先把参数对象序列化。
		args, err := json.Marshal(tc.Function.Arguments)
		if err != nil {
			slog.Error("could not marshall function arguments to json", "error", err)
			continue
		}

		toolCalls[i].Function.Arguments = string(args)
	}
	return toolCalls
}

// toChatCompletion 把内部一次完整 chat 响应包装成 OpenAI 非流式响应。
func toChatCompletion(id string, r api.ChatResponse) ChatCompletion {
	toolCalls := toToolCalls(r.Message.ToolCalls)
	return ChatCompletion{
		Id:                id,
		Object:            "chat.completion",
		Created:           r.CreatedAt.Unix(),
		Model:             r.Model,
		SystemFingerprint: "fp_ollama",
		Choices: []Choice{{
			Index: 0,
			Message: Message{
				Role:             r.Message.Role,
				Content:          r.Message.Content,
				ReasoningContent: r.Message.ReasoningContent,
				ToolCalls:        toolCalls,
			},
			FinishReason: func(reason string) *string {
				// 如果本次响应里带了工具调用，就强制把 finish_reason 改成 tool_calls。
				if len(toolCalls) > 0 {
					reason = "tool_calls"
				}
				if len(reason) > 0 {
					return &reason
				}
				return nil
			}(r.DoneReason),
		}},
		Usage: toUsage(r),
	}
}

// toChunk 把内部 chat 响应包装成 OpenAI 流式 chunk。
func toChunk(id string, r api.ChatResponse, toolCallSent bool) ChatCompletionChunk {
	toolCalls := toToolCalls(r.Message.ToolCalls)
	return ChatCompletionChunk{
		Id:                id,
		Object:            "chat.completion.chunk",
		Created:           time.Now().Unix(),
		Model:             r.Model,
		SystemFingerprint: "fp_ollama",
		Choices: []ChunkChoice{{
			Index: 0,
			Delta: Message{
				Role:             "assistant",
				Content:          r.Message.Content,
				ReasoningContent: r.Message.ReasoningContent,
				ToolCalls:        toolCalls,
			},
			FinishReason: func(reason string) *string {
				// 流式场景下如果工具调用已经发过，就用固定的 tool_calls 结束原因。
				if len(reason) > 0 {
					if toolCallSent {
						return &finishReasonToolCalls
					}
					return &reason
				}
				return nil
			}(r.DoneReason),
		}},
	}
}

// toUsageGenerate 把内部 generate 响应的统计信息映射成 OpenAI usage。
func toUsageGenerate(r api.GenerateResponse) Usage {
	return Usage{
		PromptTokens:     r.PromptEvalCount,
		CompletionTokens: r.EvalCount,
		TotalTokens:      r.PromptEvalCount + r.EvalCount,
	}
}

// toCompletion 把内部 generate 响应包装成 OpenAI text completion。
func toCompletion(id string, r api.GenerateResponse) Completion {
	return Completion{
		Id:                id,
		Object:            "text_completion",
		Created:           r.CreatedAt.Unix(),
		Model:             r.Model,
		SystemFingerprint: "fp_ollama",
		Choices: []CompleteChunkChoice{{
			Text:  r.Response,
			Index: 0,
			FinishReason: func(reason string) *string {
				if len(reason) > 0 {
					return &reason
				}
				return nil
			}(r.DoneReason),
		}},
		Usage: toUsageGenerate(r),
	}
}

// toCompleteChunk 把内部 generate 响应包装成流式 text completion chunk。
func toCompleteChunk(id string, r api.GenerateResponse) CompletionChunk {
	return CompletionChunk{
		Id:                id,
		Object:            "text_completion",
		Created:           time.Now().Unix(),
		Model:             r.Model,
		SystemFingerprint: "fp_ollama",
		Choices: []CompleteChunkChoice{{
			Text:  r.Response,
			Index: 0,
			FinishReason: func(reason string) *string {
				if len(reason) > 0 {
					return &reason
				}
				return nil
			}(r.DoneReason),
		}},
	}
}

// toListCompletion 把内部模型列表转换成 OpenAI models/list 风格结构。
func toListCompletion(r api.ListResponse) ListCompletion {
	var data []Model
	for _, m := range r.Models {
		data = append(data, Model{
			Id:      m.Name,
			Object:  "model",
			Created: m.ModifiedAt.Unix(),
		})
	}

	return ListCompletion{
		Object: "list",
		Data:   data,
	}
}

// toEmbeddingList 把内部 embedding 响应转换成 OpenAI embeddings/list 风格结构。
func toEmbeddingList(model string, r api.EmbedResponse) EmbeddingList {
	if r.Embeddings != nil {
		var data []Embedding
		for i, e := range r.Embeddings {
			// 每一条 embedding 都要带上 index，便于客户端对应原始输入。
			data = append(data, Embedding{
				Object:    "embedding",
				Embedding: e,
				Index:     i,
			})
		}

		return EmbeddingList{
			Object: "list",
			Data:   data,
			Model:  model,
			Usage: EmbeddingUsage{
				PromptTokens: r.PromptEvalCount,
				TotalTokens:  r.PromptEvalCount,
			},
		}
	}

	return EmbeddingList{}
}

// toModel 把内部模型详情压缩成 OpenAI 兼容的单个 model 对象。
func toModel(r api.ShowResponse, m string) Model {
	return Model{
		Id:      m,
		Object:  "model",
		Created: r.ModifiedAt.Unix(),
	}
}

// fromChatRequest 把 OpenAI 风格 chat/completions 请求翻译成内部 chat 请求。
func fromChatRequest(r ChatCompletionRequest) (*api.ChatRequest, error) {
	var messages []api.Message
	for _, msg := range r.Messages {
		switch content := msg.Content.(type) {
		case string:
			// 最简单的文本消息可以直接映射。
			messages = append(messages, api.Message{Role: msg.Role, Content: content})
		case []any:
			// 多模态消息会拆成 text/image_url 等分片，逐个转换。
			for _, c := range content {
				data, ok := c.(map[string]any)
				if !ok {
					return nil, errors.New("invalid message format")
				}
				switch data["type"] {
				case "text":
					// 纯文本分片直接提取 text 字段。
					text, ok := data["text"].(string)
					if !ok {
						return nil, errors.New("invalid message format")
					}
					messages = append(messages, api.Message{Role: msg.Role, Content: text})
				case "image_url":
					// 图片分片只支持 data:image/...;base64 这种内联格式。
					var url string
					if urlMap, ok := data["image_url"].(map[string]any); ok {
						if url, ok = urlMap["url"].(string); !ok {
							return nil, errors.New("invalid message format")
						}
					} else {
						if url, ok = data["image_url"].(string); !ok {
							return nil, errors.New("invalid message format")
						}
					}

					types := []string{"jpeg", "jpg", "png"}
					valid := false
					for _, t := range types {
						// 校验并剥掉 data URL 前缀，留下纯 base64 数据。
						prefix := "data:image/" + t + ";base64,"
						if strings.HasPrefix(url, prefix) {
							url = strings.TrimPrefix(url, prefix)
							valid = true
							break
						}
					}

					if !valid {
						return nil, errors.New("invalid image input")
					}

					// 把 base64 图片解码成内部使用的原始字节数组。
					img, err := base64.StdEncoding.DecodeString(url)
					if err != nil {
						return nil, errors.New("invalid message format")
					}

					messages = append(messages, api.Message{Role: msg.Role, Images: []api.ImageData{img}})
				default:
					return nil, errors.New("invalid message format")
				}
			}
		default:
			// 非字符串内容只在 tool_calls 场景下接受，其它类型一律报错。
			if msg.ToolCalls == nil {
				return nil, fmt.Errorf("invalid message content type: %T", content)
			}

			toolCalls := make([]api.ToolCall, len(msg.ToolCalls))
			for i, tc := range msg.ToolCalls {
				// OpenAI 请求里的 arguments 是字符串，这里再反解成对象。
				toolCalls[i].Function.Name = tc.Function.Name
				err := json.Unmarshal([]byte(tc.Function.Arguments), &toolCalls[i].Function.Arguments)
				if err != nil {
					return nil, errors.New("invalid tool call arguments")
				}
			}
			messages = append(messages, api.Message{Role: msg.Role, ToolCalls: toolCalls})
		}
	}

	// 把 OpenAI 参数名翻译成内部 options 字段名。
	options := make(map[string]any)

	switch stop := r.Stop.(type) {
	case string:
		options["stop"] = []string{stop}
	case []any:
		var stops []string
		for _, s := range stop {
			if str, ok := s.(string); ok {
				stops = append(stops, str)
			}
		}
		options["stop"] = stops
	}

	if r.MaxTokens != nil {
		options["num_predict"] = *r.MaxTokens
	}

	if r.Temperature != nil {
		options["temperature"] = *r.Temperature
	} else {
		options["temperature"] = 1.0
	}

	if r.Seed != nil {
		options["seed"] = *r.Seed
	}

	if r.FrequencyPenalty != nil {
		options["frequency_penalty"] = *r.FrequencyPenalty
	}

	if r.PresencePenalty != nil {
		options["presence_penalty"] = *r.PresencePenalty
	}

	if r.TopP != nil {
		options["top_p"] = *r.TopP
	} else {
		options["top_p"] = 1.0
	}

	var format json.RawMessage
	if r.ResponseFormat != nil {
		// 兼容 json_object 和 json_schema 两种响应格式表达方式。
		switch strings.ToLower(strings.TrimSpace(r.ResponseFormat.Type)) {
		// Support the old "json_object" type for OpenAI compatibility
		case "json_object":
			format = json.RawMessage(`"json"`)
		case "json_schema":
			if r.ResponseFormat.JsonSchema != nil {
				format = r.ResponseFormat.JsonSchema.Schema
			}
		}
	}

	return &api.ChatRequest{
		Model:          r.Model,
		ConversationID: strings.TrimSpace(r.ConversationID),
		Messages:       messages,
		Format:         format,
		Options:        options,
		Stream:         &r.Stream,
		Tools:          r.Tools,
	}, nil
}

// fromCompleteRequest 把 OpenAI 风格 completions 请求翻译成内部 generate 请求。
func fromCompleteRequest(r CompletionRequest) (api.GenerateRequest, error) {
	options := make(map[string]any)

	switch stop := r.Stop.(type) {
	case string:
		options["stop"] = []string{stop}
	case []any:
		var stops []string
		for _, s := range stop {
			if str, ok := s.(string); ok {
				stops = append(stops, str)
			} else {
				return api.GenerateRequest{}, fmt.Errorf("invalid type for 'stop' field: %T", s)
			}
		}
		options["stop"] = stops
	}

	// 把外部常见参数名逐个映射成内部 generate options。
	if r.MaxTokens != nil {
		options["num_predict"] = *r.MaxTokens
	}

	if r.Temperature != nil {
		options["temperature"] = *r.Temperature
	} else {
		options["temperature"] = 1.0
	}

	if r.Seed != nil {
		options["seed"] = *r.Seed
	}

	options["frequency_penalty"] = r.FrequencyPenalty

	options["presence_penalty"] = r.PresencePenalty

	if r.TopP != 0.0 {
		options["top_p"] = r.TopP
	} else {
		options["top_p"] = 1.0
	}

	return api.GenerateRequest{
		Model:   r.Model,
		Prompt:  r.Prompt,
		Options: options,
		Stream:  &r.Stream,
		Suffix:  r.Suffix,
	}, nil
}

type BaseWriter struct {
	gin.ResponseWriter
}

type ChatWriter struct {
	stream        bool
	streamOptions *StreamOptions
	id            string
	toolCallSent  bool
	BaseWriter
}

type CompleteWriter struct {
	stream        bool
	streamOptions *StreamOptions
	id            string
	BaseWriter
}

type ListWriter struct {
	BaseWriter
}

type RetrieveWriter struct {
	BaseWriter
	model string
}

type EmbedWriter struct {
	BaseWriter
	model string
}

// writeError 把内部错误响应重新包装成 OpenAI 风格错误格式。
func (w *BaseWriter) writeError(data []byte) (int, error) {
	var serr api.StatusError
	err := json.Unmarshal(data, &serr)
	if err != nil {
		return 0, err
	}

	w.ResponseWriter.Header().Set("Content-Type", "application/json")
	err = json.NewEncoder(w.ResponseWriter).Encode(NewError(http.StatusInternalServerError, serr.Error()))
	if err != nil {
		return 0, err
	}

	return len(data), nil
}

// writeResponse 把内部 chat 响应写成 OpenAI 非流式或 SSE 流式响应。
func (w *ChatWriter) writeResponse(data []byte) (int, error) {
	var chatResponse api.ChatResponse
	err := json.Unmarshal(data, &chatResponse)
	if err != nil {
		return 0, err
	}

	// chat chunk
	if w.stream {
		// 流式场景下，把每个内部响应块包装成 SSE data: {...}。
		c := toChunk(w.id, chatResponse, w.toolCallSent)
		d, err := json.Marshal(c)
		if err != nil {
			return 0, err
		}
		if !w.toolCallSent && len(c.Choices) > 0 && len(c.Choices[0].Delta.ToolCalls) > 0 {
			// 一旦工具调用已经发出，后续的 finish_reason 就按 tool_calls 处理。
			w.toolCallSent = true
		}

		w.ResponseWriter.Header().Set("Content-Type", "text/event-stream")
		_, err = w.ResponseWriter.Write([]byte(fmt.Sprintf("data: %s\n\n", d)))
		if err != nil {
			return 0, err
		}

		if chatResponse.Done {
			// 结束块除了 [DONE]，还可以按需额外附一条 usage 汇总。
			if w.streamOptions != nil && w.streamOptions.IncludeUsage {
				u := toUsage(chatResponse)
				c.Usage = &u
				c.Choices = []ChunkChoice{}
				d, err := json.Marshal(c)
				if err != nil {
					return 0, err
				}
				_, err = w.ResponseWriter.Write([]byte(fmt.Sprintf("data: %s\n\n", d)))
				if err != nil {
					return 0, err
				}
			}
			_, err = w.ResponseWriter.Write([]byte("data: [DONE]\n\n"))
			if err != nil {
				return 0, err
			}
		}

		return len(data), nil
	}

	// chat completion
	// 非流式场景下一次性写出完整 JSON。
	w.ResponseWriter.Header().Set("Content-Type", "application/json")
	err = json.NewEncoder(w.ResponseWriter).Encode(toChatCompletion(w.id, chatResponse))
	if err != nil {
		return 0, err
	}

	return len(data), nil
}

// Write 根据状态码分流到正常响应或错误响应。
func (w *ChatWriter) Write(data []byte) (int, error) {
	code := w.ResponseWriter.Status()
	if code != http.StatusOK {
		return w.writeError(data)
	}

	return w.writeResponse(data)
}

// writeResponse 把内部 generate 响应写成 OpenAI completions 风格输出。
func (w *CompleteWriter) writeResponse(data []byte) (int, error) {
	var generateResponse api.GenerateResponse
	err := json.Unmarshal(data, &generateResponse)
	if err != nil {
		return 0, err
	}

	// completion chunk
	if w.stream {
		// 流式 completions 也走 SSE，每个 chunk 都包成 data: {...}。
		c := toCompleteChunk(w.id, generateResponse)
		if w.streamOptions != nil && w.streamOptions.IncludeUsage {
			c.Usage = &Usage{}
		}
		d, err := json.Marshal(c)
		if err != nil {
			return 0, err
		}

		w.ResponseWriter.Header().Set("Content-Type", "text/event-stream")
		_, err = w.ResponseWriter.Write([]byte(fmt.Sprintf("data: %s\n\n", d)))
		if err != nil {
			return 0, err
		}

		if generateResponse.Done {
			if w.streamOptions != nil && w.streamOptions.IncludeUsage {
				u := toUsageGenerate(generateResponse)
				c.Usage = &u
				c.Choices = []CompleteChunkChoice{}
				d, err := json.Marshal(c)
				if err != nil {
					return 0, err
				}
				_, err = w.ResponseWriter.Write([]byte(fmt.Sprintf("data: %s\n\n", d)))
				if err != nil {
					return 0, err
				}
			}
			_, err = w.ResponseWriter.Write([]byte("data: [DONE]\n\n"))
			if err != nil {
				return 0, err
			}
		}

		return len(data), nil
	}

	// completion
	w.ResponseWriter.Header().Set("Content-Type", "application/json")
	err = json.NewEncoder(w.ResponseWriter).Encode(toCompletion(w.id, generateResponse))
	if err != nil {
		return 0, err
	}

	return len(data), nil
}

// Write 根据 HTTP 状态码决定走正常响应还是错误响应。
func (w *CompleteWriter) Write(data []byte) (int, error) {
	code := w.ResponseWriter.Status()
	if code != http.StatusOK {
		return w.writeError(data)
	}

	return w.writeResponse(data)
}

// writeResponse 把内部模型列表写成 OpenAI 风格列表响应。
func (w *ListWriter) writeResponse(data []byte) (int, error) {
	var listResponse api.ListResponse
	err := json.Unmarshal(data, &listResponse)
	if err != nil {
		return 0, err
	}

	w.ResponseWriter.Header().Set("Content-Type", "application/json")
	err = json.NewEncoder(w.ResponseWriter).Encode(toListCompletion(listResponse))
	if err != nil {
		return 0, err
	}

	return len(data), nil
}

// Write 根据状态码输出模型列表或错误信息。
func (w *ListWriter) Write(data []byte) (int, error) {
	code := w.ResponseWriter.Status()
	if code != http.StatusOK {
		return w.writeError(data)
	}

	return w.writeResponse(data)
}

// writeResponse 把内部模型详情写成 OpenAI 单模型响应。
func (w *RetrieveWriter) writeResponse(data []byte) (int, error) {
	var showResponse api.ShowResponse
	err := json.Unmarshal(data, &showResponse)
	if err != nil {
		return 0, err
	}

	// retrieve completion
	w.ResponseWriter.Header().Set("Content-Type", "application/json")
	err = json.NewEncoder(w.ResponseWriter).Encode(toModel(showResponse, w.model))
	if err != nil {
		return 0, err
	}

	return len(data), nil
}

// Write 根据状态码输出模型详情或错误信息。
func (w *RetrieveWriter) Write(data []byte) (int, error) {
	code := w.ResponseWriter.Status()
	if code != http.StatusOK {
		return w.writeError(data)
	}

	return w.writeResponse(data)
}

// writeResponse 把内部 embedding 结果写成 OpenAI embeddings 响应。
func (w *EmbedWriter) writeResponse(data []byte) (int, error) {
	var embedResponse api.EmbedResponse
	err := json.Unmarshal(data, &embedResponse)
	if err != nil {
		return 0, err
	}

	w.ResponseWriter.Header().Set("Content-Type", "application/json")
	err = json.NewEncoder(w.ResponseWriter).Encode(toEmbeddingList(w.model, embedResponse))
	if err != nil {
		return 0, err
	}

	return len(data), nil
}

// Write 根据状态码输出 embedding 结果或错误信息。
func (w *EmbedWriter) Write(data []byte) (int, error) {
	code := w.ResponseWriter.Status()
	if code != http.StatusOK {
		return w.writeError(data)
	}

	return w.writeResponse(data)
}

// ChatMiddleware 是当前实际挂在 /v1/chat/completions 上的兼容层中间件。
// 它负责把 OpenAI 请求转换成内部格式，并把响应 writer 替换成兼容 writer。
func ChatMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 先按 OpenAI 兼容结构读取原始请求体。
		var req ChatCompletionRequest
		err := c.ShouldBindJSON(&req)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, NewError(http.StatusBadRequest, err.Error()))
			return
		}

		if len(req.Messages) == 0 {
			c.AbortWithStatusJSON(http.StatusBadRequest,
				NewError(http.StatusBadRequest, "[] is too short - 'messages'"))
			return
		}

		var b bytes.Buffer

		// 把兼容请求翻译成内部 api.ChatRequest。
		chatReq, err := fromChatRequest(req)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, NewError(http.StatusBadRequest, err.Error()))
			return
		}

		if err := json.NewEncoder(&b).Encode(chatReq); err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, NewError(http.StatusInternalServerError, err.Error()))
			return
		}

		// 用转换后的请求体替换原始 body，让后面的 chatHandler 按内部协议继续处理。
		c.Request.Body = io.NopCloser(&b)

		// 再把 ResponseWriter 包起来，把 chatHandler 的内部响应翻译回 OpenAI 格式。
		w := &ChatWriter{
			BaseWriter:    BaseWriter{ResponseWriter: c.Writer},
			stream:        req.Stream,
			id:            fmt.Sprintf("chatcmpl-%d", rand.Intn(999)),
			streamOptions: req.StreamOptions,
		}

		c.Writer = w

		c.Next()
	}
}
