// Package certs 负责从控制面获取并轮换 sidecar 的短生命周期证书。
package certs

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Manager 保存当前证书，并在到期前自动续期。
type Manager struct {
	controlPlaneURL string
	spiffeID        string
	ttl             time.Duration
	logger          *slog.Logger
	http            *http.Client

	current  atomic.Pointer[tls.Certificate]
	notAfter atomic.Int64
	serial   atomic.Value // string

	fetchMu sync.Mutex
}

type issueResponse struct {
	SpiffeID       string    `json:"spiffe_id"`
	CertificatePEM string    `json:"certificate_pem"`
	PrivateKeyPEM  string    `json:"private_key_pem"`
	RootCAPEM      string    `json:"root_ca_pem"`
	Serial         string    `json:"serial"`
	ExpiresAt      time.Time `json:"expires_at"`
}

// New 创建证书管理器。
func New(controlPlaneURL, spiffeID string, ttl time.Duration, logger *slog.Logger) *Manager {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}

	manager := &Manager{
		controlPlaneURL: strings.TrimRight(controlPlaneURL, "/"),
		spiffeID:        spiffeID,
		ttl:             ttl,
		logger:          logger,
		http: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
	manager.serial.Store("")

	return manager
}

// Identity 返回本 sidecar 的 SPIFFE 身份。
func (m *Manager) Identity() string {
	return m.spiffeID
}

// Start 获取首张证书，然后启动续期循环。
func (m *Manager) Start(ctx context.Context) error {
	backoff := 500 * time.Millisecond

	for {
		if err := m.Fetch(ctx); err == nil {
			break
		} else if ctx.Err() != nil {
			return ctx.Err()
		} else {
			m.logger.Warn("initial certificate fetch failed; retrying", "error", err, "backoff", backoff)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}

		if backoff < 5*time.Second {
			backoff *= 2
		}
	}

	go m.renewLoop(ctx)
	return nil
}

// Fetch 向控制面申请一张新证书并原子替换当前证书。
func (m *Manager) Fetch(ctx context.Context) error {
	m.fetchMu.Lock()
	defer m.fetchMu.Unlock()

	body, err := json.Marshal(map[string]any{
		"spiffe_id":   m.spiffeID,
		"ttl_seconds": int(m.ttl.Seconds()),
	})
	if err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		m.controlPlaneURL+"/v1/certs/issue",
		bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := m.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("certificate endpoint returned %s", response.Status)
	}

	var issued issueResponse
	if err := json.NewDecoder(response.Body).Decode(&issued); err != nil {
		return fmt.Errorf("decode certificate response: %w", err)
	}

	certificate, err := tls.X509KeyPair(
		[]byte(issued.CertificatePEM),
		[]byte(issued.PrivateKeyPEM),
	)
	if err != nil {
		return fmt.Errorf("load certificate: %w", err)
	}

	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return fmt.Errorf("parse leaf certificate: %w", err)
	}
	certificate.Leaf = leaf

	m.current.Store(&certificate)
	m.notAfter.Store(leaf.NotAfter.UnixNano())
	m.serial.Store(issued.Serial)

	m.logger.Info("certificate rotated",
		"spiffe_id", m.spiffeID,
		"serial", issued.Serial,
		"expires_at", leaf.NotAfter,
	)

	return nil
}

// Certificate 返回当前证书。
func (m *Manager) Certificate() *tls.Certificate {
	return m.current.Load()
}

// NotAfter 返回当前证书到期时间。
func (m *Manager) NotAfter() time.Time {
	nanos := m.notAfter.Load()
	if nanos == 0 {
		return time.Time{}
	}
	return time.Unix(0, nanos)
}

// ExpiresInSeconds 返回证书剩余有效秒数，供指标使用。
func (m *Manager) ExpiresInSeconds() float64 {
	notAfter := m.NotAfter()
	if notAfter.IsZero() {
		return 0
	}
	return time.Until(notAfter).Seconds()
}

// Serial 返回当前证书序列号。
func (m *Manager) Serial() string {
	value, _ := m.serial.Load().(string)
	return value
}

// GetCertificate 供 tls.Config 使用。
func (m *Manager) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	certificate := m.Certificate()
	if certificate == nil {
		return nil, errors.New("certificate is not ready")
	}
	return certificate, nil
}

// GetClientCertificate 供 tls.Config 使用。
func (m *Manager) GetClientCertificate(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	certificate := m.Certificate()
	if certificate == nil {
		return nil, errors.New("certificate is not ready")
	}
	return certificate, nil
}

func (m *Manager) renewLoop(ctx context.Context) {
	interval := m.ttl / 6
	if interval < time.Second {
		interval = time.Second
	}
	if interval > 30*time.Second {
		interval = 30 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			notAfter := m.NotAfter()
			if notAfter.IsZero() || time.Until(notAfter) > m.ttl/3 {
				continue
			}

			renewCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := m.Fetch(renewCtx)
			cancel()

			if err != nil {
				m.logger.Warn("certificate renewal failed", "error", err)
			}
		}
	}
}
