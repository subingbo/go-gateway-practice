// Package dataplane 是 sidecar 与 edge 共用的网格数据面。
//
// 它负责：按 VirtualService 匹配请求、故障注入、按权重选择子集、
// 从快照中过滤实例、负载均衡、重试、mTLS 发送以及离群摘除。
package dataplane

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"example.com/go-gateway-reference/internal/httpx"
	"example.com/go-gateway-reference/internal/meshapi"
	"example.com/go-gateway-reference/internal/tlsutil"
	"example.com/go-gateway-reference/internal/upstream"
	"example.com/go-gateway-reference/mesh/certs"
	"example.com/go-gateway-reference/mesh/transport"
)

// SnapshotProvider 返回当前配置快照。
type SnapshotProvider func() *meshapi.Snapshot

type clusterEntry struct {
	cluster *upstream.Cluster
	host    string
	subset  string
	policy  meshapi.TrafficPolicy
}

type transportEntry struct {
	transport      *http.Transport
	maxConnections int
}

// Router 执行网格出站流量。
type Router struct {
	source    string
	namespace string
	snapshot  SnapshotProvider
	certs     *certs.Manager
	roots     *tlsutil.RootPool
	logger    *slog.Logger
	metrics   *Metrics

	mu          sync.Mutex
	clusters    map[string]*clusterEntry
	transports  map[string]*transportEntry
	trustBundle string
}

// NewRouter 创建数据面路由器。
func NewRouter(
	source string,
	namespace string,
	snapshot SnapshotProvider,
	certManager *certs.Manager,
	roots *tlsutil.RootPool,
	logger *slog.Logger,
	meshMetrics *Metrics,
) *Router {
	return &Router{
		source:     source,
		namespace:  namespace,
		snapshot:   snapshot,
		certs:      certManager,
		roots:      roots,
		logger:     logger,
		metrics:    meshMetrics,
		clusters:   make(map[string]*clusterEntry),
		transports: make(map[string]*transportEntry),
	}
}

// Close 关闭空闲连接。
func (r *Router) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, entry := range r.transports {
		entry.transport.CloseIdleConnections()
	}
}

// Do 把请求发往 host 对应的服务。
//
// host 是逻辑服务名（例如 user-service）；请求路径、查询参数、方法和
// 请求体会原样转发。返回的响应由调用方负责关闭。
func (r *Router) Do(ctx context.Context, req *http.Request, host string) (*http.Response, error) {
	start := time.Now()

	snapshot := r.snapshot()
	if snapshot == nil {
		response := syntheticResponse(http.StatusServiceUnavailable, "mesh snapshot is not ready")
		r.record(host, response.StatusCode, start)
		return response, nil
	}

	vs := snapshot.FindVirtualService(host)
	dr := snapshot.FindDestinationRule(host)

	policy := meshapi.TrafficPolicy{}
	if dr != nil {
		policy = dr.Spec.TrafficPolicy
	}

	var route *meshapi.HTTPRoute

	if vs != nil {
		route = matchHTTPRoute(vs.Spec.HTTP, req)
		if route == nil {
			response := syntheticResponse(http.StatusNotFound, "no matching mesh route")
			r.record(host, response.StatusCode, start)
			return response, nil
		}
	}

	destinations := []meshapi.DestinationWeight{{
		Destination: meshapi.Destination{Host: host},
		Weight:      100,
	}}

	var timeout time.Duration
	var fault *meshapi.Fault
	retryPolicy := upstream.RetryPolicy{}

	if route != nil {
		if len(route.Route) > 0 {
			destinations = route.Route
		}
		timeout = route.Timeout
		fault = route.Fault

		if route.Retries != nil {
			retryPolicy = upstream.RetryPolicy{
				Attempts:      route.Retries.Attempts,
				PerTryTimeout: route.Retries.PerTryTimeout,
				On:            route.Retries.RetryOn,
			}
		}
	}

	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	var body []byte
	if retryPolicy.Attempts > 0 && req.Body != nil && req.Body != http.NoBody {
		buffered, replayable, err := upstream.BufferBody(req, upstream.MaxReplayBody)
		if err != nil {
			response := syntheticResponse(http.StatusBadRequest, "read request body failed")
			r.record(host, response.StatusCode, start)
			return response, nil
		}
		if replayable {
			body = buffered
		}
	}

	destination := pickDestination(destinations, host)
	if destination.Destination.Host == "" {
		destination.Destination.Host = host
	}

	entry := r.clusterFor(destination.Destination.Host, destination.Destination.Subset)
	if entry == nil {
		response := syntheticResponse(http.StatusServiceUnavailable, "no mesh cluster for destination")
		r.record(host, response.StatusCode, start)
		return response, nil
	}

	response, attempts, err := upstream.DoWithRetry(
		ctx,
		entry.cluster,
		req,
		retryPolicy,
		body,
		func(attemptCtx context.Context, endpoint *upstream.Endpoint, outReq *http.Request) upstream.SendResult {
			faultResponse, faultErr := applyFault(attemptCtx, fault)
			if faultErr != nil {
				return upstream.SendResult{Err: faultErr, SkipReport: true}
			}
			if faultResponse != nil {
				return upstream.SendResult{Response: faultResponse, SkipReport: true}
			}

			sent, sendErr := r.send(
				attemptCtx,
				endpoint,
				outReq,
				destination.Destination.Host,
				policy,
			)
			return upstream.SendResult{Response: sent, Err: sendErr}
		},
	)

	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, err
		}

		status := http.StatusBadGateway
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			status = http.StatusGatewayTimeout
		case errors.Is(err, upstream.ErrNoHealthyEndpoint):
			status = http.StatusServiceUnavailable
		case errors.Is(err, upstream.ErrTooManyPendingRequests):
			status = http.StatusServiceUnavailable
		}

		response = syntheticResponse(status, err.Error())
	}

	if attempts > 1 {
		r.metrics.IncRetry(r.source, host)
	}

	r.record(host, response.StatusCode, start)
	return response, nil
}

