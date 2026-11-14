// Package transport 提供 sidecar 之间以及 edge → sidecar 的 mTLS 传输。
//
// 叶子证书同时带有两类身份：
//   - DNS SAN = 服务名，用于标准 TLS 主机名校验；
//   - URI SAN = spiffe://cluster.local/ns/<ns>/sa/<service>，用于 mTLS 身份校验。
//
// 这样客户端可以使用标准 RootCAs + ServerName 完成链与主机名校验，再在
// VerifyConnection 中额外核对 SPIFFE 身份，不需要关闭证书校验。
package transport

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"example.com/go-gateway-reference/internal/tlsutil"
	"example.com/go-gateway-reference/mesh/certs"
)

// ClientOptions 控制连接池大小。
type ClientOptions struct {
	MaxConnections int
}

// NewClientTransport 创建一个到目标服务的 mTLS Transport。
func NewClientTransport(
	certManager *certs.Manager,
	roots *tlsutil.RootPool,
	expectedSpiffeID string,
	options ClientOptions,
) (*http.Transport, error) {
	service, ok := tlsutil.ServiceFromSpiffeID(expectedSpiffeID)
	if !ok || service == "" {
		return nil, fmt.Errorf("invalid expected SPIFFE ID %q", expectedSpiffeID)
	}

	rootPool := roots.Get()
	if rootPool == nil {
		return nil, errors.New("trust bundle is not ready")
	}

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    rootPool,
		ServerName: service,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			certificate := certManager.Certificate()
			if certificate == nil {
				return nil, errors.New("client certificate is not ready")
			}
			return certificate, nil
		},
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("peer did not present a certificate")
			}

			actual, err := tlsutil.ParseSpiffeID(state.PeerCertificates[0])
			if err != nil {
				return fmt.Errorf("read peer SPIFFE ID: %w", err)
			}

			if expectedSpiffeID != "" && actual != expectedSpiffeID {
				return fmt.Errorf("unexpected peer identity %q, want %q", actual, expectedSpiffeID)
			}

			return nil
		},
	}

	maxIdle := 20
	if options.MaxConnections > 0 && options.MaxConnections < maxIdle {
		maxIdle = options.MaxConnections
	}

	return &http.Transport{
		TLSClientConfig:     tlsConfig,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: maxIdle,
		MaxConnsPerHost:     options.MaxConnections,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 5 * time.Second,
		DialContext: (&net.Dialer{
			Timeout:   3 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2: false,
	}, nil
}

// NewServerTLS 创建入站 TLS 配置。
//
// 使用 RequestClientCert 而不是 RequireAndVerifyClientCert，是为了在
// 握手后按 DestinationRule 的 STRICT / PERMISSIVE 动态决定是否必须
// 携带客户端证书。
func NewServerTLS(certManager *certs.Manager) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			certificate := certManager.Certificate()
			if certificate == nil {
				return nil, errors.New("server certificate is not ready")
			}
			return certificate, nil
		},
		ClientAuth: tls.RequestClientCert,
	}
}

// VerifyInboundPeer 校验入站请求的客户端身份。
//
// PERMISSIVE 允许没有客户端证书的请求（身份为 anonymous），但一旦
// 客户端出示了证书，就必须通过链校验；STRICT 则直接拒绝匿名请求。
func VerifyInboundPeer(
	state *tls.ConnectionState,
	roots *tlsutil.RootPool,
	mode string,
) (string, error) {
	if state == nil {
		return "", errors.New("TLS state is missing")
	}

	if len(state.PeerCertificates) == 0 {
		if strings.EqualFold(mode, "STRICT") {
			return "", errors.New("client certificate is required (STRICT mTLS)")
		}
		return "anonymous", nil
	}

	leaf := state.PeerCertificates[0]

	if err := tlsutil.VerifyCertificate(
		leaf,
		roots.Get(),
		x509.ExtKeyUsageClientAuth,
		"",
	); err != nil {
		return "", err
	}

	identity, err := tlsutil.ParseSpiffeID(leaf)
	if err != nil {
		return "", err
	}

	return identity, nil
}
