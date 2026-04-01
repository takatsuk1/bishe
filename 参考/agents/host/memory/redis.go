//go:build reference
// +build reference

package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/redis/go-redis/v9"
	tgoredis "trpc.group/trpc-go/trpc-database/goredis"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

// Redis key patterns
const (
	// Sorted set of conversation IDs for a user, with timestamp as score
	keyUserConversations = "{user:%s}:conversations"
	// Sorted set of message IDs for a conversation, with timestamp as score
	keyConversationMsgs = "{user:%s:conv:%s}:msgs"
	// Message content by user ID, conversation ID and message ID
	keyMessageContent = "{user:%s:conv:%s}:msg:%s"
	// User state hashmap
	keyUserState = "{user:%s}:state"
)

const maxWindowSize = 10

// Lua scripts for atomic operations
var (
	// Script to delete conversation and its message IDs
	// KEYS[1]: user conversations key
	// KEYS[2]: conversation messages key
	// ARGV[1]: conversation ID
	scriptDeleteConv = redis.NewScript(`
		local exists = redis.call('ZRANK', KEYS[1], ARGV[1])
		if exists ~= false then
			-- Delete message set and conversation
			redis.call('DEL', KEYS[2])
			redis.call('ZREM', KEYS[1], ARGV[1])
		end
		return 1
	`)

	// Script to update or append message
	// KEYS[1]: conversation messages key
	// KEYS[2]: message content key
	// KEYS[3]: user conversations key
	// ARGV[1]: message ID
	// ARGV[2]: message content
	// ARGV[3]: max window size
	// ARGV[4]: conversation ID
	// ARGV[5]: timestamp
	scriptUpdateOrAppendMsg = redis.NewScript(`
		local msgID = ARGV[1]
		local msgContent = ARGV[2]
		local maxSize = tonumber(ARGV[3])
		local timestamp = tonumber(ARGV[5])
		
		-- Store or update message content
		redis.call('SET', KEYS[2], msgContent)
		
		-- Add or update message in sorted set with timestamp as score
		redis.call('ZADD', KEYS[1], 'NX', timestamp, msgID)
		
		local oldIDs = {}
		if maxSize > 0 then
			-- Get the number of messages to remove
			local count = redis.call('ZCARD', KEYS[1])
			if count > maxSize then
				-- Get message IDs to remove (oldest messages)
				oldIDs = redis.call('ZRANGE', KEYS[1], 0, count - maxSize - 1)
				-- Remove old messages from sorted set
				for _, oldID in ipairs(oldIDs) do
					redis.call('ZREM', KEYS[1], oldID)
				end
			end
		end

		return oldIDs
	`)
)

type redisMemoryFactory struct {
	redisCli      redis.UniversalClient
	maxWindowSize int
}

// Get implements MemoryFactory.
func (r *redisMemoryFactory) Get(ctx context.Context, userID string) (Memory, error) {
	return &redisMemory{
		userID:        userID,
		redisCli:      r.redisCli,
		maxWindowSize: r.maxWindowSize,
	}, nil
}

func NewRedisMemoryFactory(redisName string) (Factory, error) {
	cli, err := tgoredis.New(redisName)
	if err != nil {
		return nil, fmt.Errorf("failed to create redis client: %w", err)
	}
	return &redisMemoryFactory{
		redisCli:      cli,
		maxWindowSize: maxWindowSize,
	}, nil
}

// redisMemory implements Memory interface
type redisMemory struct {
	userID        string
	redisCli      redis.UniversalClient
	maxWindowSize int
}

func (impl *redisMemory) GetUserID(ctx context.Context) string {
	return impl.userID
}

// GetConversation returns a conversation by ID
func (impl *redisMemory) GetConversation(ctx context.Context, id string) (Conversation, error) {
	key := fmt.Sprintf(keyUserConversations, impl.userID)

	// Check if conversation exists
	exists, err := impl.redisCli.ZScore(ctx, key, id).Result()
	if err != nil || exists == 0 {
		return nil, fmt.Errorf("conversation not found: %s", id)
	}

	return &redisConversation{
		mem:  impl,
		user: impl.userID,
		id:   id,
	}, nil
}

