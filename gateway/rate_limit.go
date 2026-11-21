package main

import (
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	defaultRateLimitRPS       = 20
	defaultRateLimitBurst     = 40
	defaultRateLimitClientTTL = 10 * time.Minute
	rateLimitCleanupInterval  = time.Minute
)

type rateLimitConfig struct {
	requestsPerSecond float64
	burst             int
}

func loadRateLimitConfig(
	getenv func(string) string,
) (rateLimitConfig, error) {
	if getenv == nil {
		return rateLimitConfig{}, errors.New("getenv is required")
	}

	config := rateLimitConfig{
		requestsPerSecond: defaultRateLimitRPS,
		burst:             defaultRateLimitBurst,
	}

	if raw := strings.TrimSpace(getenv("GATEWAY_RATE_LIMIT_RPS")); raw != "" {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return rateLimitConfig{}, fmt.Errorf(
				"GATEWAY_RATE_LIMIT_RPS must be a positive number",
			)
		}
		config.requestsPerSecond = value
	}

	if raw := strings.TrimSpace(getenv("GATEWAY_RATE_LIMIT_BURST")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			return rateLimitConfig{}, fmt.Errorf(
				"GATEWAY_RATE_LIMIT_BURST must be a positive integer",
			)
		}
		config.burst = value
	}

	return config, nil
}

type clientBucket struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// clientRateLimiter 给每个客户端 IP 维护独立令牌桶。
// 长时间不活跃的 IP 会在后续请求到来时被惰性清理，避免 map 一直增长。
type clientRateLimiter struct {
	mu sync.Mutex

	requestsPerSecond rate.Limit
	burst             int
	clientTTL         time.Duration
	lastCleanup       time.Time
	clients           map[string]*clientBucket
}

func newClientRateLimiter(
	requestsPerSecond float64,
	burst int,
	clientTTL time.Duration,
) *clientRateLimiter {
	return &clientRateLimiter{
		requestsPerSecond: rate.Limit(requestsPerSecond),
		burst:             burst,
		clientTTL:         clientTTL,
		clients:           make(map[string]*clientBucket),
	}
}

func (l *clientRateLimiter) allow(clientIP string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.lastCleanup.IsZero() || now.Sub(l.lastCleanup) >= rateLimitCleanupInterval {
		for key, bucket := range l.clients {
			if now.Sub(bucket.lastSeen) > l.clientTTL {
				delete(l.clients, key)
			}
		}
		l.lastCleanup = now
	}

	bucket := l.clients[clientIP]
	if bucket == nil {
		bucket = &clientBucket{
			limiter: rate.NewLimiter(l.requestsPerSecond, l.burst),
		}
		l.clients[clientIP] = bucket
	}

	bucket.lastSeen = now
	return bucket.limiter.AllowN(now, 1)
}

func rateLimitMiddleware(
	next http.Handler,
	limiter *clientRateLimiter,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if limiter == nil || !shouldRateLimitPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		if limiter.allow(clientIP(r.RemoteAddr), time.Now()) {
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("Retry-After", limiter.retryAfterSeconds())
		http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
	})
}

func (l *clientRateLimiter) retryAfterSeconds() string {
	seconds := int(math.Ceil(1 / float64(l.requestsPerSecond)))
	if seconds < 1 {
		seconds = 1
	}

	return strconv.Itoa(seconds)
}

func shouldRateLimitPath(path string) bool {
	return path == "/api" ||
		strings.HasPrefix(path, "/api/") ||
		path == "/auth" ||
		strings.HasPrefix(path, "/auth/")
}

func clientIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil {
		return host
	}

	return remoteAddr
}
