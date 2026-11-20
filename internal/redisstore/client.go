package redisstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrKeyNotFound 表示 Redis 中不存在指定 Key
var ErrKeyNotFound = errors.New("redis key not found")

// Config 保存 Redis 连接配置
type Config struct {
	Addr         string
	Password     string
	DB           int
	PoolSize     int
	DialTimeout  time.Duration
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

// RedisStore 封装 Redis 客户端
type RedisStore struct {
	client *redis.Client
}

// New 创建 Redis 客户端并验证连接
func New(ctx context.Context, cfg Config) (*RedisStore, error) {
	if ctx == nil {
		return nil, errors.New("context cannot be nil")
	}

	if strings.TrimSpace(cfg.Addr) == "" {
		return nil, errors.New("redis address is required")
	}

	if cfg.DB < 0 {
		return nil, errors.New("redis DB cannot be negative")
	}

	// 设置默认配置
	if cfg.PoolSize <= 0 {
		cfg.PoolSize = 10
	}

	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 3 * time.Second
	}

	if cfg.ReadTimeout <= 0 {
		cfg.ReadTimeout = 2 * time.Second
	}

	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = 2 * time.Second
	}

	// 创建 Redis 客户端
	client := redis.NewClient(&redis.Options{
		Addr:         cfg.Addr,
		Password:     cfg.Password,
		DB:           cfg.DB,
		PoolSize:     cfg.PoolSize,
		DialTimeout:  cfg.DialTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	})

	// 验证 Redis 连接
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	return &RedisStore{
		client: client,
	}, nil
}

// Ping 检查 Redis 是否正常
func (s *RedisStore) Ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}

// Set 设置 Key-Value
func (s *RedisStore) Set(
	ctx context.Context,
	key string,
	value any,
	expiration time.Duration,
) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("redis key cannot be empty")
	}

	if expiration < 0 {
		return errors.New("expiration cannot be negative")
	}

	return s.client.Set(ctx, key, value, expiration).Err()
}

// Get 获取指定 Key 的值
func (s *RedisStore) Get(
	ctx context.Context,
	key string,
) (string, error) {
	if strings.TrimSpace(key) == "" {
		return "", errors.New("redis key cannot be empty")
	}

	value, err := s.client.Get(ctx, key).Result()

	if errors.Is(err, redis.Nil) {
		return "", fmt.Errorf("%q: %w", key, ErrKeyNotFound)
	}

	if err != nil {
		return "", fmt.Errorf("redis GET %q: %w", key, err)
	}

	return value, nil
}

// Delete 删除指定 Key
func (s *RedisStore) Delete(
	ctx context.Context,
	key string,
) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("redis key cannot be empty")
	}

	return s.client.Del(ctx, key).Err()
}

// TTL 获取 Key 剩余有效时间
func (s *RedisStore) TTL(
	ctx context.Context,
	key string,
) (time.Duration, error) {
	if strings.TrimSpace(key) == "" {
		return 0, errors.New("redis key cannot be empty")
	}

	ttl, err := s.client.TTL(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("redis TTL %q: %w", key, err)
	}

	// Redis TTL 返回 -2 表示 Key 不存在

	if ttl == time.Duration(-2) {
		return 0, fmt.Errorf("%q: %w", key, ErrKeyNotFound)
	}

	return ttl, nil
}

// Close 关闭 Redis 客户端及其连接池
func (s *RedisStore) Close() error {
	return s.client.Close()
}

// Eval 执行 Redis Lua 脚本
func (s *RedisStore) Eval(
	ctx context.Context,
	script string,
	keys []string,
	args ...any,
) (any, error) {

	if s == nil || s.client == nil {
		return nil, errors.New("redis client is not initialized")
	}

	if strings.TrimSpace(script) == "" {
		return nil, errors.New("redis script cannot be empty")
	}

	result, err := s.client.Eval(
		ctx,
		script,
		keys,
		args...,
	).Result()

	if err != nil {
		return nil, fmt.Errorf("redis EVAL: %w", err)
	}

	return result, nil
}
