package main

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"example.com/go-gateway-practice/internal/redisstore"
)

func newTestRefreshManager(
	t *testing.T,
) (*RefreshTokenManager, context.Context) {

	t.Helper()

	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6387"
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)

	client, err := redisstore.New(ctx, redisstore.Config{
		Addr: addr,
	})
	if err != nil {
		cancel()
		t.Fatalf("connect Redis failed: %v", err)
	}

	manager, err := NewRefreshTokenManager(
		client,
		time.Minute,
	)
	if err != nil {
		_ = client.Close()
		cancel()
		t.Fatalf("create manager failed: %v", err)
	}

	t.Cleanup(func() {
		_ = client.Close()
		cancel()
	})

	return manager, ctx
}

func TestRefreshTokenManagerIssueAndRotate(t *testing.T) {
	manager, ctx := newTestRefreshManager(t)

	// 1. 签发 Token
	issued, err := manager.Issue(ctx, "user-001")
	if err != nil {
		t.Fatalf("issue token failed: %v", err)
	}

	if issued.Token == "" {
		t.Fatal("issued token cannot be empty")
	}

	// 2. 使用旧 Token 换取新 Token
	rotated, err := manager.Rotate(ctx, issued.Token)
	if err != nil {
		t.Fatalf("rotate token failed: %v", err)
	}

	if rotated.UserID != "user-001" {
		t.Fatalf("unexpected user ID: %s", rotated.UserID)
	}

	if rotated.Token == issued.Token {
		t.Fatal("new token must differ from old token")
	}

	// 3. 再次使用旧 Token，应该识别为重放
	_, err = manager.Rotate(ctx, issued.Token)

	if !errors.Is(err, errRefreshTokenReuse) {
		t.Fatalf("expected token reuse error, got: %v", err)
	}

	// 4. 新 Token 仍然可以正常使用
	_, err = manager.Rotate(ctx, rotated.Token)
	if err != nil {
		t.Fatalf("rotate new token failed: %v", err)
	}
}

func TestRefreshTokenManagerRevoke(t *testing.T) {
	manager, ctx := newTestRefreshManager(t)

	issued, err := manager.Issue(ctx, "user-002")
	if err != nil {
		t.Fatalf("issue token failed: %v", err)
	}

	// 撤销 Token
	revoked, err := manager.Revoke(ctx, issued.Token)
	if err != nil {
		t.Fatalf("revoke token failed: %v", err)
	}

	if !revoked {
		t.Fatal("expected token to be revoked")
	}

	// 被撤销的 Token 不能再使用
	_, err = manager.Rotate(ctx, issued.Token)

	if !errors.Is(err, errInvalidRefreshToken) {
		t.Fatalf("expected invalid token error, got: %v", err)
	}
}

func TestRefreshTokenManagerConcurrentRotate(t *testing.T) {
	manager, ctx := newTestRefreshManager(t)

	issued, err := manager.Issue(ctx, "user-003")
	if err != nil {
		t.Fatalf("issue token failed: %v", err)
	}

	const requestCount = 2

	start := make(chan struct{})
	results := make(chan error, requestCount)

	var wg sync.WaitGroup

	for i := 0; i < requestCount; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			<-start

			_, err := manager.Rotate(ctx, issued.Token)
			results <- err
		}()
	}

	// 同时释放两个请求
	close(start)

	wg.Wait()
	close(results)

	successCount := 0
	reuseCount := 0

	for err := range results {
		switch {
		case err == nil:
			successCount++

		case errors.Is(err, errRefreshTokenReuse):
			reuseCount++

		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if successCount != 1 {
		t.Fatalf("expected exactly one success, got %d", successCount)
	}

	if reuseCount != 1 {
		t.Fatalf("expected exactly one reuse detection, got %d", reuseCount)
	}
}
