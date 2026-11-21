package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestReverseProxyTransportFailuresFeedPassiveEjection(t *testing.T) {
	t.Parallel()

	balancer, err := NewRoundRobinBalancer(
		[]string{"http://backend.internal"},
	)
	if err != nil {
		t.Fatal(err)
	}
	balancer.targets[0].healthy.Store(true)

	handler, err := newReverseProxy(balancer, "/api")
	if err != nil {
		t.Fatal(err)
	}
	proxyHandler := handler.(*loadBalancedReverseProxy)
	proxyHandler.proxy.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})

	for attempt := 1; attempt <= defaultPassiveFailureThreshold; attempt++ {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/users/42", nil)

		proxyHandler.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusBadGateway {
			t.Fatalf("attempt %d status = %d, want 502", attempt, recorder.Code)
		}
	}

	// 达到阈值后，新请求在选择阶段就找不到可用实例，不再进入 Transport。
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/users/42", nil)
	proxyHandler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status after ejection = %d, want 503", recorder.Code)
	}
}

func TestReverseProxySuccessfulResponseResetsPassiveFailures(t *testing.T) {
	t.Parallel()

	balancer, err := NewRoundRobinBalancer(
		[]string{"http://backend.internal"},
	)
	if err != nil {
		t.Fatal(err)
	}
	balancer.targets[0].healthy.Store(true)

	handler, err := newReverseProxy(balancer, "/api")
	if err != nil {
		t.Fatal(err)
	}
	proxyHandler := handler.(*loadBalancedReverseProxy)

	var calls atomic.Int32
	proxyHandler.proxy.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		call := calls.Add(1)
		if call != 3 {
			return nil, errors.New("temporary transport failure")
		}

		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("ok")),
			Request:    req,
		}, nil
	})

	wantStatuses := []int{
		http.StatusBadGateway,
		http.StatusBadGateway,
		http.StatusOK,
		http.StatusBadGateway,
		http.StatusBadGateway,
	}
	for index, wantStatus := range wantStatuses {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/users/42", nil)
		proxyHandler.ServeHTTP(recorder, request)

		if recorder.Code != wantStatus {
			t.Fatalf("request %d status = %d, want %d", index+1, recorder.Code, wantStatus)
		}
	}

	if got := balancer.Next(); got == nil {
		t.Fatal("successful upstream response did not reset passive failure sequence")
	}
}
