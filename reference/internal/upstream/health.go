package upstream

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"
)

// HealthCheckConfig 是主动健康检查参数。
type HealthCheckConfig struct {
	Interval           time.Duration
	Timeout            time.Duration
	UnhealthyThreshold int
	HealthyThreshold   int
}

// HealthChecker 周期性访问实例的 health URL。
//
// 初始状态是健康，避免服务刚启动时被判定为不可用；连续失败达到阈值后
// 标记为不健康，连续成功达到阈值后恢复。
type HealthChecker struct {
	url      string
	config   HealthCheckConfig
	client   *http.Client
	onFlip   func(healthy bool)
	onResult func(healthy bool)

	mu      sync.RWMutex
	healthy bool

	unhealthyCount int
	healthyCount   int
}

// NewHealthChecker 创建健康检查器。url 为空时始终返回健康。
func NewHealthChecker(url string, config HealthCheckConfig, onFlip func(bool)) *HealthChecker {
	if config.Interval <= 0 {
		config.Interval = 3 * time.Second
	}
	if config.Timeout <= 0 {
		config.Timeout = time.Second
	}
	if config.UnhealthyThreshold <= 0 {
		config.UnhealthyThreshold = 3
	}
	if config.HealthyThreshold <= 0 {
		config.HealthyThreshold = 2
	}

	return &HealthChecker{
		url:     url,
		config:  config,
		client:  &http.Client{Timeout: config.Timeout},
		onFlip:  onFlip,
		healthy: true,
	}
}

// SetOnResult 注册“每次检查后”的回调。
//
// onFlip 只在健康状态翻转时触发，适合打日志；onResult 每次检查都会
// 收到当前判定，适合把 UNKNOWN 首次刷新成 HEALTHY。
func (h *HealthChecker) SetOnResult(fn func(healthy bool)) {
	h.mu.Lock()
	h.onResult = fn
	h.mu.Unlock()
}

// Healthy 返回最近一次判定结果。
func (h *HealthChecker) Healthy() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return h.healthy
}

// Run 阻塞执行健康检查，直到 ctx 结束。
func (h *HealthChecker) Run(ctx context.Context) {
	if h.url == "" {
		return
	}

	h.check(ctx)

	ticker := time.NewTicker(h.config.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.check(ctx)
		}
	}
}

func (h *HealthChecker) check(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.url, nil)
	if err != nil {
		h.record(false)
		return
	}

	resp, err := h.client.Do(req)
	if err != nil {
		h.record(false)
		return
	}

	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	_ = resp.Body.Close()

	h.record(resp.StatusCode >= 200 && resp.StatusCode < 400)
}

func (h *HealthChecker) record(ok bool) {
	h.mu.Lock()

	flipped := false

	if ok {
		h.unhealthyCount = 0
		h.healthyCount++

		if !h.healthy && h.healthyCount >= h.config.HealthyThreshold {
			h.healthy = true
			flipped = true
		}
	} else {
		h.healthyCount = 0
		h.unhealthyCount++

		if h.healthy && h.unhealthyCount >= h.config.UnhealthyThreshold {
			h.healthy = false
			flipped = true
		}
	}

	current := h.healthy
	onFlip := h.onFlip
	onResult := h.onResult
	h.mu.Unlock()

	// 回调里通常只做日志/打点，避免持锁调用外部逻辑。
	if flipped && onFlip != nil {
		go onFlip(current)
	}
	if onResult != nil {
		go onResult(current)
	}
}
