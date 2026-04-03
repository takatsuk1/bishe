package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	DraftWorkflowTTL = 5 * time.Minute

	keyDraftWorkflow     = "draft:workflow:%s"
	keyDraftWorkflowList = "draft:workflows"
)

var (
	redisClient     redis.UniversalClient
	redisClientErr  error
	redisClientOnce bool
)

func InitRedis(redisURL string) error {
	if redisClientOnce {
		return redisClientErr
	}
	redisClientOnce = true

	if redisURL == "" {
		redisURL = "redis://127.0.0.1:6379"
	}

	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		redisClientErr = fmt.Errorf("invalid redis URL: %w", err)
		return redisClientErr
	}

	redisClient = redis.NewClient(opt)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := redisClient.Ping(ctx).Err(); err != nil {
		redisClientErr = fmt.Errorf("failed to connect to Redis at %s: %w", redisURL, err)
		return redisClientErr
	}

	return nil
}

func GetRedisClient() (redis.UniversalClient, error) {
	if redisClient == nil {
		return nil, fmt.Errorf("redis client not initialized, call InitRedis first")
	}
	return redisClient, nil
}

type DraftWorkflow struct {
	WorkflowID  string `json:"workflowId"`
	UserID      string `json:"userId"`
	Name        string `json:"name"`
	Description string `json:"description"`
	StartNodeID string `json:"startNodeId"`
	Nodes       string `json:"nodes"`
	Edges       string `json:"edges"`
	CreatedAt   int64  `json:"createdAt"`
}

func SaveDraftWorkflow(ctx context.Context, workflowID string, data *DraftWorkflow) error {
	cli, err := GetRedisClient()
	if err != nil {
		return err
	}

	data.CreatedAt = time.Now().Unix()
	data.WorkflowID = workflowID
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal draft workflow: %w", err)
	}

	key := fmt.Sprintf(keyDraftWorkflow, workflowID)
	if err := cli.Set(ctx, key, jsonData, DraftWorkflowTTL).Err(); err != nil {
		return fmt.Errorf("save draft workflow: %w", err)
	}

	listKey := keyDraftWorkflowList
	cli.ZAdd(ctx, listKey, redis.Z{Score: float64(data.CreatedAt), Member: workflowID})

	return nil
}

func GetDraftWorkflow(ctx context.Context, workflowID string) (*DraftWorkflow, error) {
	cli, err := GetRedisClient()
	if err != nil {
		return nil, err
	}

	key := fmt.Sprintf(keyDraftWorkflow, workflowID)
	data, err := cli.Get(ctx, key).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, fmt.Errorf("get draft workflow: %w", err)
	}

	var draft DraftWorkflow
	if err := json.Unmarshal([]byte(data), &draft); err != nil {
		return nil, fmt.Errorf("unmarshal draft workflow: %w", err)
	}

	return &draft, nil
}

func GetAllDraftWorkflows(ctx context.Context) ([]DraftWorkflow, error) {
	cli, err := GetRedisClient()
	if err != nil {
		return nil, err
	}

	listKey := keyDraftWorkflowList
	ids, err := cli.ZRevRange(ctx, listKey, 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("get draft workflow list: %w", err)
	}

	var drafts []DraftWorkflow
	for _, id := range ids {
		draft, err := GetDraftWorkflow(ctx, id)
		if err != nil {
			continue
		}
		if draft != nil {
			drafts = append(drafts, *draft)
		} else {
			cli.ZRem(ctx, listKey, id)
		}
	}

	return drafts, nil
}

func DeleteDraftWorkflow(ctx context.Context, workflowID string) error {
	cli, err := GetRedisClient()
	if err != nil {
		return err
	}

	key := fmt.Sprintf(keyDraftWorkflow, workflowID)
	cli.Del(ctx, key)

	listKey := keyDraftWorkflowList
	cli.ZRem(ctx, listKey, workflowID)

	return nil
}

func GetDraftTTL(ctx context.Context, workflowID string) (time.Duration, error) {
	cli, err := GetRedisClient()
	if err != nil {
		return 0, err
	}

	key := fmt.Sprintf(keyDraftWorkflow, workflowID)
	ttl, err := cli.TTL(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("get draft ttl: %w", err)
	}

	return ttl, nil
}

func RefreshDraftTTL(ctx context.Context, workflowID string) error {
	cli, err := GetRedisClient()
	if err != nil {
		return err
	}

	key := fmt.Sprintf(keyDraftWorkflow, workflowID)
	return cli.Expire(ctx, key, DraftWorkflowTTL).Err()
}
