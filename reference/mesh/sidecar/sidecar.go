package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"example.com/go-gateway-reference/internal/meshapi"
	"example.com/go-gateway-reference/internal/metrics"
	"example.com/go-gateway-reference/internal/tlsutil"
	"example.com/go-gateway-reference/mesh/certs"
	"example.com/go-gateway-reference/mesh/configclient"
	"example.com/go-gateway-reference/mesh/dataplane"
	"example.com/go-gateway-reference/mesh/transport"
)

// Config 是 sidecar 启动参数。
type Config struct {
	Service         string
	Namespace       string
	Version         string
	AppAddr         string
	InboundAddr     string
	OutboundAddr    string
	AdminAddr       string
	ControlPlaneURL string
	HealthPath      string
	Labels          map[string]string
	CertTTL         time.Duration
	Logger          *slog.Logger
}

// Sidecar 是服务旁的轻量代理。
type Sidecar struct {
	config     Config
	instanceID string
	identity   string
	logger     *slog.Logger

	metrics      *metrics.Registry
	meshMetrics  *dataplane.Metrics
	configClient *configclient.Client
	certManager  *certs.Manager
	roots        *tlsutil.RootPool
	router       *dataplane.Router

	registered atomic.Bool
	httpClient *http.Client

	configVersionGauge *metrics.GaugeVec
	certExpiryGauge    *metrics.GaugeVec

	inboundServer  *http.Server
	outboundServer *http.Server
	adminServer    *http.Server

	errCh chan error
}

// New 创建 sidecar。
func New(config Config) (*Sidecar, error) {
	if config.Service == "" {
		return nil, fmt.Errorf("service is required")
	}
	if config.AppAddr == "" {
		return nil, fmt.Errorf("app-addr is required")
	}
	if config.Namespace == "" {
		config.Namespace = "default"
	}
	if config.Version == "" {
		config.Version = "v1"
	}
	if config.HealthPath == "" {
		config.HealthPath = "/healthz"
	}
	if config.CertTTL <= 0 {
		config.CertTTL = 10 * time.Minute
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}

	if config.Labels == nil {
		config.Labels = make(map[string]string)
	}
	config.Labels["version"] = config.Version

	instanceID, err := buildInstanceID(config.Service, config.Version, config.InboundAddr)
	if err != nil {
		return nil, err
	}

	registry := metrics.NewRegistry()

	sidecar := &Sidecar{
		config:       config,
		instanceID:   instanceID,
		identity:     tlsutil.SpiffeID(config.Namespace, config.Service),
		logger:       config.Logger,
		metrics:      registry,
		meshMetrics:  dataplane.NewMetrics(registry),
		configClient: configclient.New(config.ControlPlaneURL, config.Logger),
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
		errCh: make(chan error, 1),
	}

	sidecar.configVersionGauge = registry.Gauge(
		"mesh_config_version",
		"Current mesh config version",
	)
	sidecar.certExpiryGauge = registry.Gauge(
		"mesh_cert_expiry_seconds",
		"Seconds until the current certificate expires",
	)

	return sidecar, nil
}

// Errors 返回服务器错误。
func (s *Sidecar) Errors() <-chan error {
	return s.errCh
}

// Start 启动配置同步、证书、注册与三个监听器。
func (s *Sidecar) Start(ctx context.Context) error {
	if err := s.configClient.Start(ctx); err != nil {
		return fmt.Errorf("start config client: %w", err)
	}

	snapshot := s.configClient.Snapshot()
	if snapshot == nil {
		return fmt.Errorf("mesh snapshot is not ready")
	}

	roots, err := tlsutil.NewRootPool(snapshot.TrustBundlePEM)
	if err != nil {
		return fmt.Errorf("load trust bundle: %w", err)
	}
	s.roots = roots

	s.certManager = certs.New(
		s.config.ControlPlaneURL,
		s.identity,
		s.config.CertTTL,
		s.logger,
	)
	if err := s.certManager.Start(ctx); err != nil {
		return fmt.Errorf("start certificate manager: %w", err)
	}

	s.router = dataplane.NewRouter(
		s.config.Service,
		s.config.Namespace,
		s.configClient.Snapshot,
		s.certManager,
		s.roots,
		s.logger,
		s.meshMetrics,
	)

	s.configClient.SetOnUpdate(s.router.Sync)
	s.router.Sync(snapshot)

	if err := s.registerWithRetry(ctx); err != nil {
		return err
	}

	go s.heartbeatLoop(ctx)

	if err := s.startServers(); err != nil {
		return err
	}

	return nil
}

// Shutdown 先摘除实例，再关闭监听器。
func (s *Sidecar) Shutdown(ctx context.Context) {
	s.registered.Store(false)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	s.deregister(shutdownCtx)
	cancel()

	// 给控制面一点时间把实例从快照里移除，避免新流量继续进来。
	time.Sleep(200 * time.Millisecond)

	for _, server := range []*http.Server{s.inboundServer, s.outboundServer, s.adminServer} {
		if server != nil {
			_ = server.Shutdown(ctx)
		}
	}

	if s.router != nil {
		s.router.Close()
	}
}