// NewConversation creates a new conversation with a generated ID
func (impl *redisMemory) NewConversation(ctx context.Context) (Conversation, error) {
	// Generate a unique ID for the new conversation
	id := fmt.Sprintf("%d", time.Now().UnixNano())
	key := fmt.Sprintf(keyUserConversations, impl.userID)
	// Check if conversation already exists
	_, err := impl.redisCli.ZScore(ctx, key, id).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("failed to check conversation existence: %w", err)
	}

	if err == nil {
		return nil, fmt.Errorf("conversation already exists: %s", id)
	}

	// Set initial timestamp for new conversation
	// After this, timestamp will only be updated during Append operations
	now := float64(time.Now().UnixNano())
	if err := impl.redisCli.ZAdd(ctx, key, redis.Z{Score: now, Member: id}).Err(); err != nil {
		return nil, fmt.Errorf("failed to create conversation: %w", err)
	}

	return &redisConversation{
		mem:  impl,
		user: impl.userID,
		id:   id,
	}, nil
}

// GetCurrentConversation returns the most recent conversation
func (impl *redisMemory) GetCurrentConversation(ctx context.Context) (Conversation, error) {
	// Get the most recent conversation from sorted set
	key := fmt.Sprintf(keyUserConversations, impl.userID)
	// Get the conversation with the highest score (most recent)
	convs, err := impl.redisCli.ZRevRangeWithScores(ctx, key, 0, 0).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get conversations: %w", err)
	}
	if len(convs) == 0 {
		// If no conversations, create a new one
		conv, err := impl.NewConversation(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to create new conversation: %w", err)
		}
		return conv, nil
	}
	// Get the first conversation ID
	convID, ok := convs[0].Member.(string)
	if !ok {
		return nil, fmt.Errorf("invalid conversation ID type")
	}
	return &redisConversation{
		mem:  impl,
		user: impl.userID,
		id:   convID,
	}, nil
}

// GetState returns the user's state
func (impl *redisMemory) GetState(ctx context.Context) (map[string]string, error) {
	stateKey := fmt.Sprintf(keyUserState, impl.userID)

	// Get all fields and values from the hash
	state, err := impl.redisCli.HGetAll(ctx, stateKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("failed to get state: %w", err)
	}

	return state, nil
}

// SetState sets a value in the user's state
func (impl *redisMemory) SetState(ctx context.Context, fields ...string) error {
	stateKey := fmt.Sprintf(keyUserState, impl.userID)
	var redisFields []interface{}
	for _, v := range fields {
		redisFields = append(redisFields, v)
	}
	if _, err := impl.redisCli.HSet(ctx, stateKey, redisFields...).Result(); err != nil {
		return fmt.Errorf("failed to set state: %w", err)
	}

	return nil
}

// ListConversations returns all conversations for the user
func (impl *redisMemory) ListConversations(ctx context.Context) ([]string, error) {
	key := fmt.Sprintf(keyUserConversations, impl.userID)

	// Get all members from sorted set, ordered by score (most recent first)
	conversations, err := impl.redisCli.ZRevRange(ctx, key, 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to list conversations: %w", err)
	}

	return conversations, nil
}

// DeleteConversation deletes a conversation and its messages
func (impl *redisMemory) DeleteConversation(ctx context.Context, id string) error {
	conversationsKey := fmt.Sprintf(keyUserConversations, impl.userID)
	messagesKey := fmt.Sprintf(keyConversationMsgs, impl.userID, id)

	// Get all message IDs before deletion
	msgIDs, err := impl.redisCli.ZRange(ctx, messagesKey, 0, -1).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("failed to get message IDs: %w", err)
	}

	// Delete conversation index and message IDs
	if err := scriptDeleteConv.Run(ctx, impl.redisCli, []string{conversationsKey, messagesKey}, id).Err(); err != nil {
		return fmt.Errorf("failed to delete conversation: %w", err)
	}

	// Asynchronously delete message contents if there are any
	if len(msgIDs) > 0 {
		_ = trpc.Go(ctx, time.Second*10, func(ctx context.Context) {
			pipe := impl.redisCli.Pipeline()
			for _, msgID := range msgIDs {
				pipe.Del(ctx, fmt.Sprintf(keyMessageContent, impl.userID, id, msgID))
			}
			if _, err := pipe.Exec(ctx); err != nil {
				log.ErrorContextf(ctx, "failed to delete messages: %v", err)
				return
			}
		})
	}

	return nil
}

// redisConversation implements Conversation interface
type redisConversation struct {
	mem  *redisMemory
	user string
	id   string
}

// GetID returns the conversation ID
func (c *redisConversation) GetID(ctx context.Context) string {
	return c.id
}

