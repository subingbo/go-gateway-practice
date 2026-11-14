// Package sdk 是业务服务访问网格的轻量客户端。
//
// 它把 http.Transport.Proxy 指向本机 sidecar 的 outbound 端口，因此
// 业务代码仍然写普通的 http://user-service/users/1，由 sidecar 负责
// 服务发现、mTLS、重试和负载均衡。
package sdk

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"example.com/go-gateway-reference/internal/httpx"
)

// Client 是网格 HTTP 客户端。
type Client struct {
	http    *http.Client
	sidecar string
}

// New 创建指向 sidecar 的客户端。
func New(sidecarAddr string) *Client {
	proxyURL := &url.URL{Scheme: "http", Host: sidecarAddr}

	transport := &http.Transport{
		Proxy:               http.ProxyURL(proxyURL),
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     90 * time.Second,
	}

	return &Client{
		http: &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		sidecar: sidecarAddr,
	}
}

// SidecarAddr 返回 sidecar 地址。
func (c *Client) SidecarAddr() string {
	return c.sidecar
}

// Do 发送请求，并确保带上 X-Request-ID。
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if req.Header.Get(httpx.RequestIDHeader) == "" {
		req.Header.Set(httpx.RequestIDHeader, httpx.RequestID(req))
	}

	return c.http.Do(req)
}

// Get 发送 GET 请求。
func (c *Client) Get(ctx context.Context, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}

	return c.Do(req)
}

// CloseIdleConnections 关闭空闲连接。
func (c *Client) CloseIdleConnections() {
	c.http.CloseIdleConnections()
}
