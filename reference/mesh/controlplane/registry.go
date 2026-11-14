package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"example.com/go-gateway-reference/internal/meshapi"
	"example.com/go-gateway-reference/internal/upstream"
)

type registeredInstance struct {
	instance meshapi.Instance
	cancel   context.CancelFunc
	checker  *upstream.HealthChecker
}

// Registry 是控制面的服务注册中心。
//
// sidecar 注册时上报 inbound 地址和应用的 health URL；控制面主动探测
// health URL，把实例标记为 HEALTHY / UNHEALTHY，并在心跳超时后移除。
type Registry struct {
	mu          sync.RWMutex
	ttl         time.Duration
	checkConfig upstream.HealthCheckConfig
	logger      *slog.Logger
	onChange    func()
	instances   map[string]*registeredInstance
}

// NewRegistry 创建注册中心。
func NewRegistry(
	ttl time.Duration,
	checkConfig upstream.HealthCheckConfig,
	logger *slog.Logger,
	onChange func(),
) *Registry {
	if ttl <= 0 {
		ttl = 45 * time.Second
	}

	return &Registry{
		ttl:         ttl,
		checkConfig: checkConfig,
		logger:      logger,
		onChange:    onChange,
		instances:   make(map[string]*registeredInstance),
	}
}

// SetOnChange 设置注册表变化回调。
func (r *Registry) SetOnChange(fn func()) {
	r.mu.Lock()
	r.onChange = fn
	r.mu.Unlock()
}

// Register 新增或更新一个实例。
func (r *Registry) Register(instance meshapi.Instance) error {
	instance.Service = strings.TrimSpace(instance.Service)
	instance.ID = strings.TrimSpace(instance.ID)
	instance.Address = strings.TrimSpace(instance.Address)

	if instance.Service == "" {
		return errors.New("service is required")
	}
	if instance.ID == "" {
		return errors.New("instance id is required")
	}
	if instance.Address == "" {
		return errors.New("address is required")
	}
	if instance.Namespace == "" {
		instance.Namespace = "default"
	}

	now := time.Now()
	key := instanceKey(instance.Service, instance.ID)

	restartChecker := false

	r.mu.Lock()
	entry, exists := r.instances[key]

	if exists {
		healthURLChanged := entry.instance.HealthURL != instance.HealthURL

		entry.instance.Address = instance.Address
		entry.instance.Labels = cloneMap(instance.Labels)
		entry.instance.HealthURL = instance.HealthURL
		entry.instance.Namespace = instance.Namespace
		entry.instance.LastSeen = now

		if healthURLChanged {
			if entry.cancel != nil {
				entry.cancel()
				entry.cancel = nil
			}
			entry.checker = nil
			restartChecker = true
		}
	} else {
		instance.Status = meshapi.StatusUnknown
		instance.LastSeen = now
		instance.RegisteredAt = now

		entry = &registeredInstance{instance: instance}
		r.instances[key] = entry
		restartChecker = true
	}
	r.mu.Unlock()

	if restartChecker {
		r.startChecker(key)
	}

	r.logger.Info("instance registered",
		"service", instance.Service,
		"instance", instance.ID,
		"address", instance.Address,
		"health_url", instance.HealthURL,
	)

	r.notifyChange()
	return nil
}

// Heartbeat 刷新实例的最后活跃时间，并记录 sidecar 上报的运行信息。
func (r *Registry) Heartbeat(service, id string, configVersion uint64, certExpiry time.Time) bool {
	key := instanceKey(service, id)

	r.mu.Lock()
	defer r.mu.Unlock()

	entry, ok := r.instances[key]
	if !ok {
		return false
	}

	entry.instance.LastSeen = time.Now()
	entry.instance.ConfigVersion = configVersion
	entry.instance.CertExpiry = certExpiry
	return true
}

// Deregister 移除实例并停止健康检查。
func (r *Registry) Deregister(service, id string) bool {
	key := instanceKey(service, id)

	r.mu.Lock()
	entry, ok := r.instances[key]
	if ok {
		delete(r.instances, key)
		if entry.cancel != nil {
			entry.cancel()
		}
	}
	r.mu.Unlock()

	if !ok {
		return false
	}

	r.logger.Info("instance deregistered", "service", service, "instance", id)
	r.notifyChange()
	return true
}

