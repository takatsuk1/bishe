package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"ai/pkg/storage"

	"github.com/redis/go-redis/v9"
)

const (
	keyUserConversations = "{user:%s}:conversations"
	keyConversationMsgs  = "{user:%s:conv:%s}:msgs"
	keyMessageContent    = "{user:%s:conv:%s}:msg:%s"
	keyUserState         = "{user:%s}:state"
)


const DefaultMaxWindowSize = 20

type redisMemoryFactory struct {
	maxWindowSize int
}

func (r *redisMemoryFactory) Get(ctx context.Context, userID string) (Memory, error) {
	_ = ctx
	return &redisMemory{userID: userID, maxWindowSize: r.maxWindowSize}, nil
}

func NewRedisMemoryFactory(redisURL string, maxWindowSize int) (Factory, error) {
	if maxWindowSize <= 0 {
		maxWindowSize = DefaultMaxWindowSize
	}

	if err := storage.InitRedis(redisURL); err != nil {
		return nil, err
	}

	return &redisMemoryFactory{maxWindowSize: maxWindowSize}, nil
}

type redisMemory struct {
	userID        string
	maxWindowSize int
}

func (impl *redisMemory) getRedisClient() redis.UniversalClient {
	cli, err := storage.GetRedisClient()
	if err != nil {
		return nil
	}
	return cli
}

func (impl *redisMemory) GetUserID(ctx context.Context) string {
	_ = ctx
	return impl.userID
}

func (impl *redisMemory) GetConversation(ctx context.Context, id string) (Conversation, error) {
	cli := impl.getRedisClient()
	if cli == nil {
		return nil, fmt.Errorf("redis client not available")
	}
	key := fmt.Sprintf(keyUserConversations, impl.userID)
	exists, err := cli.ZScore(ctx, key, id).Result()
	if err != nil || exists == 0 {
		return nil, fmt.Errorf("conversation not found: %s", id)
	}
	return &redisConversation{mem: impl, user: impl.userID, id: id}, nil
}

func (impl *redisMemory) NewConversation(ctx context.Context) (Conversation, error) {
	cli := impl.getRedisClient()
	if cli == nil {
		return nil, fmt.Errorf("redis client not available")
	}
	id := fmt.Sprintf("%d", time.Now().UnixNano())
	key := fmt.Sprintf(keyUserConversations, impl.userID)
	now := float64(time.Now().UnixNano())
	if err := cli.ZAdd(ctx, key, redis.Z{Score: now, Member: id}).Err(); err != nil {
		return nil, fmt.Errorf("failed to create conversation: %w", err)
	}
	return &redisConversation{mem: impl, user: impl.userID, id: id}, nil
}

func (impl *redisMemory) GetCurrentConversation(ctx context.Context) (Conversation, error) {
	cli := impl.getRedisClient()
	if cli == nil {
		return nil, fmt.Errorf("redis client not available")
	}
	key := fmt.Sprintf(keyUserConversations, impl.userID)
	convs, err := cli.ZRevRangeWithScores(ctx, key, 0, 0).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get conversations: %w", err)
	}
	if len(convs) == 0 {
		return impl.NewConversation(ctx)
	}
	convID, ok := convs[0].Member.(string)
	if !ok {
		return nil, fmt.Errorf("invalid conversation ID type")
	}
	return &redisConversation{mem: impl, user: impl.userID, id: convID}, nil
}

func (impl *redisMemory) GetState(ctx context.Context) (map[string]string, error) {
	cli := impl.getRedisClient()
	if cli == nil {
		return nil, fmt.Errorf("redis client not available")
	}
	stateKey := fmt.Sprintf(keyUserState, impl.userID)
	state, err := cli.HGetAll(ctx, stateKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("failed to get state: %w", err)
	}
	return state, nil
}

func (impl *redisMemory) SetState(ctx context.Context, fields ...string) error {
	cli := impl.getRedisClient()
	if cli == nil {
		return fmt.Errorf("redis client not available")
	}
	stateKey := fmt.Sprintf(keyUserState, impl.userID)
	vals := make([]interface{}, 0, len(fields))
	for _, v := range fields {
		vals = append(vals, v)
	}
	if _, err := cli.HSet(ctx, stateKey, vals...).Result(); err != nil {
		return fmt.Errorf("failed to set state: %w", err)
	}
	return nil
}

func (impl *redisMemory) ListConversations(ctx context.Context) ([]string, error) {
	cli := impl.getRedisClient()
	if cli == nil {
		return nil, fmt.Errorf("redis client not available")
	}
	key := fmt.Sprintf(keyUserConversations, impl.userID)
	conversations, err := cli.ZRevRange(ctx, key, 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to list conversations: %w", err)
	}
	return conversations, nil
}

