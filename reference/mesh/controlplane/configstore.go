package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"example.com/go-gateway-reference/internal/meshapi"
	"example.com/go-gateway-reference/internal/meshconfig"
)

// ConfigStore 保存网格配置快照，并负责把变化通知给 watch 客户端。
type ConfigStore struct {
	mu sync.RWMutex

	dir         string
	trustBundle string
	logger      *slog.Logger
	registry    *Registry

	virtualServices  []meshapi.VirtualService
	destinationRules []meshapi.DestinationRule
	version          uint64
	generatedAt      time.Time
	hash             string
	lastError        string

	watchers map[chan struct{}]struct{}
}

// NewConfigStore 创建配置存储。
func NewConfigStore(dir, trustBundle string, logger *slog.Logger) *ConfigStore {
	return &ConfigStore{
		dir:         dir,
		trustBundle: trustBundle,
		logger:      logger,
		watchers:    make(map[chan struct{}]struct{}),
	}
}

// SetRegistry 绑定注册中心，之后 Snapshot 会包含服务实例。
func (s *ConfigStore) SetRegistry(registry *Registry) {
	s.mu.Lock()
	s.registry = registry
	s.mu.Unlock()
}

// Load 重新读取配置目录。失败时保留旧配置，并把错误暴露到状态接口。
func (s *ConfigStore) Load() error {
	hash, err := meshconfig.HashDir(s.dir)
	if err != nil {
		s.setError(err)
		return err
	}

	virtualServices, destinationRules, err := meshconfig.LoadDir(s.dir)
	if err != nil {
		s.setError(err)
		return err
	}

	s.mu.Lock()
	s.virtualServices = virtualServices
	s.destinationRules = destinationRules
	s.hash = hash
	s.version++
	s.generatedAt = time.Now()
	s.lastError = ""
	version := s.version
	s.mu.Unlock()

	s.logger.Info("mesh config loaded",
		"version", version,
		"virtual_services", len(virtualServices),
		"destination_rules", len(destinationRules),
	)

	s.notify()
	return nil
}

// Run 每 2 秒检查配置目录的哈希。
func (s *ConfigStore) Run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.poll()
		}
	}
}

func (s *ConfigStore) poll() {
	hash, err := meshconfig.HashDir(s.dir)
	if err != nil {
		s.logger.Warn("mesh config hash failed", "error", err)
		return
	}

	s.mu.RLock()
	current := s.hash
	s.mu.RUnlock()

	if hash == current {
		return
	}

	if err := s.Load(); err != nil {
		s.logger.Error("mesh config reload rejected; keeping previous config", "error", err)
	}
}

// Snapshot 生成一份完整快照。
func (s *ConfigStore) Snapshot() meshapi.Snapshot {
	s.mu.RLock()
	version := s.version
	virtualServices := s.virtualServices
	destinationRules := s.destinationRules
	trustBundle := s.trustBundle
	registry := s.registry
	s.mu.RUnlock()

	var services []meshapi.ServiceSnapshot
	if registry != nil {
		services = registry.Snapshot()
	}

	return meshapi.Snapshot{
		Version:          version,
		GeneratedAt:      time.Now(),
		Services:         services,
		VirtualServices:  virtualServices,
		DestinationRules: destinationRules,
		TrustBundlePEM:   trustBundle,
	}
}

// Bump 在注册表变化时递增版本并通知订阅者。
func (s *ConfigStore) Bump() {
	s.mu.Lock()
	s.version++
	s.generatedAt = time.Now()
	version := s.version
	s.mu.Unlock()

	s.logger.Debug("snapshot version bumped", "version", version)
	s.notify()
}

// Version 返回当前版本。
func (s *ConfigStore) Version() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.version
}

// LastError 返回最近一次配置错误。
func (s *ConfigStore) LastError() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastError
}

// Subscribe 订阅配置变化。
func (s *ConfigStore) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)

	s.mu.Lock()
	s.watchers[ch] = struct{}{}
	s.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			// 不关闭 channel，避免 notify 在解锁后发送到已关闭的 channel。
			// watch handler 通过请求 context 退出。
			s.mu.Lock()
			delete(s.watchers, ch)
			s.mu.Unlock()
		})
	}

	return ch, cancel
}

func (s *ConfigStore) notify() {
	s.mu.RLock()
	watchers := make([]chan struct{}, 0, len(s.watchers))
	for ch := range s.watchers {
		watchers = append(watchers, ch)
	}
	s.mu.RUnlock()

	for _, ch := range watchers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (s *ConfigStore) setError(err error) {
	s.mu.Lock()
	s.lastError = err.Error()
	s.mu.Unlock()

	s.logger.Error("mesh config load failed", "error", err)
}
