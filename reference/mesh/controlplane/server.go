package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"example.com/go-gateway-reference/internal/httpx"
	"example.com/go-gateway-reference/internal/meshapi"
	"example.com/go-gateway-reference/internal/metrics"
)

//go:embed dashboard/index.html
var dashboardHTML []byte

// sidecarFleet 记录 sidecar 心跳上报的状态，仅用于控制面展示。
type sidecarFleet struct {
	mu    sync.Mutex
	items map[string]meshapi.SidecarStatus
}

func newSidecarFleet() *sidecarFleet {
	return &sidecarFleet{items: make(map[string]meshapi.SidecarStatus)}
}

func (f *sidecarFleet) Update(status meshapi.SidecarStatus) {
	f.mu.Lock()
	f.items[status.Identity] = status
	f.mu.Unlock()
}

func (f *sidecarFleet) List() []meshapi.SidecarStatus {
	f.mu.Lock()
	defer f.mu.Unlock()

	now := time.Now()
	out := make([]meshapi.SidecarStatus, 0, len(f.items))

	for identity, status := range f.items {
		if now.Sub(status.LastSeen) > 2*time.Minute {
			delete(f.items, identity)
			continue
		}
		out = append(out, status)
	}

	return out
}

// Server 是控制面的 HTTP 入口。
type Server struct {
	registry *Registry
	store    *ConfigStore
	ca       *CA
	fleet    *sidecarFleet
	logger   *slog.Logger
	metrics  *metrics.Registry

	startedAt time.Time

	issuedCounter  *metrics.CounterVec
	versionGauge   *metrics.GaugeVec
	instancesGauge *metrics.GaugeVec
	watchGauge     *metrics.GaugeVec
	watchClients   atomic.Int64
}

// NewServer 创建控制面服务器。
func NewServer(registry *Registry, store *ConfigStore, ca *CA, logger *slog.Logger) *Server {
	registryMetrics := metrics.NewRegistry()

	server := &Server{
		registry:  registry,
		store:     store,
		ca:        ca,
		fleet:     newSidecarFleet(),
		logger:    logger,
		metrics:   registryMetrics,
		startedAt: time.Now(),
	}

	server.issuedCounter = registryMetrics.Counter(
		"mesh_ca_certs_issued_total",
		"Total number of issued mesh certificates",
	)
	server.versionGauge = registryMetrics.Gauge(
		"mesh_config_version",
		"Current mesh snapshot version",
	)
	server.instancesGauge = registryMetrics.Gauge(
		"mesh_registry_instances",
		"Registered instances by service and status",
		"service", "status",
	)
	server.watchGauge = registryMetrics.Gauge(
		"mesh_watch_clients",
		"Currently connected config watch clients",
	)

	return server
}

// Handler 返回控制面的完整路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /v1/registry", s.handleRegistry)
	mux.HandleFunc("GET /v1/config", s.handleConfig)
	mux.HandleFunc("GET /v1/watch", s.handleWatch)
	mux.HandleFunc("POST /v1/instances/register", s.handleRegister)
	mux.HandleFunc("POST /v1/instances/heartbeat", s.handleHeartbeat)
	mux.HandleFunc("DELETE /v1/instances/{service}/{id}", s.handleDeregister)
	mux.HandleFunc("POST /v1/certs/issue", s.handleIssueCert)
	mux.HandleFunc("GET /v1/ca", s.handleCA)
	mux.HandleFunc("POST /v1/sidecars/heartbeat", s.handleSidecarHeartbeat)
	mux.HandleFunc("GET /v1/sidecars", s.handleSidecars)
	mux.HandleFunc("GET /v1/status", s.handleStatus)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.HandleFunc("GET /", s.handleDashboard)

	return s.withCommon(mux)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"uptime_seconds": time.Since(s.startedAt).Seconds(),
	})
}

func (s *Server) handleRegistry(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"version":  s.store.Version(),
		"services": s.registry.Snapshot(),
	})
}

func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, s.store.Snapshot())
}

