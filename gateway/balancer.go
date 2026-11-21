package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type UpstreamStatus struct {
	Target  string `json:"target"`
	Healthy bool   `json:"healthy"`
}

// LoadBalancer 定义负载均衡器接口
type LoadBalancer interface {
	Next() *url.URL
}

// upstream 表示一个后端实例
type upstream struct {
	target  *url.URL
	healthy atomic.Bool
	checked bool
}

// RoundRobinBalancer 实现轮询负载均衡
type RoundRobinBalancer struct {
	mu                  sync.RWMutex
	targets             []*upstream
	counter             atomic.Uint64
	healthChecksStarted atomic.Bool
	healthClient        *http.Client
	healthWG            sync.WaitGroup
}

// 编译期检查接口实现
var _ LoadBalancer = (*RoundRobinBalancer)(nil)

// NewRoundRobinBalancer 创建轮询负载均衡器
func NewRoundRobinBalancer(
	rawTargets []string,
) (*RoundRobinBalancer, error) {

	if len(rawTargets) == 0 {
		return nil, errors.New("at least one upstream target is required")
	}

	targets := make([]*upstream, 0, len(rawTargets))

	for _, raw := range rawTargets {
		raw = strings.TrimSpace(raw)

		target, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("parse target %q: %w", raw, err)
		}

		if (target.Scheme != "http" && target.Scheme != "https") ||
			target.Host == "" {
			return nil, fmt.Errorf("invalid upstream target: %s", raw)
		}

		targets = append(targets, &upstream{
			target: target,
		})
	}

	return &RoundRobinBalancer{
		targets: targets,

		// 所有健康检查共享一个 HTTP Client
		// 不跟随重定向，避免 3xx 被错误地当成健康
		healthClient: &http.Client{
			CheckRedirect: func(
				req *http.Request,
				via []*http.Request,
			) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// Next 选择下一个健康的后端实例
func (b *RoundRobinBalancer) Next() *url.URL {

	if b == nil {
		return nil
	}

	targets := b.snapshotTargets()

	if len(targets) == 0 {
		return nil
	}

	start := (b.counter.Add(1) - 1) %
		uint64(len(targets))

	for offset := 0; offset < len(targets); offset++ {

		index := (int(start) + offset) % len(targets)

		candidate := targets[index]

		if !candidate.healthy.Load() {
			continue
		}

		target := *candidate.target

		return &target
	}

	return nil
}

// StartHealthChecks 启动定期 HTTP 健康检查
func (b *RoundRobinBalancer) StartHealthChecks(
	ctx context.Context,
	interval time.Duration,
	timeout time.Duration,
) error {

	if b == nil || len(b.targets) == 0 {
		return errors.New("balancer has no upstream targets")
	}

	if ctx == nil {
		return errors.New("context cannot be nil")
	}

	if interval <= 0 || timeout <= 0 {
		return errors.New("health check interval and timeout must be positive")
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("health check context is already done: %w", err)
	}

	if !b.healthChecksStarted.CompareAndSwap(false, true) {
		return errors.New("health checks already started")
	}

	// 启动时立即检查一次
	b.checkAll(ctx, timeout)

	// 后台定期检查
	b.healthWG.Add(1)

	go func() {
		defer b.healthWG.Done()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				log.Println("Upstream health checks stopped")
				return

			case <-ticker.C:
				b.checkAll(ctx, timeout)
			}
		}
	}()

	return nil
}

// checkAll 检查全部后端实例
func (b *RoundRobinBalancer) checkAll(
	ctx context.Context,
	timeout time.Duration,
) {

	targets := b.snapshotTargets()

	for _, item := range targets {

		if ctx.Err() != nil {
			return
		}

		checkCtx, cancel := context.WithTimeout(
			ctx,
			timeout,
		)

		healthy := checkHTTP(
			checkCtx,
			b.healthClient,
			item.target,
		)

		cancel()

		if ctx.Err() != nil {
			return
		}

		previous := item.healthy.Swap(healthy)

		if !item.checked || previous != healthy {
			log.Printf(
				"Upstream health: target=%s healthy=%t",
				item.target,
				healthy,
			)
		}

		item.checked = true
	}
}

