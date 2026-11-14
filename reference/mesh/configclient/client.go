// Package configclient 从控制面拉取配置快照并通过 SSE 保持同步。
//
// 启动时先 GET /v1/config 拿到完整快照，然后连接 /v1/watch。连接断开
// 后会先重新拉一次快照，再重连 watch，避免断线期间的变化丢失。
package configclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"example.com/go-gateway-reference/internal/meshapi"
)

// Client 是控制面配置客户端。
type Client struct {
	baseURL string
	logger  *slog.Logger
	http    *http.Client

	snapshot atomic.Pointer[meshapi.Snapshot]
	version  atomic.Uint64

	ready     chan struct{}
	readyOnce sync.Once

	mu       sync.Mutex
	onUpdate func(*meshapi.Snapshot)
}

// New 创建配置客户端。
func New(baseURL string, logger *slog.Logger) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		logger:  logger,
		http: &http.Client{
			Transport: &http.Transport{
				MaxIdleConns:       10,
				IdleConnTimeout:    90 * time.Second,
				DisableCompression: true,
				ForceAttemptHTTP2:  false,
			},
		},
		ready: make(chan struct{}),
	}
}

// SetOnUpdate 注册快照更新回调。
func (c *Client) SetOnUpdate(fn func(*meshapi.Snapshot)) {
	c.mu.Lock()
	c.onUpdate = fn
	c.mu.Unlock()
}

// Start 拉取初始快照并启动后台 watch 循环。
func (c *Client) Start(ctx context.Context) error {
	if err := c.fetchWithRetry(ctx); err != nil {
		return err
	}

	go c.watchLoop(ctx)
	return nil
}

// WaitReady 等待第一份快照。
func (c *Client) WaitReady(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ready:
		return nil
	}
}

// Snapshot 返回当前快照；可能为 nil。
func (c *Client) Snapshot() *meshapi.Snapshot {
	return c.snapshot.Load()
}

// Version 返回当前快照版本。
func (c *Client) Version() uint64 {
	return c.version.Load()
}

func (c *Client) fetchWithRetry(ctx context.Context) error {
	backoff := 500 * time.Millisecond

	for {
		err := c.fetch(ctx)
		if err == nil {
			return nil
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		c.logger.Warn("initial config fetch failed; retrying", "error", err, "backoff", backoff)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}

		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

func (c *Client) fetch(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/config", nil)
	if err != nil {
		return err
	}

	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("config endpoint returned %s", response.Status)
	}

	var snapshot meshapi.Snapshot
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		return fmt.Errorf("decode config snapshot: %w", err)
	}

	c.apply(&snapshot)
	return nil
}

func (c *Client) watchLoop(ctx context.Context) {
	backoff := time.Second

	for ctx.Err() == nil {
		err := c.watchOnce(ctx)

		if ctx.Err() != nil {
			return
		}

		c.logger.Warn("config watch disconnected", "error", err)

		// 断线期间可能错过通知，先补一次完整快照。
		if fetchErr := c.fetch(ctx); fetchErr != nil {
			c.logger.Warn("config refresh after disconnect failed", "error", fetchErr)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

func (c *Client) watchOnce(ctx context.Context) error {
	url := fmt.Sprintf("%s/v1/watch?version=%d", c.baseURL, c.version.Load())

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "text/event-stream")

	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("watch endpoint returned %s", response.Status)
	}

	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)

	var data strings.Builder

	for scanner.Scan() {
		line := scanner.Text()

		switch {
		case line == "":
			if data.Len() == 0 {
				continue
			}

			var snapshot meshapi.Snapshot
			if err := json.Unmarshal([]byte(data.String()), &snapshot); err != nil {
				c.logger.Warn("invalid snapshot event", "error", err)
			} else {
				c.apply(&snapshot)
			}

			data.Reset()
		case strings.HasPrefix(line, "data:"):
			payload := strings.TrimPrefix(line, "data:")
			payload = strings.TrimPrefix(payload, " ")

			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(payload)
		default:
			// event: / id: / :comment 都不需要处理。
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	return errors.New("watch stream closed")
}

func (c *Client) apply(snapshot *meshapi.Snapshot) {
	if snapshot == nil {
		return
	}

	c.snapshot.Store(snapshot)
	c.version.Store(snapshot.Version)

	c.readyOnce.Do(func() {
		close(c.ready)
	})

	c.logger.Info("mesh snapshot updated",
		"version", snapshot.Version,
		"services", len(snapshot.Services),
		"virtual_services", len(snapshot.VirtualServices),
		"destination_rules", len(snapshot.DestinationRules),
	)

	c.mu.Lock()
	callback := c.onUpdate
	c.mu.Unlock()

	if callback != nil {
		callback(snapshot)
	}
}
