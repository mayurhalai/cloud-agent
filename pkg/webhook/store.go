package webhook

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

type TokenStore interface {
	StoreToken(ctx context.Context, taskID string, token string) error
	VerifyToken(ctx context.Context, taskID string, token string) (bool, error)
	DeleteToken(ctx context.Context, taskID string) error
}

type InMemoryTokenStore struct {
	mu     sync.RWMutex
	tokens map[string]string
}

func NewInMemoryTokenStore() *InMemoryTokenStore {
	return &InMemoryTokenStore{
		tokens: make(map[string]string),
	}
}

func (s *InMemoryTokenStore) StoreToken(ctx context.Context, taskID string, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[taskID] = token
	return nil
}

func (s *InMemoryTokenStore) VerifyToken(ctx context.Context, taskID string, token string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	stored, exists := s.tokens[taskID]
	if !exists {
		return false, nil
	}
	return stored == token, nil
}

func (s *InMemoryTokenStore) DeleteToken(ctx context.Context, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, taskID)
	return nil
}

func (s *InMemoryTokenStore) GetToken(taskID string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	token, ok := s.tokens[taskID]
	return token, ok
}

type RedisTokenStore struct {
	client *redis.Client
}

func NewRedisTokenStore(redisURL string) (*RedisTokenStore, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse Redis URL %q: %w", redisURL, err)
	}
	client := redis.NewClient(opts)
	return &RedisTokenStore{
		client: client,
	}, nil
}

func (s *RedisTokenStore) redisKey(taskID string) string {
	return fmt.Sprintf("callback_token:%s", taskID)
}

func (s *RedisTokenStore) StoreToken(ctx context.Context, taskID string, token string) error {
	err := s.client.Set(ctx, s.redisKey(taskID), token, 24*time.Hour).Err()
	if err != nil {
		return fmt.Errorf("failed to store token in Redis: %w", err)
	}
	return nil
}

func (s *RedisTokenStore) VerifyToken(ctx context.Context, taskID string, token string) (bool, error) {
	stored, err := s.client.Get(ctx, s.redisKey(taskID)).Result()
	if err == redis.Nil {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to get token from Redis: %w", err)
	}
	return stored == token, nil
}

func (s *RedisTokenStore) DeleteToken(ctx context.Context, taskID string) error {
	err := s.client.Del(ctx, s.redisKey(taskID)).Err()
	if err != nil {
		return fmt.Errorf("failed to delete token from Redis: %w", err)
	}
	return nil
}
