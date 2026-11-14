package upstream

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRoundRobinCyclesEndpoints(t *testing.T) {
	cluster := NewCluster("test", "round_robin", OutlierPolicy{}, 0)
	cluster.SetEndpoints([]EndpointInfo{
		{Addr: "10.0.0.1:80", Healthy: true},
		{Addr: "10.0.0.2:80", Healthy: true},
		{Addr: "10.0.0.3:80", Healthy: true},
	})

	seen := map[string]int{}
	for i := 0; i < 6; i++ {
		endpoint, err := cluster.Pick()
		if err != nil {
			t.Fatal(err)
		}
		seen[endpoint.Addr]++
	}

	for addr, count := range seen {
		if count != 2 {
			t.Fatalf("%s picked %d times, want 2", addr, count)
		}
	}
}

func TestOutlierEjectsFailingEndpoint(t *testing.T) {
	cluster := NewCluster("test", "round_robin", OutlierPolicy{
		Consecutive5xx: 2,
		EjectionTime:   100 * time.Millisecond,
	}, 0)
	cluster.SetEndpoints([]EndpointInfo{
		{Addr: "bad", Healthy: true},
		{Addr: "good", Healthy: true},
	})

	cluster.mu.RLock()
	bad := cluster.endpoints[0]
	cluster.mu.RUnlock()

	cluster.Report(bad, http.StatusInternalServerError, nil)
	cluster.Report(bad, http.StatusInternalServerError, nil)

	if !bad.Ejected() {
		t.Fatal("endpoint should be ejected")
	}

	for i := 0; i < 4; i++ {
		endpoint, err := cluster.Pick()
		if err != nil {
			t.Fatal(err)
		}
		if endpoint.Addr == "bad" {
			t.Fatal("ejected endpoint should not be picked")
		}
	}
}

func TestDoWithRetryPicksAnotherEndpoint(t *testing.T) {
	cluster := NewCluster("test", "round_robin", OutlierPolicy{}, 0)
	cluster.SetEndpoints([]EndpointInfo{
		{Addr: "first", Healthy: true},
		{Addr: "second", Healthy: true},
	})

	var calls atomic.Int32

	request, _ := http.NewRequest(http.MethodGet, "http://user-service/users/1", nil)

	response, attempts, err := DoWithRetry(
		context.Background(),
		cluster,
		request,
		RetryPolicy{Attempts: 1, On: []string{"5xx"}},
		nil,
		func(_ context.Context, endpoint *Endpoint, _ *http.Request) SendResult {
			calls.Add(1)
			status := http.StatusInternalServerError
			if endpoint.Addr == "second" {
				status = http.StatusOK
			}

			return SendResult{
				Response: &http.Response{
					StatusCode: status,
					Body:       io.NopCloser(strings.NewReader("body")),
					Header:     make(http.Header),
				},
			}
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
}

func TestSetEndpointsKeepsOutlierState(t *testing.T) {
	cluster := NewCluster("test", "round_robin", OutlierPolicy{
		Consecutive5xx: 1,
		EjectionTime:   time.Minute,
	}, 0)
	cluster.SetEndpoints([]EndpointInfo{{Addr: "a", Healthy: true}})

	endpoint, _ := cluster.Pick()
	cluster.Report(endpoint, http.StatusBadGateway, nil)

	cluster.SetEndpoints([]EndpointInfo{{Addr: "a", Healthy: true}})

	statuses := cluster.Endpoints()
	if len(statuses) != 1 || !statuses[0].Ejected {
		t.Fatalf("expected ejected state to survive snapshot update: %+v", statuses)
	}
}
