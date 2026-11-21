package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestReverseProxyRetriesIdempotentRequestOnDifferentUpstream(t *testing.T) {
	t.Parallel()

	balancer, err := NewRoundRobinBalancer([]string{
		"http://backend-a.internal",
		"http://backend-b.internal",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range balancer.targets {
		item.healthy.Store(true)
	}

	var mu sync.Mutex
	var hosts []string
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		hosts = append(hosts, req.URL.Host)
		mu.Unlock()

		if req.URL.Host == "backend-a.internal" {
			return nil, errors.New("connection refused")
		}

		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("backend-b")),
			Request:    req,
		}, nil
	})

	handler, err := newReverseProxyWithTransport(
		balancer,
		"/api",
		transport,
	)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/users/42", nil)
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Body.String(); got != "backend-b" {
		t.Fatalf("body = %q, want backend-b", got)
	}

	mu.Lock()
	defer mu.Unlock()
	wantHosts := []string{"backend-a.internal", "backend-b.internal"}
	if len(hosts) != len(wantHosts) {
		t.Fatalf("attempt hosts = %v, want %v", hosts, wantHosts)
	}
	for index := range wantHosts {
		if hosts[index] != wantHosts[index] {
			t.Fatalf("attempt hosts = %v, want %v", hosts, wantHosts)
		}
	}
}

func TestReverseProxyDoesNotRetryPost(t *testing.T) {
	t.Parallel()

	balancer, err := NewRoundRobinBalancer([]string{
		"http://backend-a.internal",
		"http://backend-b.internal",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range balancer.targets {
		item.healthy.Store(true)
	}

	attempts := 0
	transport := roundTripperFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		return nil, errors.New("connection refused")
	})

	handler, err := newReverseProxyWithTransport(
		balancer,
		"/api",
		transport,
	)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/users",
		strings.NewReader(`{"name":"alice"}`),
	)
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", recorder.Code)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestReverseProxyAttributesTwoFailedAttemptsToDifferentUpstreams(t *testing.T) {
	t.Parallel()

	balancer, err := NewRoundRobinBalancer([]string{
		"http://backend-a.internal",
		"http://backend-b.internal",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range balancer.targets {
		item.healthy.Store(true)
	}

	attempts := 0
	handler, err := newReverseProxyWithTransport(
		balancer,
		"/api",
		roundTripperFunc(func(*http.Request) (*http.Response, error) {
			attempts++
			return nil, errors.New("connection refused")
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/users/42", nil)
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", recorder.Code)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	for _, item := range balancer.targets {
		if got := len(item.passive.failureTimes); got != 1 {
			t.Fatalf("target %s failure count = %d, want 1", item.target, got)
		}
	}
}

func TestRetryTransportDoesNotDoubleReportWhenRequestCannotBeCloned(t *testing.T) {
	t.Parallel()

	balancer, err := NewRoundRobinBalancer([]string{
		"http://backend-a.internal",
		"http://backend-b.internal",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range balancer.targets {
		item.healthy.Store(true)
	}

	failedTarget := balancer.targets[0].target
	request, err := http.NewRequest(
		http.MethodGet,
		"http://backend-a.internal/users/42",
		strings.NewReader("body"),
	)
	if err != nil {
		t.Fatal(err)
	}
	request = request.WithContext(
		withSelectedUpstream(request.Context(), failedTarget),
	)
	request.GetBody = func() (io.ReadCloser, error) {
		return nil, errors.New("cannot replay body")
	}

	transport := newRetryingRoundTripper(
		roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection refused")
		}),
		balancer,
		balancer,
	)

	_, err = transport.RoundTrip(request)
	if err == nil {
		t.Fatal("RoundTrip() error = nil")
	}
	if got := len(balancer.targets[0].passive.failureTimes); got != 0 {
		t.Fatalf("transport reported initial failure %d times, want 0 before ErrorHandler", got)
	}
}
