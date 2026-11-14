package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"example.com/go-gateway-reference/internal/config"
	"example.com/go-gateway-reference/internal/httpx"
	"example.com/go-gateway-reference/internal/upstream"
)

// staticCluster 是配置里直接写死地址的上游集群。
type staticCluster struct {
	name        string
	cluster     *upstream.Cluster
	transport   *http.Transport
	retryPolicy upstream.RetryPolicy
	cancel      context.CancelFunc
	logger      *slog.Logger
}

func newStaticCluster(
	parent context.Context,
	route config.RouteConfig,
	logger *slog.Logger,
	onHealth func(clusterName, address string, healthy bool),
) *staticCluster {
	outlier := upstream.OutlierPolicy{
		Consecutive5xx: route.Upstream.OutlierDetection.Consecutive5xx,
		EjectionTime:   route.Upstream.OutlierDetection.EjectionTime,
	}

	cluster := upstream.NewCluster(
		route.Name,
		route.Upstream.LoadBalancer,
		outlier,
		0,
	)

	endpoints := make([]upstream.EndpointInfo, 0, len(route.Upstream.Instances))
	for _, instance := range route.Upstream.Instances {
		endpoints = append(endpoints, upstream.EndpointInfo{
			Addr:    instance.Address,
			Labels:  instance.Labels,
			Healthy: true,
		})
	}
	cluster.SetEndpoints(endpoints)

	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   3 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   false,
	}

	ctx, cancel := context.WithCancel(parent)

	checkConfig := upstream.HealthCheckConfig{
		Interval:           route.Upstream.HealthCheck.Interval,
		Timeout:            route.Upstream.HealthCheck.Timeout,
		UnhealthyThreshold: route.Upstream.HealthCheck.UnhealthyThreshold,
		HealthyThreshold:   route.Upstream.HealthCheck.HealthyThreshold,
	}

	for _, instance := range route.Upstream.Instances {
		address := instance.Address
		healthURL := "http://" + address + route.Upstream.HealthCheck.Path

		checker := upstream.NewHealthChecker(healthURL, checkConfig, nil)
		checker.SetOnResult(func(healthy bool) {
			cluster.SetEndpointHealth(address, healthy)
			if onHealth != nil {
				onHealth(route.Name, address, healthy)
			}
		})

		go checker.Run(ctx)
	}

	return &staticCluster{
		name:      route.Name,
		cluster:   cluster,
		transport: transport,
		retryPolicy: upstream.RetryPolicy{
			Attempts:      route.Upstream.Retries.Attempts,
			PerTryTimeout: route.Upstream.Retries.PerTryTimeout,
			On:            route.Upstream.Retries.On,
		},
		cancel: cancel,
		logger: logger,
	}
}

func (c *staticCluster) Do(ctx context.Context, req *http.Request) (*http.Response, int, error) {
	var body []byte

	if c.retryPolicy.Attempts > 0 && req.Body != nil && req.Body != http.NoBody {
		buffered, replayable, err := upstream.BufferBody(req, upstream.MaxReplayBody)
		if err != nil {
			return syntheticResponse(http.StatusBadRequest, "read request body failed"), 1, nil
		}
		if replayable {
			body = buffered
		}
	}

	response, attempts, err := upstream.DoWithRetry(
		ctx,
		c.cluster,
		req,
		c.retryPolicy,
		body,
		func(attemptCtx context.Context, endpoint *upstream.Endpoint, outReq *http.Request) upstream.SendResult {
			sent, sendErr := c.send(attemptCtx, endpoint, outReq)
			return upstream.SendResult{Response: sent, Err: sendErr}
		},
	)

	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, attempts, err
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

		return syntheticResponse(status, err.Error()), attempts, nil
	}

	return response, attempts, nil
}

func (c *staticCluster) send(
	ctx context.Context,
	endpoint *upstream.Endpoint,
	req *http.Request,
) (*http.Response, error) {
	outReq := req.Clone(ctx)
	outReq.URL.Scheme = "http"
	outReq.URL.Host = endpoint.Addr
	outReq.Host = req.Host
	outReq.RequestURI = ""

	httpx.RemoveHopHeaders(outReq.Header)
	outReq.Header.Set("X-Forwarded-Host", req.Host)
	outReq.Header.Set("X-Forwarded-Proto", "http")
	outReq.Header.Set(httpx.RequestIDHeader, httpx.RequestID(req))
	httpx.AppendForwardedFor(outReq.Header, httpx.ClientIP(req))

	client := &http.Client{
		Transport: c.transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	return client.Do(outReq)
}

func (c *staticCluster) Endpoints() []upstream.EndpointStatus {
	return c.cluster.Endpoints()
}

func (c *staticCluster) Close() {
	c.cancel()
	c.transport.CloseIdleConnections()
}

func syntheticResponse(status int, message string) *http.Response {
	body := []byte(fmt.Sprintf(`{"error":%q}`, message))

	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header: http.Header{
			"Content-Type":    {"application/json; charset=utf-8"},
			"X-Gateway-Error": {"true"},
			"Cache-Control":   {"no-store"},
		},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}
