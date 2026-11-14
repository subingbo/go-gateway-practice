package main

import (
	"math"
	"sort"
	"sync"
	"time"
)

const (
	statsLatencyLimit = 500
	statsRecentLimit  = 100
)

// requestRecord 是一条请求快照。
type requestRecord struct {
	Time       time.Time `json:"time"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	Route      string    `json:"route"`
	Status     int       `json:"status"`
	DurationMS float64   `json:"duration_ms"`
	RequestID  string    `json:"request_id"`
	UserID     string    `json:"user_id,omitempty"`
	Upstream   string    `json:"upstream,omitempty"`
}

type statusClasses struct {
	Status2xx int64 `json:"2xx"`
	Status3xx int64 `json:"3xx"`
	Status4xx int64 `json:"4xx"`
	Status5xx int64 `json:"5xx"`
}

type routeStat struct {
	Route  string  `json:"route"`
	Count  int64   `json:"count"`
	Errors int64   `json:"errors"`
	AvgMS  float64 `json:"avg_ms"`

	totalMS float64
}

// StatsSnapshot 是 /api/stats 的 JSON 结构。
type StatsSnapshot struct {
	StartedAt     time.Time       `json:"started_at"`
	UptimeSeconds float64         `json:"uptime_seconds"`
	InFlight      int64           `json:"in_flight"`
	TotalRequests int64           `json:"total_requests"`
	StatusClasses statusClasses   `json:"status_classes"`
	RPS           float64         `json:"rps"`
	AvgLatencyMS  float64         `json:"avg_latency_ms"`
	P95LatencyMS  float64         `json:"p95_latency_ms"`
	Routes        []routeStat     `json:"routes"`
	Recent        []requestRecord `json:"recent"`
}

type statsStore struct {
	mu sync.Mutex

	startedAt     time.Time
	inFlight      int64
	total         int64
	statusClasses statusClasses
	latencies     []float64
	recent        []requestRecord
	routes        map[string]*routeStat

	rpsBuckets [60]int64
	lastSecond int64
}

func newStatsStore() *statsStore {
	return &statsStore{
		startedAt:  time.Now(),
		latencies:  make([]float64, 0, statsLatencyLimit),
		recent:     make([]requestRecord, 0, statsRecentLimit),
		routes:     make(map[string]*routeStat),
		lastSecond: time.Now().Unix(),
	}
}

func (s *statsStore) begin() {
	s.mu.Lock()
	s.inFlight++
	s.mu.Unlock()
}

func (s *statsStore) finish(record requestRecord) {
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.inFlight--
	if s.inFlight < 0 {
		s.inFlight = 0
	}

	s.total++

	switch {
	case record.Status >= 200 && record.Status < 300:
		s.statusClasses.Status2xx++
	case record.Status >= 300 && record.Status < 400:
		s.statusClasses.Status3xx++
	case record.Status >= 400 && record.Status < 500:
		s.statusClasses.Status4xx++
	case record.Status >= 500:
		s.statusClasses.Status5xx++
	}

	s.latencies = append(s.latencies, record.DurationMS)
	if len(s.latencies) > statsLatencyLimit {
		s.latencies = s.latencies[len(s.latencies)-statsLatencyLimit:]
	}

	s.recent = append(s.recent, record)
	if len(s.recent) > statsRecentLimit {
		s.recent = s.recent[len(s.recent)-statsRecentLimit:]
	}

	stat, ok := s.routes[record.Route]
	if !ok {
		stat = &routeStat{Route: record.Route}
		s.routes[record.Route] = stat
	}

	stat.Count++
	stat.totalMS += record.DurationMS
	if record.Status >= 400 {
		stat.Errors++
	}

	nowSecond := now.Unix()
	if nowSecond != s.lastSecond {
		delta := nowSecond - s.lastSecond
		if delta >= 60 {
			for i := range s.rpsBuckets {
				s.rpsBuckets[i] = 0
			}
		} else {
			for i := int64(1); i <= delta; i++ {
				s.rpsBuckets[(s.lastSecond+i)%60] = 0
			}
		}
		s.lastSecond = nowSecond
	}
	s.rpsBuckets[nowSecond%60]++
}

func (s *statsStore) snapshot() StatsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot := StatsSnapshot{
		StartedAt:     s.startedAt,
		UptimeSeconds: round2(time.Since(s.startedAt).Seconds()),
		InFlight:      s.inFlight,
		TotalRequests: s.total,
		StatusClasses: s.statusClasses,
		Routes:        make([]routeStat, 0, len(s.routes)),
		Recent:        append([]requestRecord(nil), s.recent...),
	}

	for _, stat := range s.routes {
		item := *stat
		if item.Count > 0 {
			item.AvgMS = round2(item.totalMS / float64(item.Count))
		}
		snapshot.Routes = append(snapshot.Routes, item)
	}

	sort.Slice(snapshot.Routes, func(i, j int) bool {
		return snapshot.Routes[i].Count > snapshot.Routes[j].Count
	})

	if len(s.latencies) > 0 {
		var total float64
		for _, latency := range s.latencies {
			total += latency
		}

		snapshot.AvgLatencyMS = round2(total / float64(len(s.latencies)))
		snapshot.P95LatencyMS = round2(percentile(s.latencies, 95))
	}

	var bucketSum int64
	for _, count := range s.rpsBuckets {
		bucketSum += count
	}

	window := math.Min(60, math.Max(1, time.Since(s.startedAt).Seconds()))
	snapshot.RPS = round2(float64(bucketSum) / window)

	return snapshot
}

func (s *statsStore) inFlightCount() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inFlight
}

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
