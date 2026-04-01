//go:build reference
// +build reference

package memory

import (
	"context"
	"fmt"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/client"
)

func setupTestRedis(t *testing.T) *miniredis.Miniredis {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(func() {
		mr.Close()
	})
	return mr
}

func setupRedisMemory(t *testing.T) (*miniredis.Miniredis, Memory) {
	ctx := context.Background()
	mr := setupTestRedis(t)
	config := trpc.GlobalConfig()
	config.Client.Service = append(config.Client.Service, &client.BackendConfig{
		ServiceName: "test",
		Target:      "ip://" + mr.Addr(),
	})
	err := trpc.RepairConfig(config)
	require.NoError(t, err)
	err = trpc.Setup(config)
	require.NoError(t, err)

	// Create memory factory with small window size for testing
	factory, err := NewRedisMemoryFactory("test")
	require.NoError(t, err)

	// Get memory instance
	mem, err := factory.Get(ctx, "test_user")
	require.NoError(t, err)

	return mr, mem
}

func TestRedisConversationManagement(t *testing.T) {
	ctx := context.Background()
	_, mem := setupRedisMemory(t)

	// Create new conversation
	conv1, err := mem.NewConversation(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, conv1.GetID(ctx))

	// Get conversation by ID
	conv2, err := mem.GetConversation(ctx, conv1.GetID(ctx))
	require.NoError(t, err)
	assert.Equal(t, conv1.GetID(ctx), conv2.GetID(ctx))

	// Get current conversation (should be the same as conv1)
	currentConv, err := mem.GetCurrentConversation(ctx)
	require.NoError(t, err)
	assert.Equal(t, conv1.GetID(ctx), currentConv.GetID(ctx))

	// Create another conversation
	conv3, err := mem.NewConversation(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, conv3.GetID(ctx))

	// Get current conversation (should be conv3 now)
	currentConv, err = mem.GetCurrentConversation(ctx)
	require.NoError(t, err)
	assert.Equal(t, conv3.GetID(ctx), currentConv.GetID(ctx))

	// List conversations
	convs, err := mem.ListConversations(ctx)
	require.NoError(t, err)
	assert.Len(t, convs, 2)
	assert.Contains(t, convs, conv1.GetID(ctx))
	assert.Contains(t, convs, conv3.GetID(ctx))

	// Delete conversation
	err = mem.DeleteConversation(ctx, conv1.GetID(ctx))
	require.NoError(t, err)

	// Verify conversation is deleted
	convs, err = mem.ListConversations(ctx)
	require.NoError(t, err)
	assert.Len(t, convs, 1)
	assert.Contains(t, convs, conv3.GetID(ctx))
}

func TestRedisMessageManagement(t *testing.T) {
	ctx := context.Background()
	_, mem := setupRedisMemory(t)

	// Create new conversation
	conv, err := mem.NewConversation(ctx)
	require.NoError(t, err)

	// Create test messages
	msg1 := &schema.Message{
		Role:    "user",
		Content: "Hello",
	}
	msg2 := &schema.Message{
		Role:    "assistant",
		Content: "Hi there!",
	}
	msg3 := &schema.Message{
		Role:    "user",
		Content: "How are you?",
	}
	msg4 := &schema.Message{
		Role:    "assistant",
		Content: "I'm good, thanks!",
	}

	// Append messages
	err = conv.Append(ctx, uuid.New().String(), msg1)
	require.NoError(t, err)
	err = conv.Append(ctx, uuid.New().String(), msg2)
	require.NoError(t, err)
	err = conv.Append(ctx, uuid.New().String(), msg3)
	require.NoError(t, err)

	// Get messages
	messages, err := conv.GetMessages(ctx)
	require.NoError(t, err)
	assert.Len(t, messages, 3)
	assert.Equal(t, "Hello", messages[0].Content)
	assert.Equal(t, "Hi there!", messages[1].Content)
	assert.Equal(t, "How are you?", messages[2].Content)

	// Append one more message (should trigger window size limit)
	err = conv.Append(ctx, uuid.New().String(), msg4)
	require.NoError(t, err)

	// Verify oldest message is removed
	messages, err = conv.GetMessages(ctx)
	require.NoError(t, err)
	assert.Len(t, messages, 3)
	assert.Equal(t, "Hi there!", messages[0].Content)
	assert.Equal(t, "How are you?", messages[1].Content)
	assert.Equal(t, "I'm good, thanks!", messages[2].Content)

	// Update existing message
	msg2.Content = "Hi there! How can I help you?"
	err = conv.Append(ctx, uuid.New().String(), msg2)
	require.NoError(t, err)

	// Verify message is updated
	messages, err = conv.GetMessages(ctx)
	require.NoError(t, err)
	assert.Len(t, messages, 3)
	assert.Equal(t, "Hi there! How can I help you?", messages[2].Content)
}

func TestRedisStateManagement(t *testing.T) {
	ctx := context.Background()
	_, mem := setupRedisMemory(t)

	// Set state
	err := mem.SetState(ctx, "key1", "value1")
	require.NoError(t, err)

	// Get state
	state, err := mem.GetState(ctx)
	require.NoError(t, err)
	assert.Equal(t, "value1", state["key1"])

	// Update state
	err = mem.SetState(ctx, "key1", "value2")
	require.NoError(t, err)

	// Verify state is updated
	state, err = mem.GetState(ctx)
	require.NoError(t, err)
	assert.Equal(t, "value2", state["key1"])
}

func TestRedisConcurrentOperations(t *testing.T) {
	ctx := context.Background()
	_, mem := setupRedisMemory(t)

	// Create conversation
	conv, err := mem.NewConversation(ctx)
	require.NoError(t, err)

	// Create messages
	msgs := make([]*schema.Message, 10)
	for i := 0; i < 10; i++ {
		msgs[i] = &schema.Message{
			Role:    "user",
			Content: string(rune('A' + i)),
		}
	}

	// Concurrently append messages
	done := make(chan error, 10)
	for _, msg := range msgs {
		go func(m *schema.Message) {
			done <- conv.Append(ctx, uuid.New().String(), m)
		}(msg)
	}

	// Wait for all operations to complete
	for i := 0; i < 10; i++ {
		err := <-done
		require.NoError(t, err)
	}

	// Verify messages are properly ordered
	messages, err := conv.GetMessages(ctx)
	require.NoError(t, err)
	assert.Len(t, messages, 3) // Due to window size limit
}

func TestRedisStateVerification(t *testing.T) {
	ctx := context.Background()
	mr, mem := setupRedisMemory(t)

	// Create a conversation and add some messages
	conv, err := mem.NewConversation(ctx)
	require.NoError(t, err)

	msg := &schema.Message{
		Role:    "user",
		Content: "test message",
	}
	msgID := uuid.New().String()
	err = conv.Append(ctx, msgID, msg)
	require.NoError(t, err)

	// Verify Redis keys
	convKey := fmt.Sprintf(keyUserConversations, "test_user")
	msgKey := fmt.Sprintf(keyConversationMsgs, "test_user", conv.GetID(ctx))

	// Check conversation exists in sorted set
	score, err := mr.ZScore(convKey, conv.GetID(ctx))
	require.NoError(t, err)
	assert.NotZero(t, score)

	// Check message exists in sorted set
	score, err = mr.ZScore(msgKey, msgID)
	require.NoError(t, err)
	assert.NotZero(t, score)

	// Check message content exists
	contentKey := fmt.Sprintf(keyMessageContent, "test_user", conv.GetID(ctx), msgID)
	content, err := mr.Get(contentKey)
	require.NoError(t, err)
	assert.NotEmpty(t, content)
}
