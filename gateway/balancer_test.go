package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 创建测试 HTTP 服务
func newHealthServer(
	t *testing.T,
	handler http.HandlerFunc,
) string {

	t.Helper()

	server := httptest.NewServer(handler)

	t.Cleanup(server.Close)

	return server.URL
}

// 创建固定状态码的健康检查接口
func staticHealthHandler(statusCode int) http.HandlerFunc {

	return func(w http.ResponseWriter, r *http.Request) {

		if r.Method != http.MethodGet ||
			r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}

		w.WriteHeader(statusCode)
	}
}

// 启动测试用健康检查
func startTestHealthChecks(
	t *testing.T,
	balancer *RoundRobinBalancer,
) {

	ctx, cancel := context.WithCancel(
		context.Background(),
	)

	t.Cleanup(cancel)

	if err := balancer.StartHealthChecks(
		ctx,
		time.Hour,
		200*time.Millisecond,
	); err != nil {
		t.Fatal(err)
	}
}

// 测试轮询顺序
func TestRoundRobinBalancerSequence(t *testing.T) {

	target1 := newHealthServer(
		t,
		staticHealthHandler(http.StatusOK),
	)

	target2 := newHealthServer(
		t,
		staticHealthHandler(http.StatusOK),
	)

	targets := []string{
		target1,
		target2,
	}

	balancer, err := NewRoundRobinBalancer(targets)
	if err != nil {
		t.Fatal(err)
	}

	startTestHealthChecks(t, balancer)

	for i := 0; i < 6; i++ {

		got := balancer.Next()

		if got == nil {
			t.Fatal("expected healthy upstream")
		}

		want := targets[i%len(targets)]

		if got.String() != want {
			t.Errorf(
				"request %d: want %s, got %s",
				i+1,
				want,
				got,
			)
		}
	}
}

// 测试并发轮询
func TestRoundRobinBalancerConcurrent(t *testing.T) {

	targets := []string{
		newHealthServer(t, staticHealthHandler(http.StatusOK)),
		newHealthServer(t, staticHealthHandler(http.StatusOK)),
	}

	balancer, err := NewRoundRobinBalancer(targets)
	if err != nil {
		t.Fatal(err)
	}

	startTestHealthChecks(t, balancer)

	const totalRequests = 1000

	var wg sync.WaitGroup
	var mu sync.Mutex

	counts := make(map[string]int)

	for i := 0; i < totalRequests; i++ {

		wg.Add(1)

		go func() {
			defer wg.Done()

			target := balancer.Next()

			if target == nil {
				t.Error("expected healthy upstream")
				return
			}

			mu.Lock()
			counts[target.String()]++
			mu.Unlock()
		}()
	}

	wg.Wait()

	for _, target := range targets {

		if counts[target] != totalRequests/len(targets) {
			t.Errorf(
				"target %s: expected %d, got %d",
				target,
				totalRequests/len(targets),
				counts[target],
			)
		}
	}
}

