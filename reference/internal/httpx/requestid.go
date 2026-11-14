package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

// RequestIDHeader 是贯穿边缘网关与网格的请求 ID 头。
const RequestIDHeader = "X-Request-ID"

type requestIDKey struct{}

// RequestID 读取请求头里的 ID；缺失或格式非法时生成一个新的。
func RequestID(r *http.Request) string {
	if id := r.Header.Get(RequestIDHeader); validRequestID(id) {
		return id
	}

	return newRequestID()
}

// WithRequestID 把请求 ID 放进 context。
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestIDFromContext 读取 context 中的请求 ID。
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

func newRequestID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "unknown"
	}

	return hex.EncodeToString(buf)
}

func validRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}

	for i := 0; i < len(id); i++ {
		if id[i] < 32 || id[i] > 126 {
			return false
		}
	}

	return true
}