func (s *Sidecar) startServers() error {
	inboundListener, err := net.Listen("tcp", s.config.InboundAddr)
	if err != nil {
		return fmt.Errorf("listen inbound %s: %w", s.config.InboundAddr, err)
	}

	outboundListener, err := net.Listen("tcp", s.config.OutboundAddr)
	if err != nil {
		_ = inboundListener.Close()
		return fmt.Errorf("listen outbound %s: %w", s.config.OutboundAddr, err)
	}

	adminListener, err := net.Listen("tcp", s.config.AdminAddr)
	if err != nil {
		_ = inboundListener.Close()
		_ = outboundListener.Close()
		return fmt.Errorf("listen admin %s: %w", s.config.AdminAddr, err)
	}

	s.inboundServer = &http.Server{
		Handler:           s.inboundHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	s.outboundServer = &http.Server{
		Handler:           s.outboundHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	s.adminServer = &http.Server{
		Handler:           s.adminHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	tlsListener := tls.NewListener(inboundListener, transport.NewServerTLS(s.certManager))

	go func() {
		s.logger.Info("sidecar inbound listening",
			"addr", s.config.InboundAddr,
			"app", s.config.AppAddr,
			"identity", s.identity,
		)
		if err := s.inboundServer.Serve(tlsListener); err != nil && err != http.ErrServerClosed {
			s.reportError(fmt.Errorf("inbound server: %w", err))
		}
	}()

	go func() {
		s.logger.Info("sidecar outbound listening", "addr", s.config.OutboundAddr)
		if err := s.outboundServer.Serve(outboundListener); err != nil && err != http.ErrServerClosed {
			s.reportError(fmt.Errorf("outbound server: %w", err))
		}
	}()

	go func() {
		s.logger.Info("sidecar admin listening", "addr", s.config.AdminAddr)
		if err := s.adminServer.Serve(adminListener); err != nil && err != http.ErrServerClosed {
			s.reportError(fmt.Errorf("admin server: %w", err))
		}
	}()

	return nil
}

func (s *Sidecar) reportError(err error) {
	s.logger.Error("sidecar server failed", "error", err)
	select {
	case s.errCh <- err:
	default:
	}
}

func (s *Sidecar) registerWithRetry(ctx context.Context) error {
	backoff := 500 * time.Millisecond

	for {
		err := s.register(ctx)
		if err == nil {
			s.registered.Store(true)
			s.logger.Info("instance registered",
				"service", s.config.Service,
				"instance", s.instanceID,
				"address", s.config.InboundAddr,
			)
			return nil
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		s.logger.Warn("instance registration failed; retrying", "error", err, "backoff", backoff)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}

		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

func (s *Sidecar) register(ctx context.Context) error {
	instance := meshapi.Instance{
		ID:        s.instanceID,
		Service:   s.config.Service,
		Namespace: s.config.Namespace,
		Address:   s.config.InboundAddr,
		Labels:    s.config.Labels,
		HealthURL: "http://" + s.config.AppAddr + s.config.HealthPath,
	}

	return s.postJSON(ctx, "/v1/instances/register", instance, http.StatusCreated)
}

func (s *Sidecar) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.heartbeat(ctx); err != nil {
				s.registered.Store(false)
				s.logger.Warn("instance heartbeat failed", "error", err)

				if registerErr := s.register(ctx); registerErr != nil {
					s.logger.Warn("re-register failed", "error", registerErr)
				} else {
					s.registered.Store(true)
				}
			} else {
				s.registered.Store(true)
			}

			s.fleetHeartbeat(ctx)
		}
	}
}

func (s *Sidecar) heartbeat(ctx context.Context) error {
	payload := map[string]any{
		"service":        s.config.Service,
		"id":             s.instanceID,
		"config_version": s.configClient.Version(),
		"cert_expiry":    s.certManager.NotAfter(),
	}

	return s.postJSON(ctx, "/v1/instances/heartbeat", payload, http.StatusNoContent)
}

func (s *Sidecar) fleetHeartbeat(ctx context.Context) {
	payload := meshapi.SidecarStatus{
		Identity:      s.identity,
		Service:       s.config.Service,
		Namespace:     s.config.Namespace,
		Inbound:       s.config.InboundAddr,
		ConfigVersion: s.configClient.Version(),
		CertExpiry:    s.certManager.NotAfter(),
	}

	if err := s.postJSON(ctx, "/v1/sidecars/heartbeat", payload, http.StatusNoContent); err != nil {
		s.logger.Debug("fleet heartbeat failed", "error", err)
	}
}

func (s *Sidecar) deregister(ctx context.Context) {
	endpoint := fmt.Sprintf(
		"%s/v1/instances/%s/%s",
		strings.TrimRight(s.config.ControlPlaneURL, "/"),
		url.PathEscape(s.config.Service),
		url.PathEscape(s.instanceID),
	)

	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return
	}

	response, err := s.httpClient.Do(request)
	if err != nil {
		s.logger.Warn("deregister failed", "error", err)
		return
	}
	_ = response.Body.Close()
}

func (s *Sidecar) postJSON(ctx context.Context, path string, payload any, expectedStatus int) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	endpoint := strings.TrimRight(s.config.ControlPlaneURL, "/") + path

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := s.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode != expectedStatus {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
		return fmt.Errorf("%s returned %s: %s", path, response.Status, strings.TrimSpace(string(message)))
	}

	return nil
}

func buildInstanceID(service, version, inboundAddr string) (string, error) {
	_, port, err := net.SplitHostPort(inboundAddr)
	if err != nil {
		return "", fmt.Errorf("invalid inbound address %q: %w", inboundAddr, err)
	}

	return fmt.Sprintf("%s-%s-%s", service, version, port), nil
}

// Snapshot 返回当前配置快照。
func (s *Sidecar) Snapshot() *meshapi.Snapshot {
	return s.configClient.Snapshot()
}
