package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"
)

type recordingRegistry struct {
	mu         sync.Mutex
	calls      []string
	registered chan struct{}
	record     func(string)
}

func (r *recordingRegistry) Register(serviceName string, ip string, port uint64) error {
	call := fmt.Sprintf("register %s %s %d", serviceName, ip, port)
	r.mu.Lock()
	r.calls = append(r.calls, call)
	r.mu.Unlock()
	if r.record != nil {
		r.record(call)
	}
	close(r.registered)
	return nil
}

func (r *recordingRegistry) Deregister(serviceName string, ip string, port uint64) error {
	call := fmt.Sprintf("deregister %s %s %d", serviceName, ip, port)
	r.mu.Lock()
	r.calls = append(r.calls, call)
	r.mu.Unlock()
	if r.record != nil {
		r.record(call)
	}
	return nil
}

func (r *recordingRegistry) Close() {
	r.mu.Lock()
	r.calls = append(r.calls, "close")
	r.mu.Unlock()
	if r.record != nil {
		r.record("close")
	}
}

func (r *recordingRegistry) snapshotCalls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

type recordingListener struct {
	net.Listener
	once   sync.Once
	record func(string)
}

func (l *recordingListener) Close() error {
	l.once.Do(func() {
		l.record("listener close")
	})
	return l.Listener.Close()
}

func TestLoadConfigUsesDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := loadConfig(func(string) string { return "" })
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}

	if cfg.port != 9090 {
		t.Fatalf("port = %d, want 9090", cfg.port)
	}
	if cfg.nacosHost != "127.0.0.1" {
		t.Fatalf("nacosHost = %q, want 127.0.0.1", cfg.nacosHost)
	}
	if cfg.nacosPort != 8848 {
		t.Fatalf("nacosPort = %d, want 8848", cfg.nacosPort)
	}
	if cfg.serviceIP != "127.0.0.1" {
		t.Fatalf("serviceIP = %q, want 127.0.0.1", cfg.serviceIP)
	}
}

func TestLoadConfigUsesEnvironment(t *testing.T) {
	t.Parallel()

	values := map[string]string{
		"USER_SERVICE_PORT": "19090",
		"NACOS_ADDR":        "nacos.internal:18848",
		"USER_SERVICE_IP":   "10.0.0.9",
	}

	cfg, err := loadConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}

	if cfg.port != 19090 {
		t.Fatalf("port = %d, want 19090", cfg.port)
	}
	if cfg.nacosHost != "nacos.internal" {
		t.Fatalf("nacosHost = %q, want nacos.internal", cfg.nacosHost)
	}
	if cfg.nacosPort != 18848 {
		t.Fatalf("nacosPort = %d, want 18848", cfg.nacosPort)
	}
	if cfg.serviceIP != "10.0.0.9" {
		t.Fatalf("serviceIP = %q, want 10.0.0.9", cfg.serviceIP)
	}
}

func TestLoadConfigRejectsInvalidEnvironment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		values map[string]string
	}{
		{name: "non-numeric port", values: map[string]string{"USER_SERVICE_PORT": "abc"}},
		{name: "port out of range", values: map[string]string{"USER_SERVICE_PORT": "70000"}},
		{name: "nacos address without port", values: map[string]string{"NACOS_ADDR": "127.0.0.1"}},
		{name: "nacos port out of range", values: map[string]string{"NACOS_ADDR": "127.0.0.1:70000"}},
		{name: "invalid service IP", values: map[string]string{"USER_SERVICE_IP": "not-an-ip"}},
		{name: "unspecified service IP", values: map[string]string{"USER_SERVICE_IP": "0.0.0.0"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadConfig(func(key string) string { return tt.values[key] })
			if err == nil {
				t.Fatal("loadConfig() error = nil, want an error")
			}
		})
	}
}

func TestRunUserServiceRegistersServesAndDeregisters(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}

	port := uint64(listener.Addr().(*net.TCPAddr).Port)
	var eventMu sync.Mutex
	var events []string
	record := func(event string) {
		eventMu.Lock()
		defer eventMu.Unlock()
		events = append(events, event)
	}
	recordedListener := &recordingListener{Listener: listener, record: record}
	registry := &recordingRegistry{registered: make(chan struct{}), record: record}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		done <- runUserService(ctx, serviceConfig{
			port:      port,
			serviceIP: "127.0.0.1",
		}, registry, recordedListener)
	}()

	select {
	case <-registry.registered:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("service did not register within 2 seconds")
	}

	response, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/users/42", port))
	if err != nil {
		cancel()
		t.Fatalf("GET /users/42 error = %v", err)
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil {
		cancel()
		t.Fatalf("read /users/42 response: %v", readErr)
	}
	if closeErr != nil {
		cancel()
		t.Fatalf("close /users/42 response: %v", closeErr)
	}
	if response.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("GET /users/42 status = %d, want 200", response.StatusCode)
	}
	wantBody := fmt.Sprintf(
		"Service: User\nInstance: 127.0.0.1:%d\nUser ID: 42\nMethod: GET\nPath: /users/42\n",
		port,
	)
	if string(body) != wantBody {
		cancel()
		t.Fatalf("GET /users/42 body = %q, want %q", body, wantBody)
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runUserService() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("service did not shut down within 2 seconds")
	}

	wantCalls := []string{
		fmt.Sprintf("register user-service 127.0.0.1 %d", port),
		fmt.Sprintf("deregister user-service 127.0.0.1 %d", port),
		"close",
	}
	if calls := registry.snapshotCalls(); !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("registry calls = %v, want %v", calls, wantCalls)
	}

	eventMu.Lock()
	defer eventMu.Unlock()
	wantEvents := []string{
		fmt.Sprintf("register user-service 127.0.0.1 %d", port),
		fmt.Sprintf("deregister user-service 127.0.0.1 %d", port),
		"listener close",
		"close",
	}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("shutdown events = %v, want %v", events, wantEvents)
	}
}
