package main

import (
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	metricsRecentLimit  = 50
	metricsLatencyLimit = 200
)

// requestRecord 是一条请求快照
type requestRecord struct {
	Time       time.Time `json:"time"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	Status     int       `json:"status"`
	DurationMS float64   `json:"duration_ms"`
}

// statusClasses 按状态码区间统计请求数
type statusClasses struct {
	Status2xx int64 `json:"2xx"`
	Status3xx int64 `json:"3xx"`
	Status4xx int64 `json:"4xx"`
	Status5xx int64 `json:"5xx"`
}

// routeMetrics 是按路由聚合的统计
type routeMetrics struct {
	Path       string  `json:"path"`
	Count      int64   `json:"count"`
	ErrorCount int64   `json:"error_count"`
	AvgMS      float64 `json:"avg_ms"`

	totalMS float64
}

// metricsSnapshot 是 /api/stats 的响应体
type metricsSnapshot struct {
	StartedAt     time.Time       `json:"started_at"`
	UptimeSeconds float64         `json:"uptime_seconds"`
	InFlight      int64           `json:"in_flight"`
	TotalRequests int64           `json:"total_requests"`
	StatusClasses statusClasses   `json:"status_classes"`
	AvgLatencyMS  float64         `json:"avg_latency_ms"`
	P95LatencyMS  float64         `json:"p95_latency_ms"`
	Routes        []routeMetrics  `json:"routes"`
	Recent        []requestRecord `json:"recent"`
}

// metricsStore 在进程内保存网关请求指标
type metricsStore struct {
	mu sync.Mutex

	startedAt time.Time
	inFlight  int64

	total         int64
	statusClasses statusClasses
	latencies     []float64
	recent        []requestRecord
	routes        map[string]*routeMetrics
}

func newMetricsStore() *metricsStore {
	return &metricsStore{
		startedAt: time.Now(),
		latencies: make([]float64, 0, metricsLatencyLimit),
		recent:    make([]requestRecord, 0, metricsRecentLimit),
		routes:    make(map[string]*routeMetrics),
	}
}

// beginRequest 记录一个正在处理的请求
func (m *metricsStore) beginRequest() {
	m.mu.Lock()
	m.inFlight++
	m.mu.Unlock()
}

// finishRequest 记录一个已完成请求的状态与耗时
func (m *metricsStore) finishRequest(record requestRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.inFlight--

	if m.inFlight < 0 {
		m.inFlight = 0
	}

	m.total++

	switch {
	case record.Status >= 200 && record.Status < 300:
		m.statusClasses.Status2xx++
	case record.Status >= 300 && record.Status < 400:
		m.statusClasses.Status3xx++
	case record.Status >= 400 && record.Status < 500:
		m.statusClasses.Status4xx++
	case record.Status >= 500:
		m.statusClasses.Status5xx++
	}

	m.latencies = append(m.latencies, record.DurationMS)
	if len(m.latencies) > metricsLatencyLimit {
		m.latencies = m.latencies[len(m.latencies)-metricsLatencyLimit:]
	}

	m.recent = append(m.recent, record)
	if len(m.recent) > metricsRecentLimit {
		m.recent = m.recent[len(m.recent)-metricsRecentLimit:]
	}

	key := routeGroup(record.Path)

	stats, ok := m.routes[key]
	if !ok {
		stats = &routeMetrics{Path: key}
		m.routes[key] = stats
	}

	stats.Count++
	stats.totalMS += record.DurationMS

	if record.Status >= 400 {
		stats.ErrorCount++
	}
}

// routeGroup 把具体路径归并到路由前缀
func routeGroup(path string) string {
	for _, route := range proxyRoutes {
		if strings.HasPrefix(path, route.Path) {
			return route.Path
		}
	}

	return path
}

// snapshot 返回当前指标的副本
func (m *metricsStore) snapshot() metricsSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	snapshot := metricsSnapshot{
		StartedAt:     m.startedAt,
		UptimeSeconds: round2(time.Since(m.startedAt).Seconds()),
		InFlight:      m.inFlight,
		TotalRequests: m.total,
		StatusClasses: m.statusClasses,
		Routes:        make([]routeMetrics, 0, len(m.routes)),
		Recent:        append([]requestRecord(nil), m.recent...),
	}

	for _, route := range m.routes {
		item := *route

		if item.Count > 0 {
			item.AvgMS = round2(item.totalMS / float64(item.Count))
		}

		snapshot.Routes = append(snapshot.Routes, item)
	}

	sort.Slice(snapshot.Routes, func(i, j int) bool {
		return snapshot.Routes[i].Count > snapshot.Routes[j].Count
	})

	if len(m.latencies) > 0 {
		var total float64

		for _, latency := range m.latencies {
			total += latency
		}

		snapshot.AvgLatencyMS = round2(total / float64(len(m.latencies)))
		snapshot.P95LatencyMS = round2(percentile(m.latencies, 95))
	}

	return snapshot
}

// percentile 计算给定分位的延迟
func percentile(values []float64, p float64) float64 {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)

	index := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}

	return sorted[index]
}

func round2(value float64) float64 {
	return math.Round(value*100) / 100
}

// statusRecorder 捕获 Handler 写出的状态码
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status != 0 {
		return
	}

	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}

	return r.ResponseWriter.Write(data)
}

// Unwrap 让 http.ResponseController 能访问底层 ResponseWriter
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// metricsMiddleware 统计所有经过网关的请求
func metricsMiddleware(
	next http.Handler,
	store *metricsStore,
) http.Handler {

	return http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		// 看板自身的健康检查与指标轮询不计入统计
		if isObservabilityPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()

		store.beginRequest()

		recorder := &statusRecorder{ResponseWriter: w}

		next.ServeHTTP(recorder, r)

		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}

		store.finishRequest(requestRecord{
			Time:       start,
			Method:     r.Method,
			Path:       r.URL.Path,
			Status:     status,
			DurationMS: round2(float64(time.Since(start).Microseconds()) / 1000),
		})
	})
}

// isObservabilityPath 判断是否为看板自身的观测请求或静态资源
func isObservabilityPath(path string) bool {
	switch {
	case path == "/health", path == "/api/routes", path == "/api/stats":
		return true
	case path == "/", path == "/favicon.ico":
		return true
	case strings.HasPrefix(path, "/assets/"):
		return true
	default:
		return false
	}
}

// statsHandler 返回进程内指标快照
func statsHandler(store *metricsStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		if err := json.NewEncoder(w).Encode(store.snapshot()); err != nil {
			http.Error(w, "Encode response failed", http.StatusInternalServerError)
		}
	}
}
