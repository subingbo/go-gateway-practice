package upstream

import (
	"sync/atomic"
	"time"
)

// Endpoint 是一个可被负载均衡选中的上游实例。
//
// InFlight 用于 least_request；ejectedUntil 用于被动熔断（离群摘除）。
// 这些状态在控制面推送新快照时按地址保留，避免每次刷新都清空熔断状态。
type Endpoint struct {
	Addr   string
	Labels map[string]string

	Healthy bool

	inFlight     atomic.Int64
	consecutive  atomic.Int32
	ejectedUntil atomic.Int64 // UnixNano
}

// EndpointInfo 是构建 Endpoint 的只读输入。
type EndpointInfo struct {
	Addr    string
	Labels  map[string]string
	Healthy bool
}

// EndpointStatus 是暴露给管理接口的实例状态。
type EndpointStatus struct {
	Addr     string            `json:"addr"`
	Subset   string            `json:"subset,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
	Healthy  bool              `json:"healthy"`
	Ejected  bool              `json:"ejected"`
	InFlight int64             `json:"in_flight"`
}

func newEndpoint(info EndpointInfo) *Endpoint {
	return &Endpoint{
		Addr:    info.Addr,
		Labels:  cloneLabels(info.Labels),
		Healthy: info.Healthy,
	}
}

// Available 返回实例当前是否可以接收请求。
func (e *Endpoint) Available() bool {
	if e == nil || !e.Healthy {
		return false
	}

	return time.Now().UnixNano() >= e.ejectedUntil.Load()
}

// Ejected 返回实例是否处于摘除窗口内。
func (e *Endpoint) Ejected() bool {
	return time.Now().UnixNano() < e.ejectedUntil.Load()
}

// Begin 记录一个正在处理的请求。
func (e *Endpoint) Begin() {
	e.inFlight.Add(1)
}

// End 结束一个请求；防御性地避免计数变成负数。
func (e *Endpoint) End() {
	if e.inFlight.Add(-1) < 0 {
		e.inFlight.Store(0)
	}
}

// Eject 在 d 时间内跳过该实例。
func (e *Endpoint) Eject(d time.Duration) {
	if d <= 0 {
		d = 5 * time.Second
	}

	e.ejectedUntil.Store(time.Now().Add(d).UnixNano())
	e.consecutive.Store(0)
}

// RecordSuccess 清零连续失败计数。
func (e *Endpoint) RecordSuccess() {
	e.consecutive.Store(0)
}

// RecordFailure 增加连续失败计数，达到阈值时摘除实例。
//
// 返回 true 表示本次调用触发了摘除。
func (e *Endpoint) RecordFailure(threshold int, ejection time.Duration) bool {
	if threshold <= 0 {
		return false
	}

	if e.consecutive.Add(1) < int32(threshold) {
		return false
	}

	e.Eject(ejection)
	return true
}

func cloneLabels(labels map[string]string) map[string]string {
	if len(labels) == 0 {
		return nil
	}

	out := make(map[string]string, len(labels))
	for k, v := range labels {
		out[k] = v
	}

	return out
}