// 测试跳过不健康实例
func TestRoundRobinBalancerSkipsUnhealthy(t *testing.T) {

	deadTarget := newHealthServer(
		t,
		staticHealthHandler(http.StatusServiceUnavailable),
	)

	liveTarget := newHealthServer(
		t,
		staticHealthHandler(http.StatusOK),
	)

	balancer, err := NewRoundRobinBalancer(
		[]string{
			deadTarget,
			liveTarget,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	startTestHealthChecks(t, balancer)

	for i := 0; i < 10; i++ {

		target := balancer.Next()

		if target == nil {
			t.Fatal("expected healthy upstream")
		}

		if target.String() != liveTarget {
			t.Errorf(
				"expected %s, got %s",
				liveTarget,
				target,
			)
		}
	}
}

// 测试所有实例都不健康
func TestRoundRobinBalancerNoHealthyUpstream(t *testing.T) {

	deadTarget := newHealthServer(
		t,
		staticHealthHandler(http.StatusServiceUnavailable),
	)

	balancer, err := NewRoundRobinBalancer(
		[]string{deadTarget},
	)
	if err != nil {
		t.Fatal(err)
	}

	startTestHealthChecks(t, balancer)

	if target := balancer.Next(); target != nil {
		t.Errorf("expected nil, got %s", target)
	}
}

// 测试 HTTP 健康检查超时
func TestRoundRobinBalancerHealthCheckTimeout(t *testing.T) {

	target := newHealthServer(
		t,
		func(w http.ResponseWriter, r *http.Request) {

			// 等待客户端取消请求，或者等待一秒
			select {
			case <-r.Context().Done():
				return

			case <-time.After(time.Second):
				w.WriteHeader(http.StatusOK)
			}
		},
	)

	balancer, err := NewRoundRobinBalancer(
		[]string{target},
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(
		context.Background(),
	)
	defer cancel()

	err = balancer.StartHealthChecks(
		ctx,
		time.Hour,
		20*time.Millisecond,
	)
	if err != nil {
		t.Fatal(err)
	}

	if got := balancer.Next(); got != nil {
		t.Errorf("expected nil because health check timed out, got %s", got)
	}
}

// 测试后端恢复后重新加入轮询
func TestRoundRobinBalancerRecovery(t *testing.T) {

	var status atomic.Int32

	status.Store(int32(http.StatusServiceUnavailable))

	target := newHealthServer(
		t,
		func(w http.ResponseWriter, r *http.Request) {

			if r.URL.Path != "/health" {
				http.NotFound(w, r)
				return
			}

			w.WriteHeader(int(status.Load()))
		},
	)

	balancer, err := NewRoundRobinBalancer(
		[]string{target},
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(
		context.Background(),
	)
	t.Cleanup(cancel)

	err = balancer.StartHealthChecks(
		ctx,
		20*time.Millisecond,
		200*time.Millisecond,
	)
	if err != nil {
		t.Fatal(err)
	}

	// 初始状态为 503，不应该进入轮询
	if got := balancer.Next(); got != nil {
		t.Fatalf("expected nil before recovery, got %s", got)
	}

	// 模拟后端恢复
	status.Store(int32(http.StatusOK))

	// 等待下一轮健康检查发现后端恢复
	deadline := time.Now().Add(time.Second)

	for time.Now().Before(deadline) {

		if got := balancer.Next(); got != nil {
			if got.String() != target {
				t.Fatalf("expected %s, got %s", target, got)
			}

			return
		}

		time.Sleep(5 * time.Millisecond)
	}

	t.Fatal("expected upstream to recover and become healthy")
}

func TestBalancerAddRemoveList(t *testing.T) {

	balancer, err := NewRoundRobinBalancer(
		[]string{
			"http://127.0.0.1:9090",
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	// 添加实例
	err = balancer.AddTarget(
		"http://127.0.0.1:9091",
	)
	if err != nil {
		t.Fatal(err)
	}

	// 查询实例
	targets := balancer.ListTargets()

	if len(targets) != 2 {
		t.Fatalf(
			"expected 2 targets, got %d",
			len(targets),
		)
	}

	// 检查重复添加
	err = balancer.AddTarget(
		"http://127.0.0.1:9091",
	)
	if err == nil {
		t.Fatal("expected duplicate target error")
	}

	// 删除实例
	err = balancer.RemoveTarget(
		"http://127.0.0.1:9090",
	)
	if err != nil {
		t.Fatal(err)
	}

	// 再次查询
	targets = balancer.ListTargets()

	if len(targets) != 1 {
		t.Fatalf(
			"expected 1 target, got %d",
			len(targets),
		)
	}

	if targets[0].Target != "http://127.0.0.1:9091" {
		t.Errorf(
			"unexpected target: %s",
			targets[0].Target,
		)
	}

	// 删除不存在的实例
	err = balancer.RemoveTarget(
		"http://127.0.0.1:9999",
	)
	if err == nil {
		t.Fatal("expected target not found error")
	}
}

func TestBalancerListTargetsReturnsCopy(t *testing.T) {

	balancer, err := NewRoundRobinBalancer(
		[]string{
			"http://127.0.0.1:9090",
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	targets := balancer.ListTargets()

	// 修改返回结果
	targets[0].Target = "http://127.0.0.1:9999"

	// 再次查询
	current := balancer.ListTargets()

	if current[0].Target != "http://127.0.0.1:9090" {
		t.Fatal("external modification affected internal targets")
	}
}

func TestBalancerConcurrentAddRemoveAndNext(t *testing.T) {

	balancer, err := NewRoundRobinBalancer(
		[]string{
			"http://127.0.0.1:9090",
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	// 让初始实例处于健康状态
	balancer.targets[0].healthy.Store(true)

	const operations = 500

	var wg sync.WaitGroup

	// Goroutine 1：持续选择后端
	wg.Add(1)

	go func() {
		defer wg.Done()

		for i := 0; i < operations; i++ {
			balancer.Next()
		}
	}()

	// Goroutine 2：持续添加和删除实例
	wg.Add(1)

	go func() {
		defer wg.Done()

		for i := 0; i < operations; i++ {

			target := fmt.Sprintf(
				"http://127.0.0.1:%d",
				10000+i,
			)

			if err := balancer.AddTarget(target); err != nil {
				t.Errorf("AddTarget failed: %v", err)
				continue
			}

			if err := balancer.RemoveTarget(target); err != nil {
				t.Errorf("RemoveTarget failed: %v", err)
			}
		}
	}()

	// Goroutine 3：持续读取实例列表
	wg.Add(1)

	go func() {
		defer wg.Done()

		for i := 0; i < operations; i++ {
			balancer.ListTargets()
		}
	}()

	wg.Wait()
}