func (s *Server) handleWatch(w http.ResponseWriter, r *http.Request) {
	clientVersion, _ := strconv.ParseUint(r.URL.Query().Get("version"), 10, 64)

	stream, err := httpx.NewSSE(w)
	if err != nil {
		httpx.ErrorJSON(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}

	changes, cancel := s.store.Subscribe()
	defer cancel()

	s.watchClients.Add(1)
	defer s.watchClients.Add(-1)

	send := func() error {
		snapshot := s.store.Snapshot()
		data, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		return stream.Event("snapshot", data)
	}

	if s.store.Version() > clientVersion {
		if err := send(); err != nil {
			return
		}
	}

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-changes:
			if err := send(); err != nil {
				return
			}
		case <-heartbeat.C:
			if err := stream.Comment("ping"); err != nil {
				return
			}
		}
	}
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var instance meshapi.Instance

	if err := decodeJSON(w, r, &instance); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := s.registry.Register(instance); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, err.Error())
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"status":  "registered",
		"id":      instance.ID,
		"service": instance.Service,
		"address": instance.Address,
		"version": s.store.Version(),
	})
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Service       string    `json:"service"`
		ID            string    `json:"id"`
		ConfigVersion uint64    `json:"config_version"`
		CertExpiry    time.Time `json:"cert_expiry"`
	}

	if err := decodeJSON(w, r, &request); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if !s.registry.Heartbeat(request.Service, request.ID, request.ConfigVersion, request.CertExpiry) {
		httpx.ErrorJSON(w, http.StatusNotFound, "instance not registered")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeregister(w http.ResponseWriter, r *http.Request) {
	service := r.PathValue("service")
	id := r.PathValue("id")

	if !s.registry.Deregister(service, id) {
		httpx.ErrorJSON(w, http.StatusNotFound, "instance not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleIssueCert(w http.ResponseWriter, r *http.Request) {
	var request struct {
		SpiffeID   string `json:"spiffe_id"`
		TTLSeconds int    `json:"ttl_seconds"`
	}

	if err := decodeJSON(w, r, &request); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	certPEM, keyPEM, expiresAt, serial, err := s.ca.Issue(
		request.SpiffeID,
		time.Duration(request.TTLSeconds)*time.Second,
	)
	if err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, err.Error())
		return
	}

	s.issuedCounter.Inc()

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"spiffe_id":       request.SpiffeID,
		"certificate_pem": certPEM,
		"private_key_pem": keyPEM,
		"root_ca_pem":     s.ca.RootPEM(),
		"serial":          serial,
		"expires_at":      expiresAt,
	})
}

func (s *Server) handleCA(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, s.ca.Info())
}

func (s *Server) handleSidecarHeartbeat(w http.ResponseWriter, r *http.Request) {
	var status meshapi.SidecarStatus

	if err := decodeJSON(w, r, &status); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if status.Identity == "" {
		httpx.ErrorJSON(w, http.StatusBadRequest, "identity is required")
		return
	}

	status.LastSeen = time.Now()
	s.fleet.Update(status)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSidecars(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sidecars": s.fleet.List()})
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	snapshot := s.store.Snapshot()

	caInfo := s.ca.Info()
	if len(caInfo.Issued) > 20 {
		caInfo.Issued = caInfo.Issued[:20]
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"version":           snapshot.Version,
		"generated_at":      snapshot.GeneratedAt,
		"services":          snapshot.Services,
		"virtual_services":  snapshot.VirtualServices,
		"destination_rules": snapshot.DestinationRules,
		"sidecars":          s.fleet.List(),
		"ca":                caInfo,
		"ca_issued_total":   s.ca.IssuedTotal(),
		"config_error":      s.store.LastError(),
		"watch_clients":     s.watchClients.Load(),
		"uptime_seconds":    time.Since(s.startedAt).Seconds(),
	})
}

func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	s.updateGauges()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = s.metrics.WritePrometheus(w)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		httpx.ErrorJSON(w, http.StatusNotFound, "not found")
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(dashboardHTML)
}

func (s *Server) updateGauges() {
	s.versionGauge.Set(float64(s.store.Version()))
	s.watchGauge.Set(float64(s.watchClients.Load()))

	s.instancesGauge.Reset()
	for service, statuses := range s.registry.StatusCounts() {
		for status, count := range statuses {
			s.instancesGauge.Set(float64(count), service, string(status))
		}
	}
}

func (s *Server) withCommon(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		requestID := httpx.RequestID(r)

		w.Header().Set(httpx.RequestIDHeader, requestID)
		r = r.WithContext(httpx.WithRequestID(r.Context(), requestID))

		recorder := &httpx.StatusRecorder{ResponseWriter: w}

		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error("panic while serving request",
					"request_id", requestID,
					"path", r.URL.Path,
					"panic", recovered,
				)
				httpx.ErrorJSON(recorder, http.StatusInternalServerError, "internal server error")
			}

			s.logger.Info("control plane request",
				"request_id", requestID,
				"method", r.Method,
				"path", r.URL.Path,
				"status", recorder.Status(),
				"duration_ms", float64(time.Since(start).Microseconds())/1000,
				"remote", r.RemoteAddr,
			)
		}()

		next.ServeHTTP(recorder, r)
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}

	return nil
}
