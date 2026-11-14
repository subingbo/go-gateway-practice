package main

import (
	"strings"
	"sync"
	"time"

	"example.com/go-gateway-reference/internal/config"
)

type tokenBucket struct {
	tokens float64
	last   time.Time
}

// rateLimiter 是按“路由 + 身份”分桶的令牌桶限流器。
type rateLimiter struct {
	mu sync.Mutex

	buckets      map[string]*tokenBucket
	defaultLimit config.Limit
	routeLimits  map[string]config.Limit
	lastCleanup  time.Time
}

func newRateLimiter(cfg config.RateLimitConfig) *rateLimiter {
	return &rateLimiter{
		buckets:      make(map[string]*tokenBucket),
		defaultLimit: cfg.Default,
		routeLimits:  cloneLimits(cfg.Routes),
		lastCleanup:  time.Now(),
	}
}

func (l *rateLimiter) Update(cfg config.RateLimitConfig) {
	l.mu.Lock()
	l.defaultLimit = cfg.Default
	l.routeLimits = cloneLimits(cfg.Routes)
	l.mu.Unlock()
}

// LimitFor 返回路由的限流配置：路由显式配置优先，其次按最长前缀匹配。
func (l *rateLimiter) LimitFor(route config.RouteConfig) config.Limit {
	if route.RateLimit != nil {
		return *route.RateLimit
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	best := l.defaultLimit
	bestLength := -1

	for prefix, limit := range l.routeLimits {
		if strings.HasPrefix(route.Match.PathPrefix, prefix) && len(prefix) > bestLength {
			best = limit
			bestLength = len(prefix)
		}
	}

	return best
}

// Allow 消耗一个令牌。
func (l *rateLimiter) Allow(key string, limit config.Limit) bool {
	if !limit.Enabled() {
		return true
	}

	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastCleanup) > 5*time.Minute {
		for bucketKey, bucket := range l.buckets {
			if now.Sub(bucket.last) > 10*time.Minute {
				delete(l.buckets, bucketKey)
			}
		}
		l.lastCleanup = now
	}

	bucket, ok := l.buckets[key]
	if !ok {
		bucket = &tokenBucket{
			tokens: float64(limit.Burst),
			last:   now,
		}
		l.buckets[key] = bucket
	}

	elapsed := now.Sub(bucket.last).Seconds()
	if elapsed > 0 {
		bucket.tokens += elapsed * limit.RPS
		if bucket.tokens > float64(limit.Burst) {
			bucket.tokens = float64(limit.Burst)
		}
	}
	bucket.last = now

	if bucket.tokens < 1 {
		return false
	}

	bucket.tokens--
	return true
}

func cloneLimits(input map[string]config.Limit) map[string]config.Limit {
	out := make(map[string]config.Limit, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}
