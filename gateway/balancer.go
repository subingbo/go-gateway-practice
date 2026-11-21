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

// upstreamOutcomeReporter 接收真实代理请求的传输结果。
// 业务响应只用于维护被动失败窗口，不直接修改主动健康检查状态。
type upstreamOutcomeReporter interface {
	ReportFailure(target *url.URL)
	ReportSuccess(target *url.URL)
}

// upstream 表示一个后端实例
type upstream struct {
	target  *url.URL
	healthy atomic.Bool
	checked bool
	passive passiveFailureTracker
}

// RoundRobinBalancer 实现轮询负载均衡
type RoundRobinBalancer struct {
	mu                  sync.RWMutex
	targets             []*upstream
	counter             atomic.Uint64
	healthChecksStarted atomic.Bool
	healthClient        *http.Client
	healthWG            sync.WaitGroup
	eventPublisher      upstreamEventPublisher
}

type RoundRobinBalancerOption func(*RoundRobinBalancer)

func WithUpstreamEventPublisher(
	publisher upstreamEventPublisher,
) RoundRobinBalancerOption {
	return func(b *RoundRobinBalancer) {
		b.eventPublisher = publisher
	}
}

// 编译期检查接口实现
var _ LoadBalancer = (*RoundRobinBalancer)(nil)
var _ upstreamOutcomeReporter = (*RoundRobinBalancer)(nil)

func newUpstream(target *url.URL) *upstream {
	return &upstream{
		target: target,
		passive: newPassiveFailureTracker(
			defaultPassiveFailureThreshold,
			defaultPassiveFailureWindow,
			defaultPassiveRecoveryThreshold,
		),
	}
}

// NewRoundRobinBalancer 创建轮询负载均衡器
func NewRoundRobinBalancer(
	rawTargets []string,
	options ...RoundRobinBalancerOption,
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

		targets = append(targets, newUpstream(target))
	}

	balancer := &RoundRobinBalancer{
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
	}

	for _, option := range options {
		if option != nil {
			option(balancer)
		}
	}

	return balancer, nil
}

// Next 选择下一个健康的后端实例
func (b *RoundRobinBalancer) Next() *url.URL {
	return b.nextExcluding("")
}

// NextExcluding 选择下一台可用实例，并明确跳过刚刚请求失败的地址。
// 它用于幂等请求重试，保证第二次尝试不会再次命中原实例。
func (b *RoundRobinBalancer) NextExcluding(excluded *url.URL) *url.URL {
	excludedAddress := ""
	if excluded != nil {
		excludedAddress = excluded.String()
	}

	return b.nextExcluding(excludedAddress)
}

func (b *RoundRobinBalancer) nextExcluding(excludedAddress string) *url.URL {

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

		if candidate.target.String() == excludedAddress {
			continue
		}
		if !candidate.healthy.Load() {
			continue
		}
		if candidate.passive.isEjected() {
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
		if item.passive.recordHealthProbe(healthy) {
			b.publishEvent(upstreamEvent{
				Kind:             upstreamEventRecovered,
				Target:           item.target.String(),
				SuccessfulProbes: defaultPassiveRecoveryThreshold,
			})
		}

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

	b.targets = append(b.targets, newUpstream(target))

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

// ReplaceTargets 用服务发现返回的完整实例列表替换当前列表。
// 地址未变化的实例会复用原来的 upstream，从而保留已经探测到的健康状态；
// 新出现的实例默认不健康，要等 HTTP 健康检查通过后才会参与转发。
func (b *RoundRobinBalancer) ReplaceTargets(rawTargets []string) error {

	if b == nil {
		return errors.New("balancer is nil")
	}

	if len(rawTargets) == 0 {
		return errors.New("at least one upstream target is required")
	}

	// 先完整解析新列表，再获取写锁。只要其中一个地址非法，旧列表就完全不变。
	parsedTargets := make([]*url.URL, 0, len(rawTargets))

	for _, raw := range rawTargets {
		raw = strings.TrimSpace(raw)

		target, err := url.Parse(raw)
		if err != nil {
			return fmt.Errorf("parse target %q: %w", raw, err)
		}

		if (target.Scheme != "http" && target.Scheme != "https") ||
			target.Host == "" {
			return fmt.Errorf("invalid upstream target: %s", raw)
		}

		parsedTargets = append(parsedTargets, target)
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	existingByAddress := make(map[string]*upstream, len(b.targets))
	for _, item := range b.targets {
		existingByAddress[item.target.String()] = item
	}

	nextTargets := make([]*upstream, 0, len(parsedTargets))
	for _, target := range parsedTargets {
		if existing := existingByAddress[target.String()]; existing != nil {
			nextTargets = append(nextTargets, existing)
			continue
		}

		nextTargets = append(nextTargets, newUpstream(target))
	}

	b.targets = nextTargets

	return nil
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
			Healthy: item.healthy.Load() && !item.passive.isEjected(),
		})
	}

	return result
}

// ReportFailure 把真实代理请求的传输失败计入目标实例的滑动窗口。
// 达到阈值只改变被动摘除状态，不覆盖主动 /health 检查结果。
func (b *RoundRobinBalancer) ReportFailure(target *url.URL) {
	item := b.findTarget(target)
	if item == nil {
		return
	}

	if item.passive.recordFailure(time.Now()) {
		b.publishEvent(upstreamEvent{
			Kind:     upstreamEventEjected,
			Target:   item.target.String(),
			Failures: defaultPassiveFailureThreshold,
			Window:   defaultPassiveFailureWindow,
		})
	}
}

// ReportSuccess 清空尚未摘除实例的连续传输失败记录。
func (b *RoundRobinBalancer) ReportSuccess(target *url.URL) {
	item := b.findTarget(target)
	if item == nil {
		return
	}

	item.passive.recordRequestSuccess()
}

func (b *RoundRobinBalancer) findTarget(target *url.URL) *upstream {
	if b == nil || target == nil {
		return nil
	}

	address := target.String()
	for _, item := range b.snapshotTargets() {
		if item.target.String() == address {
			return item
		}
	}

	return nil
}

func (b *RoundRobinBalancer) publishEvent(event upstreamEvent) {
	if b == nil || b.eventPublisher == nil {
		return
	}

	b.eventPublisher.Publish(event)
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
