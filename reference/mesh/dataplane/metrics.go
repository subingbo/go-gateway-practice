package dataplane

import (
	"strconv"
	"time"

	"example.com/go-gateway-reference/internal/metrics"
)

// Metrics 是数据面共享的指标集合。
type Metrics struct {
	requests  *metrics.CounterVec
	durations *metrics.HistogramVec
	retries   *metrics.CounterVec
	ejections *metrics.CounterVec
}

// NewMetrics 在一个注册表上创建数据面指标。
func NewMetrics(registry *metrics.Registry) *Metrics {
	return &Metrics{
		requests: registry.Counter(
			"mesh_requests_total",
			"Total mesh requests",
			"direction", "source", "destination", "code",
		),
		durations: registry.Histogram(
			"mesh_request_duration_seconds",
			"Mesh request duration in seconds",
			[]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5},
			"direction", "source", "destination",
		),
		retries: registry.Counter(
			"mesh_retries_total",
			"Total mesh retries",
			"source", "destination",
		),
		ejections: registry.Counter(
			"mesh_outlier_ejections_total",
			"Total outlier ejections",
			"source", "destination", "endpoint",
		),
	}
}

// Observe 记录一次请求。
func (m *Metrics) Observe(direction, source, destination string, status int, duration time.Duration) {
	if m == nil {
		return
	}

	m.requests.Inc(direction, source, destination, strconv.Itoa(status))
	m.durations.Observe(duration.Seconds(), direction, source, destination)
}

// IncRetry 记录一次重试。
func (m *Metrics) IncRetry(source, destination string) {
	if m == nil {
		return
	}

	m.retries.Inc(source, destination)
}

func (m *Metrics) incEjection(source, destination, endpoint string) {
	if m == nil {
		return
	}

	m.ejections.Inc(source, destination, endpoint)
}