// checkHTTP 检查后端 HTTP /health 接口
func checkHTTP(
	ctx context.Context,
	client *http.Client,
	target *url.URL,
) bool {

	// 根据上游地址构造健康检查地址
	// 例如 http://127.0.0.1:9090 -> http://127.0.0.1:9090/health
	healthURL := target.JoinPath("health")

	// 健康检查不需要继承上游地址中的查询参数和片段
	healthURL.RawQuery = ""
	healthURL.Fragment = ""

	// 创建绑定 Context 的 HTTP GET 请求
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		healthURL.String(),
		nil,
	)
	if err != nil {
		return false
	}

	// 发送请求
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	// 最多读取 4 KiB 响应体，避免异常响应占用过多资源
	// 同时尽可能让 HTTP 连接得到复用
	_, _ = io.Copy(
		io.Discard,
		io.LimitReader(resp.Body, 4<<10),
	)

	// 只有 HTTP 2xx 才认为后端健康
	return resp.StatusCode >= 200 &&
		resp.StatusCode < 300
}

// 选中后端地址的 Context Key
type selectedUpstreamContextKey struct{}

// 将选中的后端地址放入 Context
func withSelectedUpstream(
	ctx context.Context,
	target *url.URL,
) context.Context {

	return context.WithValue(
		ctx,
		selectedUpstreamContextKey{},
		target,
	)
}

// 从 Context 获取选中的后端地址
func selectedUpstreamFromContext(
	ctx context.Context,
) (*url.URL, bool) {

	target, ok := ctx.Value(
		selectedUpstreamContextKey{},
	).(*url.URL)

	return target, ok
}

// AddTarget 添加新的后端实例
func (b *RoundRobinBalancer) AddTarget(raw string) error {

	if b == nil {
		return errors.New("balancer is nil")
	}

	raw = strings.TrimSpace(raw)

	target, err := url.Parse(raw)
	if err != nil {
		return err
	}

	if (target.Scheme != "http" && target.Scheme != "https") ||
		target.Host == "" {
		return fmt.Errorf("invalid upstream target: %s", raw)
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	// 防止重复添加
	for _, item := range b.targets {
		if item.target.String() == target.String() {
			return fmt.Errorf("upstream already exists: %s", raw)
		}
	}

	b.targets = append(b.targets, &upstream{
		target: target,
	})

	return nil
}

// RemoveTarget 移除指定的后端实例
func (b *RoundRobinBalancer) RemoveTarget(raw string) error {

	if b == nil {
		return errors.New("balancer is nil")
	}

	target, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	for i, item := range b.targets {

		if item.target.String() == target.String() {

			b.targets = append(
				b.targets[:i],
				b.targets[i+1:]...,
			)

			return nil
		}
	}

	return fmt.Errorf("upstream not found: %s", raw)
}

// ListTargets 返回当前所有后端实例的健康状态
func (b *RoundRobinBalancer) ListTargets() []UpstreamStatus {

	if b == nil {
		return nil
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	result := make([]UpstreamStatus, 0, len(b.targets))

	for _, item := range b.targets {
		result = append(result, UpstreamStatus{
			Target:  item.target.String(),
			Healthy: item.healthy.Load(),
		})
	}

	return result
}

// snapshotTargets 获取后端实例切片的快照
func (b *RoundRobinBalancer) snapshotTargets() []*upstream {

	if b == nil {
		return nil
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	return append(
		[]*upstream(nil),
		b.targets...,
	)
}

// WaitHealthChecks 等待健康检查 Goroutine 全部退出
func (b *RoundRobinBalancer) WaitHealthChecks() {
	if b == nil {
		return
	}

	b.healthWG.Wait()
}
