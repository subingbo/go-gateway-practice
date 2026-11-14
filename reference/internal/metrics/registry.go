// Package metrics 是一个很小的 Prometheus 指标实现。
//
// 只支持 Counter / Gauge / Histogram 三种类型，目的是在不引入
// prometheus/client_golang 的前提下把指标语义讲清楚。
package metrics

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Labels 是标签名到标签值的映射。
type Labels map[string]string

type collector interface {
	writePrometheus(w io.Writer)
}

// Registry 保存所有指标。
type Registry struct {
	mu         sync.RWMutex
	collectors []collector
	names      map[string]struct{}
}

// NewRegistry 创建一个空注册表。
func NewRegistry() *Registry {
	return &Registry{names: make(map[string]struct{})}
}

// Counter 注册一个单调递增计数器。
func (r *Registry) Counter(name, help string, labelNames ...string) *CounterVec {
	r.register(name)
	vec := &CounterVec{
		name:       name,
		help:       help,
		labelNames: labelNames,
		values:     make(map[string]*counterValue),
	}
	r.collectors = append(r.collectors, vec)
	return vec
}

// Gauge 注册一个可增可减的瞬时值。
func (r *Registry) Gauge(name, help string, labelNames ...string) *GaugeVec {
	r.register(name)
	vec := &GaugeVec{
		name:       name,
		help:       help,
		labelNames: labelNames,
		values:     make(map[string]*gaugeValue),
	}
	r.collectors = append(r.collectors, vec)
	return vec
}

// Histogram 注册一个直方图。
func (r *Registry) Histogram(name, help string, buckets []float64, labelNames ...string) *HistogramVec {
	r.register(name)
	sorted := append([]float64(nil), buckets...)
	sort.Float64s(sorted)

	vec := &HistogramVec{
		name:       name,
		help:       help,
		buckets:    sorted,
		labelNames: labelNames,
		values:     make(map[string]*histogramValue),
	}
	r.collectors = append(r.collectors, vec)
	return vec
}

func (r *Registry) register(name string) {
	if name == "" {
		panic("metrics: metric name cannot be empty")
	}

	if _, exists := r.names[name]; exists {
		panic(fmt.Sprintf("metrics: duplicate metric %q", name))
	}

	r.names[name] = struct{}{}
}

// WritePrometheus 按 Prometheus 文本格式输出全部指标。
func (r *Registry) WritePrometheus(w io.Writer) error {
	r.mu.RLock()
	collectors := append([]collector(nil), r.collectors...)
	r.mu.RUnlock()

	for _, c := range collectors {
		c.writePrometheus(w)
	}

	return nil
}

// Handler 返回 /metrics 的 HTTP Handler。
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = r.WritePrometheus(w)
	})
}

// CounterVec 是带标签的计数器。
type CounterVec struct {
	name       string
	help       string
	labelNames []string

	mu     sync.Mutex
	values map[string]*counterValue
}

type counterValue struct {
	labels Labels
	value  float64
}

// Inc 给一组标签加一。
func (c *CounterVec) Inc(labelValues ...string) {
	c.Add(1, labelValues...)
}

// Add 给一组标签增加指定值。
func (c *CounterVec) Add(delta float64, labelValues ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key, labels := buildLabels(c.labelNames, labelValues)
	value, ok := c.values[key]
	if !ok {
		value = &counterValue{labels: labels}
		c.values[key] = value
	}

	value.value += delta
}

func (c *CounterVec) writePrometheus(w io.Writer) {
	c.mu.Lock()
	defer c.mu.Unlock()

	writeMeta(w, c.name, c.help, "counter")

	for _, key := range sortedKeys(c.values) {
		value := c.values[key]
		fmt.Fprintf(w, "%s%s %s\n", c.name, formatLabels(value.labels), formatFloat(value.value))
	}
}

// GaugeVec 是带标签的瞬时值。
type GaugeVec struct {
	name       string
	help       string
	labelNames []string

	mu     sync.Mutex
	values map[string]*gaugeValue
}

type gaugeValue struct {
	labels Labels
	value  float64
}

// Set 设置瞬时值。
func (g *GaugeVec) Set(value float64, labelValues ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	key, labels := buildLabels(g.labelNames, labelValues)
	item, ok := g.values[key]
	if !ok {
		item = &gaugeValue{labels: labels}
		g.values[key] = item
	}

	item.value = value
}

