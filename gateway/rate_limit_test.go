package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLoadRateLimitConfigUsesDefaultsAndEnvironmentOverrides(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		values map[string]string
		want   rateLimitConfig
	}{
		{
			name: "defaults",
			want: rateLimitConfig{
				requestsPerSecond: 20,
				burst:             40,
			},
		},
		{
			name: "environment overrides",
			values: map[string]string{
				"GATEWAY_RATE_LIMIT_RPS":   "12.5",
				"GATEWAY_RATE_LIMIT_BURST": "25",
			},
			want: rateLimitConfig{
				requestsPerSecond: 12.5,
				burst:             25,
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := loadRateLimitConfig(func(name string) string {
				return test.values[name]
			})
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("config = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestLoadRateLimitConfigRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	for _, values := range []map[string]string{
		{"GATEWAY_RATE_LIMIT_RPS": "0"},
		{"GATEWAY_RATE_LIMIT_RPS": "not-a-number"},
		{"GATEWAY_RATE_LIMIT_RPS": "NaN"},
		{"GATEWAY_RATE_LIMIT_RPS": "+Inf"},
		{"GATEWAY_RATE_LIMIT_BURST": "0"},
		{"GATEWAY_RATE_LIMIT_BURST": "1.5"},
	} {
		values := values
		t.Run("invalid", func(t *testing.T) {
			t.Parallel()

			_, err := loadRateLimitConfig(func(name string) string {
				return values[name]
			})
			if err == nil {
				t.Fatalf("loadRateLimitConfig(%v) error = nil", values)
			}
		})
	}
}

func TestRateLimitMiddlewareRejectsRequestsBeyondClientBurst(t *testing.T) {
	t.Parallel()

	limiter := newClientRateLimiter(1, 2, time.Minute)
	handler := rateLimitMiddleware(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
		limiter,
	)

	for attempt, wantStatus := range []int{
		http.StatusNoContent,
		http.StatusNoContent,
		http.StatusTooManyRequests,
	} {
		request := httptest.NewRequest(http.MethodGet, "/api/users/42", nil)
		request.RemoteAddr = "192.0.2.10:5000"
		recorder := httptest.NewRecorder()

		handler.ServeHTTP(recorder, request)

		if recorder.Code != wantStatus {
			t.Fatalf(
				"attempt %d status = %d, want %d",
				attempt+1,
				recorder.Code,
				wantStatus,
			)
		}
		if wantStatus == http.StatusTooManyRequests && recorder.Header().Get("Retry-After") != "1" {
			t.Fatalf("Retry-After = %q, want 1", recorder.Header().Get("Retry-After"))
		}
	}
}

func TestRateLimitMiddlewareKeepsIndependentBucketsPerClient(t *testing.T) {
	t.Parallel()

	limiter := newClientRateLimiter(1, 1, time.Minute)
	handler := rateLimitMiddleware(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
		limiter,
	)

	for _, remoteAddr := range []string{
		"192.0.2.10:5000",
		"192.0.2.11:5000",
	} {
		request := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
		request.RemoteAddr = remoteAddr
		recorder := httptest.NewRecorder()

		handler.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusNoContent {
			t.Fatalf("client %s status = %d, want 204", remoteAddr, recorder.Code)
		}
	}
}

func TestRateLimitMiddlewareCalculatesRetryAfterForSlowRefill(t *testing.T) {
	t.Parallel()

	limiter := newClientRateLimiter(0.5, 1, time.Minute)
	handler := rateLimitMiddleware(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
		limiter,
	)

	for attempt := 0; attempt < 2; attempt++ {
		request := httptest.NewRequest(http.MethodGet, "/api/users/42", nil)
		request.RemoteAddr = "192.0.2.10:5000"
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)

		if attempt == 1 {
			if recorder.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want 429", recorder.Code)
			}
			if got := recorder.Header().Get("Retry-After"); got != "2" {
				t.Fatalf("Retry-After = %q, want 2", got)
			}
		}
	}
}

func TestRateLimitMiddlewareBypassesHealthAndStaticPaths(t *testing.T) {
	t.Parallel()

	limiter := newClientRateLimiter(1, 1, time.Minute)
	handler := rateLimitMiddleware(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
		limiter,
	)

	for _, path := range []string{"/health", "/", "/assets/app.js"} {
		for attempt := 0; attempt < 3; attempt++ {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.RemoteAddr = "192.0.2.10:5000"
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusNoContent {
				t.Fatalf("path %s attempt %d status = %d, want 204", path, attempt+1, recorder.Code)
			}
		}
	}
}
