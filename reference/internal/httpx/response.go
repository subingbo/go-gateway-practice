package httpx

import (
	"encoding/json"
	"net/http"
)

// StatusRecorder 记录 Handler 写出的状态码和字节数。
//
// Unwrap 让 http.ResponseController 能拿到原始 ResponseWriter，
// 否则 SSE / Hijack 等能力会在包装后失效。
type StatusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

// WriteHeader 只接受第一次写入的状态码。
func (r *StatusRecorder) WriteHeader(code int) {
	if r.status != 0 {
		return
	}

	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Write 在未显式写头时把状态码记为 200。
func (r *StatusRecorder) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}

	n, err := r.ResponseWriter.Write(data)
	r.bytes += int64(n)
	return n, err
}

// Status 返回最终状态码；没有写入时按 200 处理。
func (r *StatusRecorder) Status() int {
	if r.status == 0 {
		return http.StatusOK
	}

	return r.status
}

// Bytes 返回响应体字节数。
func (r *StatusRecorder) Bytes() int64 {
	return r.bytes
}

// Unwrap 暴露底层 ResponseWriter。
func (r *StatusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// WriteJSON 输出 JSON 响应。
func WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// ErrorJSON 输出统一格式的错误响应。
func ErrorJSON(w http.ResponseWriter, status int, message string) {
	WriteJSON(w, status, map[string]string{"error": message})
}