// Reset 清空所有标签组合，便于在每次抓取前重建动态 gauge。
func (g *GaugeVec) Reset() {
	g.mu.Lock()
	g.values = make(map[string]*gaugeValue)
	g.mu.Unlock()
}

// Add 增加瞬时值。
func (g *GaugeVec) Add(delta float64, labelValues ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	key, labels := buildLabels(g.labelNames, labelValues)
	item, ok := g.values[key]
	if !ok {
		item = &gaugeValue{labels: labels}
		g.values[key] = item
	}

	item.value += delta
}

// Inc 给瞬时值加一。
func (g *GaugeVec) Inc(labelValues ...string) {
	g.Add(1, labelValues...)
}

// Dec 给瞬时值减一。
func (g *GaugeVec) Dec(labelValues ...string) {
	g.Add(-1, labelValues...)
}

func (g *GaugeVec) writePrometheus(w io.Writer) {
	g.mu.Lock()
	defer g.mu.Unlock()

	writeMeta(w, g.name, g.help, "gauge")

	for _, key := range sortedKeys(g.values) {
		value := g.values[key]
		fmt.Fprintf(w, "%s%s %s\n", g.name, formatLabels(value.labels), formatFloat(value.value))
	}
}

// HistogramVec 是带标签的直方图。
type HistogramVec struct {
	name       string
	help       string
	buckets    []float64
	labelNames []string

	mu     sync.Mutex
	values map[string]*histogramValue
}

type histogramValue struct {
	labels  Labels
	buckets []uint64 // 累计计数
	sum     float64
	count   uint64
}

// Observe 记录一个观测值。
func (h *HistogramVec) Observe(value float64, labelValues ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	key, labels := buildLabels(h.labelNames, labelValues)
	item, ok := h.values[key]
	if !ok {
		item = &histogramValue{
			labels:  labels,
			buckets: make([]uint64, len(h.buckets)),
		}
		h.values[key] = item
	}

	item.sum += value
	item.count++

	for i, upper := range h.buckets {
		if value <= upper {
			item.buckets[i]++
		}
	}
}

func (h *HistogramVec) writePrometheus(w io.Writer) {
	h.mu.Lock()
	defer h.mu.Unlock()

	writeMeta(w, h.name, h.help, "histogram")

	for _, key := range sortedKeys(h.values) {
		value := h.values[key]

		for i, upper := range h.buckets {
			labels := cloneLabels(value.labels)
			labels["le"] = formatFloat(upper)
			fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, formatLabels(labels), value.buckets[i])
		}

		labels := cloneLabels(value.labels)
		labels["le"] = "+Inf"
		fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, formatLabels(labels), value.count)
		fmt.Fprintf(w, "%s_sum%s %s\n", h.name, formatLabels(value.labels), formatFloat(value.sum))
		fmt.Fprintf(w, "%s_count%s %d\n", h.name, formatLabels(value.labels), value.count)
	}
}

func buildLabels(names, values []string) (string, Labels) {
	if len(names) == 0 {
		return "", nil
	}

	labels := make(Labels, len(names))
	keyParts := make([]string, len(names))

	for i, name := range names {
		value := ""
		if i < len(values) {
			value = values[i]
		}

		labels[name] = value
		keyParts[i] = value
	}

	return strings.Join(keyParts, "\xff"), labels
}

func writeMeta(w io.Writer, name, help, kind string) {
	if help != "" {
		fmt.Fprintf(w, "# HELP %s %s\n", name, help)
	}
	fmt.Fprintf(w, "# TYPE %s %s\n", name, kind)
}

func formatLabels(labels Labels) string {
	if len(labels) == 0 {
		return ""
	}

	names := make([]string, 0, len(labels))
	for name := range labels {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteByte('{')

	for i, name := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(name)
		b.WriteString(`="`)
		b.WriteString(escapeLabel(labels[name]))
		b.WriteByte('"')
	}

	b.WriteByte('}')
	return b.String()
}

func escapeLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return value
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func cloneLabels(labels Labels) Labels {
	if len(labels) == 0 {
		return nil
	}

	out := make(Labels, len(labels)+1)
	for k, v := range labels {
		out[k] = v
	}
	return out
}
