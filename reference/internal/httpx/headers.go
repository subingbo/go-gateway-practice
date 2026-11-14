package httpx

import (
	"net"
	"net/http"
	"strings"
)

var hopHeaders = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

// RemoveHopHeaders 删除逐跳头，以及 Connection 中列出的扩展逐跳头。
func RemoveHopHeaders(header http.Header) {
	for _, value := range header.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			if name = strings.TrimSpace(name); name != "" {
				header.Del(name)
			}
		}
	}

	for _, name := range hopHeaders {
		header.Del(name)
	}
}

// CopyHeaders 复制请求/响应头，自动跳过逐跳头。
func CopyHeaders(dst, src http.Header) {
	for key, values := range src {
		if isHopHeader(key) {
			continue
		}

		dst.Del(key)
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func isHopHeader(name string) bool {
	for _, hop := range hopHeaders {
		if strings.EqualFold(name, hop) {
			return true
		}
	}

	return false
}

// ClientIP 返回调用方 IP，优先使用 X-Forwarded-For 的第一段。
func ClientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		if first, _, ok := strings.Cut(forwarded, ","); ok {
			return strings.TrimSpace(first)
		}

		return strings.TrimSpace(forwarded)
	}

	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}

	return r.RemoteAddr
}

// AppendForwardedFor 把当前客户端 IP 追加到 X-Forwarded-For。
func AppendForwardedFor(header http.Header, clientIP string) {
	if clientIP == "" {
		return
	}

	existing := header.Get("X-Forwarded-For")
	if existing == "" {
		header.Set("X-Forwarded-For", clientIP)
		return
	}

	header.Set("X-Forwarded-For", existing+", "+clientIP)
}