// Append adds a message to the conversation or updates an existing one
func (c *redisConversation) Append(ctx context.Context, msgID string, msg *schema.Message) error {
	// Get current timestamp
	timestamp := time.Now().UnixNano()

	// Get last message first
	lastMessage, err := c.GetMessage(ctx, msgID)
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("failed to get last messages: %w", err)
	}
	var concatMessage *schema.Message
	if lastMessage == nil {
		concatMessage = msg
	} else {
		concatMessage, err = schema.ConcatMessages([]*schema.Message{lastMessage, msg})
		if err != nil {
			return fmt.Errorf("failed to concat messages: %w", err)
		}
	}

	if concatMessage.Role == "" {
		return fmt.Errorf("invalid concat message, empty Role")
	}

	// Convert message to JSON
	msgJSON, err := json.Marshal(concatMessage)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}
	// Use Lua script to update or append message
	msgListKey := fmt.Sprintf(keyConversationMsgs, c.user, c.id)
	msgContentKey := fmt.Sprintf(keyMessageContent, c.user, c.id, msgID)

	// Run script and get old message IDs
	oldIDs, err := scriptUpdateOrAppendMsg.Run(ctx, c.mem.redisCli,
		[]string{msgListKey, msgContentKey},
		msgID, string(msgJSON), c.mem.maxWindowSize, c.id, timestamp).StringSlice()
	if err != nil {
		return fmt.Errorf("failed to update or append message: %w", err)
	}

	pipe := c.mem.redisCli.Pipeline()
	// Delete old message contents
	if len(oldIDs) > 0 {
		for _, oldID := range oldIDs {
			oldContentKey := fmt.Sprintf(keyMessageContent, c.user, c.id, oldID)
			pipe.Del(ctx, oldContentKey)
		}
	}

	// Update conversation timestamp in sorted set
	convListKey := fmt.Sprintf(keyUserConversations, c.user)
	pipe.ZAdd(ctx, convListKey, redis.Z{
		Score:  float64(timestamp),
		Member: c.id,
	})
	if _, err = pipe.Exec(ctx); err != nil {
		return fmt.Errorf("failed to delete old messages: %w", err)
	}

	return nil
}

// GetMessages returns all messages in the conversation
func (c *redisConversation) GetMessages(ctx context.Context) ([]*schema.Message, error) {
	msgListKey := fmt.Sprintf(keyConversationMsgs, c.user, c.id)

	// Get all message IDs in order
	msgIDs, err := c.mem.redisCli.ZRange(ctx, msgListKey, 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get message IDs: %w", err)
	}

	// Get message contents using pipeline for better performance
	messages := make([]*schema.Message, 0, len(msgIDs))
	if len(msgIDs) > 0 {
		pipe := c.mem.redisCli.Pipeline()
		cmds := make([]*redis.StringCmd, len(msgIDs))

		// Queue all get commands in the pipeline
		for i, msgID := range msgIDs {
			msgContentKey := fmt.Sprintf(keyMessageContent, c.user, c.id, msgID)
			cmds[i] = pipe.Get(ctx, msgContentKey)
		}

		// Execute pipeline
		_, err := pipe.Exec(ctx)
		if err != nil && !errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("failed to execute pipeline: %w", err)
		}

		// Process results
		for _, cmd := range cmds {
			msgJSON, err := cmd.Result()
			if err != nil {
				if errors.Is(err, redis.Nil) {
					continue // Skip if message content not found
				}
				return nil, fmt.Errorf("failed to get message content: %w", err)
			}

			var msg = &schema.Message{}
			if err := json.Unmarshal([]byte(msgJSON), msg); err != nil {
				return nil, fmt.Errorf("failed to unmarshal message: %w", err)
			}

			messages = append(messages, msg)
		}
	}

	return messages, nil
}

// GetMessage returns single messages in the conversation
func (c *redisConversation) GetMessage(ctx context.Context, msgID string) (*schema.Message, error) {
	msgContentKey := fmt.Sprintf(keyMessageContent, c.user, c.id, msgID)
	msgJSON, err := c.mem.redisCli.Get(ctx, msgContentKey).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get message content: %w", err)
	}
	var msg = &schema.Message{}
	if err := json.Unmarshal([]byte(msgJSON), msg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal message: %w", err)
	}
	return msg, nil
}

func (c *redisConversation) GetMemory(ctx context.Context) Memory {
	return c.mem
}
