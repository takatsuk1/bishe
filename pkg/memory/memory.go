package memory

import "context"

const (
	StateKeyCurrentTaskID         = "sk:current_task_id"
	StateKeyCurrentAgentMessageID = "sk:current_agent_message_id"
	StateKeyCurrentUserEventID    = "sk:current_user_event_id"
)

type Message struct {
	Role         string        `json:"role"`
	Content      string        `json:"content"`
	ResponseMeta *ResponseMeta `json:"response_meta,omitempty"`
}

type ResponseMeta struct {
	FinishReason string `json:"finish_reason,omitempty"`
}

type Factory interface {
	Get(ctx context.Context, userID string) (Memory, error)
}

type Memory interface {
	GetUserID(ctx context.Context) string
	GetState(ctx context.Context) (map[string]string, error)
	SetState(ctx context.Context, fields ...string) error
	GetConversation(ctx context.Context, id string) (Conversation, error)
	GetCurrentConversation(ctx context.Context) (Conversation, error)
	NewConversation(ctx context.Context) (Conversation, error)
	ListConversations(ctx context.Context) ([]string, error)
	DeleteConversation(ctx context.Context, id string) error
}

type Conversation interface {
	GetMemory(ctx context.Context) Memory
	GetID(ctx context.Context) string
	Append(ctx context.Context, msgID string, msg *Message) error
	GetMessages(ctx context.Context) ([]*Message, error)
	GetMessage(ctx context.Context, msgID string) (*Message, error)
}