// Endpoints 返回某个目标服务/子集的实例状态。
func (r *Router) Endpoints(host, subset string) []upstream.EndpointStatus {
	entry := r.clusterFor(host, subset)
	if entry == nil {
		return nil
	}

	statuses := entry.cluster.Endpoints()
	for i := range statuses {
		statuses[i].Subset = entry.subset
	}

	return statuses
}

func (r *Router) record(destination string, status int, start time.Time) {
	r.metrics.Observe("outbound", r.source, destination, status, time.Since(start))
}

func (r *Router) send(
	ctx context.Context,
	endpoint *upstream.Endpoint,
	req *http.Request,
	host string,
	policy meshapi.TrafficPolicy,
) (*http.Response, error) {
	clientTransport, err := r.transportFor(host, policy)
	if err != nil {
		return nil, err
	}

	outReq := req.Clone(ctx)
	outReq.URL.Scheme = "https"
	outReq.URL.Host = endpoint.Addr
	outReq.Host = host
	outReq.RequestURI = ""

	httpx.RemoveHopHeaders(outReq.Header)
	outReq.Header.Set("X-Forwarded-Host", host)
	outReq.Header.Set("X-Forwarded-Proto", "https")
	outReq.Header.Set(httpx.RequestIDHeader, httpx.RequestID(req))
	outReq.Header.Set("X-Mesh-Destination", host)
	httpx.AppendForwardedFor(outReq.Header, httpx.ClientIP(req))

	client := &http.Client{
		Transport: clientTransport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	return client.Do(outReq)
}

func (r *Router) transportFor(host string, policy meshapi.TrafficPolicy) (*http.Transport, error) {
	maxConnections := policy.ConnectionPool.MaxConnections

	r.mu.Lock()
	defer r.mu.Unlock()

	entry, ok := r.transports[host]
	if ok && entry.maxConnections == maxConnections {
		return entry.transport, nil
	}

	if ok {
		entry.transport.CloseIdleConnections()
	}

	expectedSpiffeID := tlsutil.SpiffeID(r.namespace, host)

	created, err := transport.NewClientTransport(
		r.certs,
		r.roots,
		expectedSpiffeID,
		transport.ClientOptions{MaxConnections: maxConnections},
	)
	if err != nil {
		return nil, err
	}

	r.transports[host] = &transportEntry{
		transport:      created,
		maxConnections: maxConnections,
	}

	return created, nil
}

func matchHTTPRoute(routes []meshapi.HTTPRoute, req *http.Request) *meshapi.HTTPRoute {
	for i := range routes {
		route := &routes[i]

		if len(route.Match) == 0 {
			return route
		}

		for _, match := range route.Match {
			if matchRequest(match, req) {
				return route
			}
		}
	}

	return nil
}

func matchRequest(match meshapi.HTTPMatch, req *http.Request) bool {
	if match.PathPrefix != "" && !strings.HasPrefix(req.URL.Path, match.PathPrefix) {
		return false
	}

	if match.Method != "" && !strings.EqualFold(match.Method, req.Method) {
		return false
	}

	for key, want := range match.Headers {
		if req.Header.Get(key) != want {
			return false
		}
	}

	return true
}

func pickDestination(destinations []meshapi.DestinationWeight, host string) meshapi.DestinationWeight {
	total := 0
	for _, destination := range destinations {
		if destination.Weight > 0 {
			total += destination.Weight
		}
	}

	if total <= 0 {
		return meshapi.DestinationWeight{
			Destination: meshapi.Destination{Host: host},
			Weight:      100,
		}
	}

	pick := rand.IntN(total)

	for _, destination := range destinations {
		if destination.Weight <= 0 {
			continue
		}

		pick -= destination.Weight
		if pick < 0 {
			return destination
		}
	}

	return destinations[len(destinations)-1]
}

func applyFault(ctx context.Context, fault *meshapi.Fault) (*http.Response, error) {
	if fault == nil {
		return nil, nil
	}

	if fault.Abort != nil && percentageHit(fault.Abort.Percentage) {
		status := fault.Abort.HTTPStatus
		if status == 0 {
			status = http.StatusInternalServerError
		}

		response := syntheticResponse(status, "fault injected")
		response.Header.Set("X-Mesh-Fault", "abort")
		return response, nil
	}

	if fault.Delay != nil && percentageHit(fault.Delay.Percentage) {
		timer := time.NewTimer(fault.Delay.FixedDelay)
		defer timer.Stop()

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}

	return nil, nil
}

func percentageHit(percentage int) bool {
	if percentage <= 0 {
		return false
	}
	if percentage >= 100 {
		return true
	}
	return rand.IntN(100) < percentage
}

func syntheticResponse(status int, message string) *http.Response {
	body := []byte(fmt.Sprintf(`{"error":%q}`, message))

	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header: http.Header{
			"Content-Type":  {"application/json; charset=utf-8"},
			"X-Mesh-Error":  {"true"},
			"Cache-Control": {"no-store"},
		},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}
