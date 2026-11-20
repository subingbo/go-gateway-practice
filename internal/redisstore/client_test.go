package redisstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestRedisStoreIntegration(t *testing.T) {
	// 1. 获取 Redis 地址
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6387"
	}

	// 2. 创建带超时时间的 Context
	ctx, cancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancel()

	// 3. 创建 Redis 客户端
	store, err := New(ctx, Config{
		Addr: addr,
	})
	if err != nil {
		t.Fatalf("create Redis client failed: %v", err)
	}

	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close Redis client failed: %v", err)
		}
	})

	// 4. 测试 PING
	if err := store.Ping(ctx); err != nil {
		t.Fatalf("Redis PING failed: %v", err)
	}

	// 5. 创建测试 Key
	key := fmt.Sprintf(
		"go-gateway:test:%d",
		time.Now().UnixNano(),
	)

	// 6. 测试 SET
	if err := store.Set(ctx, key, "hello-go", 30*time.Second); err != nil {
		t.Fatalf("Redis SET failed: %v", err)
	}

	// 7. 测试 GET
	value, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Redis GET failed: %v", err)
	}

	if value != "hello-go" {
		t.Fatalf("unexpected value: got %q", value)
	}

	// 8. 测试 TTL
	ttl, err := store.TTL(ctx, key)
	if err != nil {
		t.Fatalf("Redis TTL failed: %v", err)
	}

	if ttl <= 0 {
		t.Fatalf("expected positive TTL, got %v", ttl)
	}

	// 9. 测试 DELETE
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Redis DELETE failed: %v", err)
	}

	// 10. 测试不存在的 Key
	_, err = store.Get(ctx, key)
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound, got %v", err)
	}

	// 11. 测试不存在的 Key 对应的 TTL
	_, err = store.TTL(ctx, key)
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound, got %v", err)
	}
}

func TestRedisStoreEval(t *testing.T) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6387"
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancel()

	store, err := New(ctx, Config{
		Addr: addr,
	})
	if err != nil {
		t.Fatalf("create Redis client failed: %v", err)
	}

	t.Cleanup(func() {
		_ = store.Close()
	})

	key := fmt.Sprintf(
		"go-gateway:lua-test:%d",
		time.Now().UnixNano(),
	)

	script := `
        return redis.call(
            "SET",
            KEYS[1],
            ARGV[1],
            "EX",
            ARGV[2]
        )
    `

	result, err := store.Eval(
		ctx,
		script,
		[]string{key},
		"hello-lua",
		30,
	)
	if err != nil {
		t.Fatalf("Redis EVAL failed: %v", err)
	}

	if result != "OK" {
		t.Fatalf("unexpected EVAL result: %v", result)
	}

	value, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Redis GET failed: %v", err)
	}

	if value != "hello-lua" {
		t.Fatalf("unexpected value: %s", value)
	}
}
