package metrics

import (
	"bytes"
	"strings"
	"testing"
)

func TestRegistryWritesPrometheusText(t *testing.T) {
	registry := NewRegistry()

	counter := registry.Counter("demo_requests_total", "demo counter", "route", "code")
	counter.Inc("users", "200")
	counter.Inc("users", "200")
	counter.Inc("orders", "500")

	gauge := registry.Gauge("demo_in_flight", "demo gauge", "route")
	gauge.Set(3, "users")

	histogram := registry.Histogram("demo_duration_seconds", "demo histogram",
		[]float64{0.1, 0.5}, "route")
	histogram.Observe(0.05, "users")
	histogram.Observe(0.3, "users")

	var buf bytes.Buffer
	if err := registry.WritePrometheus(&buf); err != nil {
		t.Fatal(err)
	}

	text := buf.String()

	for _, want := range []string{
		`# TYPE demo_requests_total counter`,
		`demo_requests_total{code="200",route="users"} 2`,
		`demo_requests_total{code="500",route="orders"} 1`,
		`demo_in_flight{route="users"} 3`,
		`demo_duration_seconds_bucket{le="0.1",route="users"} 1`,
		`demo_duration_seconds_bucket{le="0.5",route="users"} 2`,
		`demo_duration_seconds_bucket{le="+Inf",route="users"} 2`,
		`demo_duration_seconds_count{route="users"} 2`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in output:\n%s", want, text)
		}
	}
}
