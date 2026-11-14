package httpx

import (
	"bytes"
	"fmt"
	"net/http"
	"sync"
)

// SSEWriter 封装 Server-Sent Events 的写入细节。
//
// 使用 http.ResponseController 而不是直接断言 http.Flusher，这样即使
// ResponseWriter 被 StatusRecorder 之类的中间件包装过，也能正确 Flush。
type SSEWriter struct {
	w     http.ResponseWriter
	flush func() error
	mu    sync.Mutex
}

// NewSSE 设置 SSE 响应头并返回写入器。
func NewSSE(w http.ResponseWriter) (*SSEWriter, error) {
	controller := http.NewResponseController(w)

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	if err := controller.Flush(); err != nil {
		return nil, err
	}

	return &SSEWriter{w: w, flush: controller.Flush}, nil
}

// Event 写一条事件。
func (s *SSEWriter) Event(event string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var buf bytes.Buffer

	if event != "" {
		fmt.Fprintf(&buf, "event: %s\n", event)
	}

	for _, line := range bytes.Split(data, []byte("\n")) {
		buf.WriteString("data: ")
		buf.Write(line)
		buf.WriteByte('\n')
	}

	buf.WriteByte('\n')

	if _, err := s.w.Write(buf.Bytes()); err != nil {
		return err
	}

	return s.flush()
}

// Comment 写一条注释，用作心跳。
func (s *SSEWriter) Comment(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := fmt.Fprintf(s.w, ": %s\n\n", text); err != nil {
		return err
	}

	return s.flush()
}
