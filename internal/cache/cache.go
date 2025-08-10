package cache

import (
	"context"
	"crypto/md5"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

type CacheService struct {
	client *redis.Client
	logger *zap.SugaredLogger
	ttl    time.Duration
}

func NewCacheService(logger *zap.SugaredLogger) (*CacheService, error) {
	host := getEnv("REDIS_HOST", "localhost")
	port := getEnv("REDIS_PORT", "6379")
	password := getEnv("REDIS_PASSWORD", "")
	ttlSeconds, _ := strconv.Atoi(getEnv("CACHE_TTL", "3600"))

	client := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%s", host, port),
		Password: password,
		DB:       0, // default DB
	})

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	logger.Infow("Connected to Redis cache", "host", host, "port", port, "ttl", ttlSeconds)

	return &CacheService{
		client: client,
		logger: logger,
		ttl:    time.Duration(ttlSeconds) * time.Second,
	}, nil
}

func (c *CacheService) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := c.client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil // Cache miss
	}
	if err != nil {
		c.logger.Errorw("Cache get error", "key", key, "error", err)
		return nil, err
	}
	
	c.logger.Debugw("Cache hit", "key", key, "size", len(data))
	return data, nil
}

func (c *CacheService) Set(ctx context.Context, key string, value []byte) error {
	err := c.client.Set(ctx, key, value, c.ttl).Err()
	if err != nil {
		c.logger.Errorw("Cache set error", "key", key, "error", err)
		return err
	}
	
	c.logger.Debugw("Cache set", "key", key, "size", len(value), "ttl", c.ttl)
	return nil
}

func (c *CacheService) GenerateKey(prefix, uploadID, path string) string {
	// Create a hash-based key to avoid key length issues
	hash := md5.Sum([]byte(fmt.Sprintf("%s:%s:%s", prefix, uploadID, path)))
	return fmt.Sprintf("%s:%x", prefix, hash)
}

func (c *CacheService) Close() error {
	return c.client.Close()
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