func (impl *redisMemory) DeleteConversation(ctx context.Context, id string) error {
	cli := impl.getRedisClient()
	if cli == nil {
		return fmt.Errorf("redis client not available")
	}
	convKey := fmt.Sprintf(keyUserConversations, impl.userID)
	msgKey := fmt.Sprintf(keyConversationMsgs, impl.userID, id)
	msgIDs, _ := cli.ZRange(ctx, msgKey, 0, -1).Result()
	_, _ = cli.Del(ctx, msgKey).Result()
	_, _ = cli.ZRem(ctx, convKey, id).Result()
	for _, msgID := range msgIDs {
		_, _ = cli.Del(ctx, fmt.Sprintf(keyMessageContent, impl.userID, id, msgID)).Result()
	}
	return nil
}

type redisConversation struct {
	mem  *redisMemory
	user string
	id   string
}

func (c *redisConversation) GetMemory(ctx context.Context) Memory {
	_ = ctx
	return c.mem
}

func (c *redisConversation) GetID(ctx context.Context) string {
	_ = ctx
	return c.id
}

func concatMessages(a, b *Message) *Message {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	role := b.Role
	if role == "" {
		role = a.Role
	}
	return &Message{Role: role, Content: a.Content + b.Content, ResponseMeta: b.ResponseMeta}
}

func (c *redisConversation) getRedisClient() redis.UniversalClient {
	return c.mem.getRedisClient()
}

func (c *redisConversation) Append(ctx context.Context, msgID string, msg *Message) error {
	cli := c.getRedisClient()
	if cli == nil {
		return fmt.Errorf("redis client not available")
	}
	timestamp := time.Now().UnixNano()
	lastMessage, err := c.GetMessage(ctx, msgID)
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("failed to get last message: %w", err)
	}
	merged := concatMessages(lastMessage, msg)
	if merged == nil || merged.Role == "" {
		return fmt.Errorf("invalid message")
	}

	msgJSON, err := json.Marshal(merged)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	msgListKey := fmt.Sprintf(keyConversationMsgs, c.user, c.id)
	msgContentKey := fmt.Sprintf(keyMessageContent, c.user, c.id, msgID)
	pipe := cli.Pipeline()
	pipe.Set(ctx, msgContentKey, string(msgJSON), 0)
	pipe.ZAdd(ctx, msgListKey, redis.Z{Score: float64(timestamp), Member: msgID})
	pipe.ZAdd(ctx, fmt.Sprintf(keyUserConversations, c.user), redis.Z{Score: float64(timestamp), Member: c.id})
	if _, err = pipe.Exec(ctx); err != nil {
		return fmt.Errorf("failed to append message: %w", err)
	}

	msgIDs, err := cli.ZRange(ctx, msgListKey, 0, -1).Result()
	if err == nil && len(msgIDs) > c.mem.maxWindowSize {
		drop := len(msgIDs) - c.mem.maxWindowSize
		for i := 0; i < drop; i++ {
			oldID := msgIDs[i]
			_, _ = cli.ZRem(ctx, msgListKey, oldID).Result()
			_, _ = cli.Del(ctx, fmt.Sprintf(keyMessageContent, c.user, c.id, oldID)).Result()
		}
	}
	return nil
}

func (c *redisConversation) GetMessages(ctx context.Context) ([]*Message, error) {
	cli := c.getRedisClient()
	if cli == nil {
		return nil, fmt.Errorf("redis client not available")
	}
	msgListKey := fmt.Sprintf(keyConversationMsgs, c.user, c.id)
	msgIDs, err := cli.ZRange(ctx, msgListKey, 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get message IDs: %w", err)
	}
	messages := make([]*Message, 0, len(msgIDs))
	for _, msgID := range msgIDs {
		msg, err := c.GetMessage(ctx, msgID)
		if err != nil {
			continue
		}
		messages = append(messages, msg)
	}
	return messages, nil
}

func (c *redisConversation) GetMessage(ctx context.Context, msgID string) (*Message, error) {
	cli := c.getRedisClient()
	if cli == nil {
		return nil, fmt.Errorf("redis client not available")
	}
	msgContentKey := fmt.Sprintf(keyMessageContent, c.user, c.id, msgID)
	msgJSON, err := cli.Get(ctx, msgContentKey).Result()
	if err != nil {
		return nil, err
	}
	msg := &Message{}
	if err = json.Unmarshal([]byte(msgJSON), msg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal message: %w", err)
	}
	return msg, nil
}
