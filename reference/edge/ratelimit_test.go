package main

import (
	"testing"

	"example.com/go-gateway-reference/internal/config"
)

func TestLimitForUsesLongestPrefix(t *testing.T) {
	limiter := newRateLimiter(config.RateLimitConfig{
		Default: config.Limit{RPS: 50, Burst: 100},
		Routes: map[string]config.Limit{
			"/api/":        {RPS: 5, Burst: 10},
			"/api/orders/": {RPS: 10, Burst: 20},
		},
	})

	route := config.RouteConfig{
		Name:  "orders",
		Match: config.MatchConfig{PathPrefix: "/api/orders/"},
	}

	limit := limiter.LimitFor(route)
	if limit.RPS != 10 || limit.Burst != 20 {
		t.Fatalf("limit = %+v, want 10/20", limit)
	}
}

func TestAllowConsumesBurst(t *testing.T) {
	limiter := newRateLimiter(config.RateLimitConfig{})
	limit := config.Limit{RPS: 1, Burst: 3}

	for i := 0; i < 3; i++ {
		if !limiter.Allow("key", limit) {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}

	if limiter.Allow("key", limit) {
		t.Fatal("fourth request should be rejected")
	}
}