// Snapshot 返回按服务分组的实例视图。
func (r *Registry) Snapshot() []meshapi.ServiceSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()

	byService := make(map[string][]meshapi.Instance)
	namespaces := make(map[string]string)

	for _, entry := range r.instances {
		instance := entry.instance
		instance.Labels = cloneMap(instance.Labels)

		byService[instance.Service] = append(byService[instance.Service], instance)
		namespaces[instance.Service] = instance.Namespace
	}

	names := make([]string, 0, len(byService))
	for name := range byService {
		names = append(names, name)
	}
	sort.Strings(names)

	services := make([]meshapi.ServiceSnapshot, 0, len(names))
	for _, name := range names {
		instances := byService[name]
		sort.Slice(instances, func(i, j int) bool {
			return instances[i].ID < instances[j].ID
		})

		services = append(services, meshapi.ServiceSnapshot{
			Name:      name,
			Namespace: namespaces[name],
			Instances: instances,
		})
	}

	return services
}

// StatusCounts 返回 service -> status -> 数量，供指标使用。
func (r *Registry) StatusCounts() map[string]map[meshapi.InstanceStatus]int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	counts := make(map[string]map[meshapi.InstanceStatus]int)

	for _, entry := range r.instances {
		service := entry.instance.Service
		if counts[service] == nil {
			counts[service] = make(map[meshapi.InstanceStatus]int)
		}
		counts[service][entry.instance.Status]++
	}

	return counts
}

// Run 启动 TTL 清理循环，直到 ctx 结束。
func (r *Registry) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.expireStale()
		}
	}
}

// Stop 停止所有健康检查。
func (r *Registry) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, entry := range r.instances {
		if entry.cancel != nil {
			entry.cancel()
		}
	}
}

func (r *Registry) expireStale() {
	now := time.Now()

	r.mu.Lock()
	var removed []meshapi.Instance

	for key, entry := range r.instances {
		if now.Sub(entry.instance.LastSeen) <= r.ttl {
			continue
		}

		removed = append(removed, entry.instance)
		delete(r.instances, key)

		if entry.cancel != nil {
			entry.cancel()
		}
	}
	r.mu.Unlock()

	if len(removed) == 0 {
		return
	}

	for _, instance := range removed {
		r.logger.Warn("instance expired",
			"service", instance.Service,
			"instance", instance.ID,
			"last_seen", instance.LastSeen,
		)
	}

	r.notifyChange()
}

func (r *Registry) startChecker(key string) {
	r.mu.Lock()
	entry, ok := r.instances[key]
	if !ok {
		r.mu.Unlock()
		return
	}

	if entry.cancel != nil {
		entry.cancel()
	}

	ctx, cancel := context.WithCancel(context.Background())
	checker := upstream.NewHealthChecker(entry.instance.HealthURL, r.checkConfig, nil)
	checker.SetOnResult(func(healthy bool) {
		r.setStatus(key, healthy)
	})

	entry.cancel = cancel
	entry.checker = checker
	healthURL := entry.instance.HealthURL
	r.mu.Unlock()

	if healthURL == "" {
		// 没有 health URL 时默认视为健康。
		r.setStatus(key, true)
		return
	}

	go checker.Run(ctx)
}

func (r *Registry) setStatus(key string, healthy bool) {
	status := meshapi.StatusHealthy
	if !healthy {
		status = meshapi.StatusUnhealthy
	}

	r.mu.Lock()
	entry, ok := r.instances[key]
	if !ok {
		r.mu.Unlock()
		return
	}

	changed := entry.instance.Status != status
	entry.instance.Status = status
	service := entry.instance.Service
	id := entry.instance.ID
	r.mu.Unlock()

	if changed {
		r.logger.Info("instance health changed",
			"service", service,
			"instance", id,
			"status", status,
		)
		r.notifyChange()
	}
}

func (r *Registry) notifyChange() {
	r.mu.RLock()
	callback := r.onChange
	r.mu.RUnlock()

	if callback != nil {
		callback()
	}
}

func instanceKey(service, id string) string {
	return fmt.Sprintf("%s\x00%s", service, id)
}

func cloneMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}

	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}
