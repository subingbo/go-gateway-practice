// Package tlsutil 处理 SPIFFE 身份与证书链校验。
//
// 网格里的每个 sidecar 都持有一张由控制面 CA 签发的短生命周期证书，
// URI SAN 形如 spiffe://cluster.local/ns/default/sa/user-service。
// 入站和出站都通过这个身份做 mTLS 校验。
package tlsutil

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
)

// TrustDomain 是演示集群的 SPIFFE trust domain。
const TrustDomain = "cluster.local"

// SpiffeID 构造服务身份。
func SpiffeID(namespace, service string) string {
	return fmt.Sprintf("spiffe://%s/ns/%s/sa/%s", TrustDomain, namespace, service)
}

// ParseSpiffeID 返回证书中的第一个 SPIFFE URI SAN。
func ParseSpiffeID(cert *x509.Certificate) (string, error) {
	if cert == nil {
		return "", errors.New("certificate is nil")
	}

	for _, uri := range cert.URIs {
		if uri != nil && uri.Scheme == "spiffe" {
			return uri.String(), nil
		}
	}

	return "", errors.New("certificate has no SPIFFE URI SAN")
}

// ServiceFromSpiffeID 从身份中解析 service 名称。
func ServiceFromSpiffeID(id string) (string, bool) {
	parsed, err := url.Parse(id)
	if err != nil || parsed.Scheme != "spiffe" {
		return "", false
	}

	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "sa" {
			return parts[i+1], true
		}
	}

	return "", false
}

// NamespaceFromSpiffeID 从身份中解析 namespace。
func NamespaceFromSpiffeID(id string) (string, bool) {
	parsed, err := url.Parse(id)
	if err != nil || parsed.Scheme != "spiffe" {
		return "", false
	}

	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "ns" {
			return parts[i+1], true
		}
	}

	return "", false
}

// VerifyCertificate 校验证书链、用途以及可选的 SPIFFE 身份。
func VerifyCertificate(
	cert *x509.Certificate,
	roots *x509.CertPool,
	usage x509.ExtKeyUsage,
	expectedSpiffeID string,
) error {
	if cert == nil {
		return errors.New("missing certificate")
	}
	if roots == nil {
		return errors.New("missing root CA pool")
	}

	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{usage},
	}); err != nil {
		return fmt.Errorf("verify certificate chain: %w", err)
	}

	if expectedSpiffeID != "" {
		actual, err := ParseSpiffeID(cert)
		if err != nil {
			return err
		}

		if actual != expectedSpiffeID {
			return fmt.Errorf("unexpected peer identity %q, want %q", actual, expectedSpiffeID)
		}
	}

	return nil
}

// ParseCertificatePEM 解析一张 PEM 证书。
func ParseCertificatePEM(data []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("invalid certificate PEM")
	}

	return x509.ParseCertificate(block.Bytes)
}

// ParsePrivateKeyPEM 解析 PKCS#8 或 EC 私钥。
func ParsePrivateKeyPEM(data []byte) (any, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("invalid private key PEM")
	}

	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		return key, nil
	}

	return x509.ParseECPrivateKey(block.Bytes)
}

// EncodeCertPEM 把证书编码成 PEM。
func EncodeCertPEM(der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// EncodeKeyPEM 把 PKCS#8 私钥编码成 PEM。
func EncodeKeyPEM(der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// RootPool 是可在运行时替换的根证书池。
//
// 控制面推送新的 trust bundle 时，sidecar 只需要 SetPEM，不需要重启
// TLS 监听。
type RootPool struct {
	current atomic.Pointer[x509.CertPool]
}

// NewRootPool 从 PEM 创建根证书池。
func NewRootPool(pemData string) (*RootPool, error) {
	pool := &RootPool{}
	if err := pool.SetPEM(pemData); err != nil {
		return nil, err
	}
	return pool, nil
}

// SetPEM 替换根证书池。
func (p *RootPool) SetPEM(pemData string) error {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(pemData)) {
		return errors.New("no certificates found in trust bundle")
	}

	p.current.Store(pool)
	return nil
}

// Get 返回当前根证书池。
func (p *RootPool) Get() *x509.CertPool {
	if p == nil {
		return nil
	}

	return p.current.Load()
}
