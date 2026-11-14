package upstream

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

var (
	// ErrNoHealthyEndpoint 表示目标服务当前没有可用实例。
	ErrNoHealthyEndpoint = errors.New("no healthy upstream endpoint")

	// ErrTooManyPendingRequests 表示连接池的排队请求已满。
	ErrTooManyPendingRequests = errors.New("too many pending requests")
)

// OutlierPolicy 是离群摘除策略。
type OutlierPolicy struct {
	Consecutive5xx int
	EjectionTime   time.Duration
}

// Cluster 管理一个目标服务（或子集）的实例集合。
type Cluster struct {
	Name string

	mu        sync.RWMutex
	endpoints []*Endpoint
	balancer  Balancer
	outlier   OutlierPolicy
	pending   chan struct{}
	counter   atomic.Uint64

	onEject func(addr string)
}

// NewCluster 创建一个集群。maxPending <= 0 表示不限制排队请求。
func NewCluster(name, balancer string, outlier OutlierPolicy, maxPending int) *Cluster {
	cluster := &Cluster{
		Name:     name,
		balancer: NewBalancer(balancer),
		outlier:  outlier,
	}

	if maxPending > 0 {
		cluster.pending = make(chan struct{}, maxPending)
	}

	return cluster
}

// SetOnEject 注册摘除回调，用于打点或日志。
func (c *Cluster) SetOnEject(fn func(addr string)) {
	c.mu.Lock()
	c.onEject = fn
	c.mu.Unlock()
}

// SetEndpoints 用新快照替换实例列表，并按地址保留在途与熔断状态。
func (c *Cluster) SetEndpoints(list []EndpointInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()

	previous := make(map[string]*Endpoint, len(c.endpoints))
	for _, ep := range c.endpoints {
		previous[ep.Addr] = ep
	}

	sort.Slice(list, func(i, j int) bool {
		return list[i].Addr < list[j].Addr
	})

	endpoints := make([]*Endpoint, 0, len(list))
	seen := make(map[string]struct{}, len(list))

	for _, info := range list {
		if info.Addr == "" {
			continue
		}

		if _, ok := seen[info.Addr]; ok {
			continue
		}

		seen[info.Addr] = struct{}{}

		if old, ok := previous[info.Addr]; ok {
			old.Labels = cloneLabels(info.Labels)
			old.Healthy = info.Healthy
			endpoints = append(endpoints, old)
			continue
		}

		endpoints = append(endpoints, newEndpoint(info))
	}

	c.endpoints = endpoints
}

// SetEndpointHealth 更新单个实例的健康状态，供主动健康检查使用。
func (c *Cluster) SetEndpointHealth(addr string, healthy bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, endpoint := range c.endpoints {
		if endpoint.Addr == addr {
			endpoint.Healthy = healthy
			return
		}
	}
}

// Endpoints 返回当前实例状态副本。
func (c *Cluster) Endpoints() []EndpointStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()

	statuses := make([]EndpointStatus, 0, len(c.endpoints))
	for _, ep := range c.endpoints {
		statuses = append(statuses, EndpointStatus{
			Addr:     ep.Addr,
			Labels:   cloneLabels(ep.Labels),
			Healthy:  ep.Healthy,
			Ejected:  ep.Ejected(),
			InFlight: ep.inFlight.Load(),
		})
	}

	return statuses
}

// Pick 从可用实例中选择一个。
func (c *Cluster) Pick() (*Endpoint, error) {
	c.mu.RLock()
	available := make([]*Endpoint, 0, len(c.endpoints))
	for _, ep := range c.endpoints {
		if ep.Available() {
			available = append(available, ep)
		}
	}
	balancer := c.balancer
	c.mu.RUnlock()

	if len(available) == 0 {
		return nil, ErrNoHealthyEndpoint
	}

	return balancer.Pick(available, &c.counter), nil
}

// Acquire 占用一个排队名额；未配置上限时直接成功。
func (c *Cluster) Acquire() error {
	if c.pending == nil {
		return nil
	}

	select {
	case c.pending <- struct{}{}:
		return nil
	default:
		return ErrTooManyPendingRequests
	}
}

// Release 释放排队名额。
func (c *Cluster) Release() {
	if c.pending == nil {
		return
	}

	select {
	case <-c.pending:
	default:
	}
}

// Report 根据一次请求的结果更新熔断状态。
func (c *Cluster) Report(ep *Endpoint, status int, err error) {
	if ep == nil {
		return
	}

	if errors.Is(err, context.Canceled) {
		return
	}

	failed := err != nil || status >= 500
	if !failed {
		ep.RecordSuccess()
		return
	}

	c.mu.RLock()
	policy := c.outlier
	callback := c.onEject
	c.mu.RUnlock()

	if ep.RecordFailure(policy.Consecutive5xx, policy.EjectionTime) && callback != nil {
		callback(ep.Addr)
	}
}
