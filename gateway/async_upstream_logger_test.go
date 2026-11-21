package main

import (
	"bytes"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAsyncUpstreamLoggerWritesTransitions(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := newAsyncUpstreamEventLogger(
		log.New(&output, "", 0),
	)

	if !logger.Publish(upstreamEvent{
		Kind:     upstreamEventEjected,
		Target:   "http://127.0.0.1:9090",
		Failures: 3,
		Window:   10 * time.Second,
	}) {
		t.Fatal("ejection event was not queued")
	}
	if !logger.Publish(upstreamEvent{
		Kind:             upstreamEventRecovered,
		Target:           "http://127.0.0.1:9090",
		SuccessfulProbes: 1,
	}) {
		t.Fatal("recovery event was not queued")
	}

	// Close 必须等待后台日志 Goroutine 把已排队事件全部写完。
	logger.Close()

	got := output.String()
	if !strings.Contains(got, "[WARN] upstream passively ejected target=http://127.0.0.1:9090 failures=3 window=10s") {
		t.Fatalf("missing ejection warning in output: %q", got)
	}
	if !strings.Contains(got, "[INFO] upstream recovered target=http://127.0.0.1:9090 successful_probes=1") {
		t.Fatalf("missing recovery info in output: %q", got)
	}
}

type blockingLogWriter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once

	mu     sync.Mutex
	output bytes.Buffer
}

func (w *blockingLogWriter) Write(data []byte) (int, error) {
	w.once.Do(func() {
		close(w.started)
	})
	<-w.release

	w.mu.Lock()
	defer w.mu.Unlock()
	return w.output.Write(data)
}

func (w *blockingLogWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.output.String()
}

func TestAsyncUpstreamLoggerQueuesEventsWhileWriterIsBlocked(t *testing.T) {
	t.Parallel()

	writer := &blockingLogWriter{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	logger := newAsyncUpstreamEventLogger(log.New(writer, "", 0))
	event := upstreamEvent{
		Kind:     upstreamEventEjected,
		Target:   "http://127.0.0.1:9090",
		Failures: 3,
		Window:   10 * time.Second,
	}

	if !logger.Publish(event) {
		t.Fatal("first event was not queued")
	}

	// 等后台 Goroutine 取走第一条并阻塞在真正的日志 I/O 上。
	select {
	case <-writer.started:
	case <-time.After(time.Second):
		t.Fatal("logger worker did not start writing")
	}

	for i := 0; i < 3; i++ {
		if !logger.Publish(event) {
			t.Fatalf("event %d was dropped while output I/O was blocked", i+2)
		}
	}

	close(writer.release)
	logger.Close()

	if got := strings.Count(writer.String(), "upstream passively ejected"); got != 4 {
		t.Fatalf("written ejection events = %d, want 4; output=%q", got, writer.String())
	}
}
