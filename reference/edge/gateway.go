package main

import (
	"context"
	_ "embed"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"example.com/go-gateway-reference/internal/config"
	"example.com/go-gateway-reference/internal/httpx"
	"example.com/go-gateway-reference/internal/jwtutil"
	"example.com/go-gateway-reference/internal/logbuf"
	"example.com/go-gateway-reference/internal/metrics"
	"example.com/go-gateway-reference/internal/route"
	"example.com/go-gateway-reference/internal/tlsutil"
	"example.com/go-gateway-reference/internal/upstream"
	"example.com/go-gateway-reference/mesh/certs"
	"example.com/go-gateway-reference/mesh/configclient"
	"example.com/go-gateway-reference/mesh/dataplane"
)

//go:embed dashboard/index.html
var dashboardHTML []byte

type routeTable struct {
	rules  []route.Rule
	byName map[string]*compiledRoute
	static map[string]*staticCluster
	mesh   bool
}

type compiledRoute struct {
	config config.RouteConfig
	static *staticCluster
}

type meshState struct {
	client          *configclient.Client
	certManager     *certs.Manager
	roots           *tlsutil.RootPool
	router          *dataplane.Router
	controlPlaneURL string
	namespace       string
}

func (m *meshState) Version() uint64 {
	return m.client.Version()
}

func (m *meshState) Ready() bool {
	return m.client.Snapshot() != nil && m.certManager.Certificate() != nil
}

func (m *meshState) Close() {
	if m.router != nil {
		m.router.Close()
	}
}

// Gateway 是边缘网关。
type Gateway struct {
	configPath string
	logger     *slog.Logger
	logs       *logbuf.Buffer
	ctx        context.Context
	startedAt  time.Time

	cfg    atomic.Pointer[config.Config]
	routes atomic.Pointer[routeTable]
	ready  atomic.Bool

	auth    *authService
	limiter *rateLimiter
	stats   *statsStore
	mesh    *meshState

	metrics           *metrics.Registry
	requests          *metrics.CounterVec
	durations         *metrics.HistogramVec
	inFlight          *metrics.GaugeVec
	upstreamHealthy   *metrics.GaugeVec
	upstreamRetries   *metrics.CounterVec
	rateLimited       *metrics.CounterVec
	authFailures      *metrics.CounterVec
	reloads           *metrics.CounterVec
	meshConfigVersion *metrics.GaugeVec
}

// NewGateway 组装边缘网关。
func NewGateway(
	ctx context.Context,
	configPath string,
	cfg *config.Config,
	logs *logbuf.Buffer,
	logger *slog.Logger,
) (*Gateway, error) {
	secret, err := jwtutil.LoadOrCreateSecret(cfg.Auth.SecretFile)
	if err != nil {
		return nil, err
	}

	auth, err := newAuthService(cfg.Auth, secret)
	if err != nil {
		return nil, err
	}

	registry := metrics.NewRegistry()

	gateway := &Gateway{
		configPath: configPath,
		logger:     logger,
		logs:       logs,
		ctx:        ctx,
		startedAt:  time.Now(),
		auth:       auth,
		limiter:    newRateLimiter(cfg.RateLimit),
		stats:      newStatsStore(),
		metrics:    registry,
	}

	gateway.requests = registry.Counter(
		"gateway_http_requests_total",
		"Total edge gateway requests",
		"route", "method", "code",
	)
	gateway.durations = registry.Histogram(
		"gateway_http_request_duration_seconds",
		"Edge gateway request duration in seconds",
		[]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5},
		"route",
	)
	gateway.inFlight = registry.Gauge(
		"gateway_http_in_flight",
		"Requests currently being handled by the edge gateway",
	)
	gateway.upstreamHealthy = registry.Gauge(
		"gateway_upstream_healthy",
		"Upstream instance health (1 = healthy, 0 = unhealthy/ejected)",
		"cluster", "instance",
	)
	gateway.upstreamRetries = registry.Counter(
		"gateway_upstream_retries_total",
		"Total upstream retries performed by the edge gateway",
		"cluster",
	)
	gateway.rateLimited = registry.Counter(
		"gateway_ratelimit_rejected_total",
		"Total requests rejected by the rate limiter",
		"route",
	)
	gateway.authFailures = registry.Counter(
		"gateway_auth_failures_total",
		"Total authentication/authorization failures",
		"reason",
	)
	gateway.reloads = registry.Counter(
		"gateway_config_reloads_total",
		"Total successful configuration reloads",
	)
	gateway.meshConfigVersion = registry.Gauge(
		"gateway_mesh_config_version",
		"Current mesh config version seen by the edge gateway",
	)

	if usesMeshRoutes(cfg) {
		meshMetrics := dataplane.NewMetrics(registry)

		mesh, err := newMeshState(ctx, cfg, logger, meshMetrics)
		if err != nil {
			return nil, err
		}
		gateway.mesh = mesh
	}

	table, err := gateway.buildRouteTable(cfg)
	if err != nil {
		return nil, err
	}

	gateway.cfg.Store(cfg)
	gateway.routes.Store(table)
	gateway.ready.Store(true)

	return gateway, nil
}

func newMeshState(
	ctx context.Context,
	cfg *config.Config,
	logger *slog.Logger,
	meshMetrics *dataplane.Metrics,
) (*meshState, error) {
	client := configclient.New(cfg.Mesh.ControlPlaneURL, logger)

	if err := client.Start(ctx); err != nil {
		return nil, err
	}

	snapshot := client.Snapshot()
	if snapshot == nil {
		return nil, errors.New("mesh snapshot is not ready")
	}

	roots, err := tlsutil.NewRootPool(snapshot.TrustBundlePEM)
	if err != nil {
		return nil, err
	}

	certManager := certs.New(
		cfg.Mesh.ControlPlaneURL,
		tlsutil.SpiffeID(cfg.Mesh.Namespace, cfg.Mesh.Identity),
		cfg.Mesh.CertTTL,
		logger,
	)

	if err := certManager.Start(ctx); err != nil {
		return nil, err
	}

	router := dataplane.NewRouter(
		cfg.Mesh.Identity,
		cfg.Mesh.Namespace,
		client.Snapshot,
		certManager,
		roots,
		logger,
		meshMetrics,
	)

	client.SetOnUpdate(router.Sync)
	router.Sync(snapshot)

	return &meshState{
		client:          client,
		certManager:     certManager,
		roots:           roots,
		router:          router,
		controlPlaneURL: strings.TrimRight(cfg.Mesh.ControlPlaneURL, "/"),
		namespace:       cfg.Mesh.Namespace,
	}, nil
}

func usesMeshRoutes(cfg *config.Config) bool {
	for _, routeConfig := range cfg.Routes {
		if routeConfig.Upstream.IsMesh() {
			return true
		}
	}
	return false
}

func (g *Gateway) buildRouteTable(cfg *config.Config) (*routeTable, error) {
	table := &routeTable{
		byName: make(map[string]*compiledRoute, len(cfg.Routes)),
		static: make(map[string]*staticCluster),
	}

	for _, routeConfig := range cfg.Routes {
		compiled := &compiledRoute{config: routeConfig}

		if !routeConfig.Upstream.IsMesh() {
			compiled.static = newStaticCluster(
				g.ctx,
				routeConfig,
				g.logger,
				func(clusterName, address string, healthy bool) {
					value := 0.0
					if healthy {
						value = 1
					}
					g.upstreamHealthy.Set(value, clusterName, address)
				},
			)
			table.static[routeConfig.Name] = compiled.static
		} else {
			table.mesh = true
		}

		table.byName[routeConfig.Name] = compiled
		table.rules = append(table.rules, route.Rule{
			Match: route.Match{
				Host:       routeConfig.Match.Host,
				PathPrefix: routeConfig.Match.PathPrefix,
				Methods:    routeConfig.Match.Methods,
				Headers:    routeConfig.Match.Headers,
			},
			Value: compiled,
		})
	}

	return table, nil
}

// Reload 应用新配置；失败时保留旧路由。
func (g *Gateway) Reload(cfg *config.Config) {
	table, err := g.buildRouteTable(cfg)
	if err != nil {
		g.logger.Error("rebuild routes failed; keeping previous config", "error", err)
		return
	}

	if err := g.auth.SetUsers(cfg.Auth.Users); err != nil {
		for _, cluster := range table.static {
			cluster.Close()
		}
		g.logger.Error("reload auth users failed; keeping previous config", "error", err)
		return
	}

	old := g.routes.Swap(table)
	g.limiter.Update(cfg.RateLimit)
	g.cfg.Store(cfg)
	g.reloads.Inc()

	if old != nil {
		for _, cluster := range old.static {
			cluster.Close()
		}
	}

	g.logger.Info("gateway config reloaded", "routes", len(cfg.Routes))
}

// Close 停止健康检查并关闭连接。
func (g *Gateway) Close() {
	if table := g.routes.Load(); table != nil {
		for _, cluster := range table.static {
			cluster.Close()
		}
	}
	if g.mesh != nil {
		g.mesh.Close()
	}
}

// ServeHTTP 是网关主入口。
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	requestID := httpx.RequestID(r)

	w.Header().Set(httpx.RequestIDHeader, requestID)
	r = r.WithContext(httpx.WithRequestID(r.Context(), requestID))

	recorder := &httpx.StatusRecorder{ResponseWriter: w}

	routeName := "local"
	userID := ""
	upstreamName := ""
	observe := !isObservabilityPath(r.URL.Path)

	if observe {
		g.stats.begin()
		g.inFlight.Inc()
	}

	defer func() {
		if observe {
			g.inFlight.Dec()
		}

		if recovered := recover(); recovered != nil {
			g.logger.Error("panic while serving request",
				"request_id", requestID,
				"path", r.URL.Path,
				"panic", recovered,
			)
			httpx.ErrorJSON(recorder, http.StatusInternalServerError, "internal server error")
		}

		duration := time.Since(start)
		status := recorder.Status()

		if observe {
			g.stats.finish(requestRecord{
				Time:       start,
				Method:     r.Method,
				Path:       r.URL.Path,
				Route:      routeName,
				Status:     status,
				DurationMS: round2(float64(duration.Microseconds()) / 1000),
				RequestID:  requestID,
				UserID:     userID,
				Upstream:   upstreamName,
			})

			g.requests.Inc(routeName, r.Method, strconv.Itoa(status))
			g.durations.Observe(duration.Seconds(), routeName)
		}

		g.logger.Info("gateway request",
			"request_id", requestID,
			"method", r.Method,
			"path", r.URL.Path,
			"route", routeName,
			"status", status,
			"duration_ms", float64(duration.Microseconds())/1000,
			"user_id", userID,
			"upstream", upstreamName,
			"remote", r.RemoteAddr,
		)
	}()

	g.applyCORS(recorder, r)
	if r.Method == http.MethodOptions {
		recorder.WriteHeader(http.StatusNoContent)
		return
	}

	switch r.URL.Path {
	case "/health":
		routeName = "health"
		httpx.WriteJSON(recorder, http.StatusOK, map[string]any{
			"status":         "ok",
			"uptime_seconds": round2(time.Since(g.startedAt).Seconds()),
		})
		return
	case "/ready":
		routeName = "ready"
		g.handleReady(recorder)
		return
	case "/metrics":
		routeName = "metrics"
		g.handleMetrics(recorder)
		return
	case "/auth/login":
		routeName = "auth:login"
		g.handleLogin(recorder, r)
		return
	case "/auth/refresh":
		routeName = "auth:refresh"
		g.handleRefresh(recorder, r)
		return
	case "/auth/logout":
		routeName = "auth:logout"
		g.handleLogout(recorder, r)
		return
	case "/", "/dashboard":
		routeName = "dashboard"
		g.handleDashboard(recorder, r)
		return
	}

	switch r.URL.Path {
	case "/api/whoami":
		routeName = "api:whoami"
		g.handleWhoami(recorder, r)
		return
	case "/api/routes":
		routeName = "api:routes"
		g.handleRoutes(recorder, r)
		return
	case "/api/stats":
		routeName = "api:stats"
		g.handleStats(recorder, r)
		return
	case "/api/logs/stream":
		routeName = "api:logs"
		g.handleLogs(recorder, r)
		return
	case "/api/mesh/status":
		routeName = "api:mesh-status"
		g.handleMeshProxy(recorder, r, "/v1/status")
		return
	case "/api/mesh/config":
		routeName = "api:mesh-config"
		g.handleMeshProxy(recorder, r, "/v1/config")
		return
	}

	table := g.routes.Load()
	if table == nil {
		routeName = "unmatched"
		httpx.ErrorJSON(recorder, http.StatusServiceUnavailable, "gateway is not ready")
		return
	}

	index, ok := route.Best(table.rules, r)
	if !ok {
		routeName = "unmatched"
		httpx.ErrorJSON(recorder, http.StatusNotFound, "route not found")
		return
	}

	compiled := table.rules[index].Value.(*compiledRoute)
	routeName = compiled.config.Name

	if compiled.config.Upstream.Service != "" {
		upstreamName = compiled.config.Upstream.Service
	} else {
		upstreamName = compiled.config.Name
	}

	var claims *jwtutil.Claims

	if compiled.config.Auth.Required {
		authorized, ok := g.authorize(recorder, r, compiled.config.Auth)
		if !ok {
			return
		}

		claims = authorized
		userID = claims.UserID
		r = r.WithContext(withClaims(r.Context(), claims))
	}

	limit := g.limiter.LimitFor(compiled.config)
	if limit.Enabled() {
		identity := "ip:" + httpx.ClientIP(r)
		if claims != nil {
			identity = "user:" + claims.UserID
		}

		if !g.limiter.Allow(routeName+"|"+identity, limit) {
			g.rateLimited.Inc(routeName)
			recorder.Header().Set("Retry-After", "1")
			httpx.ErrorJSON(recorder, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
	}

	g.proxy(recorder, r, compiled, claims)
}

func (g *Gateway) proxy(
	w http.ResponseWriter,
	r *http.Request,
	compiled *compiledRoute,
	claims *jwtutil.Claims,
) {
	outReq := r.Clone(r.Context())

	path := r.URL.Path
	if strip := compiled.config.Match.StripPrefix; strip != "" && strings.HasPrefix(path, strip) {
		path = strings.TrimPrefix(path, strip)
		if path == "" {
			path = "/"
		}
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	outReq.URL.Path = path
	outReq.URL.RawPath = ""
	outReq.RequestURI = ""

	outReq.Header.Del("X-Gateway-User")
	outReq.Header.Del("X-Gateway-Roles")

	if claims != nil {
		outReq.Header.Set("X-Gateway-User", claims.UserID)
		outReq.Header.Set("X-Gateway-Roles", strings.Join(claims.Roles, ","))
	}

	outReq.Header.Set(httpx.RequestIDHeader, httpx.RequestIDFromContext(r.Context()))

	ctx := r.Context()
	if compiled.config.Upstream.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, compiled.config.Upstream.Timeout)
		defer cancel()
	}

	outReq = outReq.WithContext(ctx)

	var (
		response *http.Response
		attempts int
		err      error
	)

	if compiled.config.Upstream.IsMesh() {
		if g.mesh == nil {
			httpx.ErrorJSON(w, http.StatusServiceUnavailable, "mesh is not configured")
			return
		}

		response, err = g.mesh.router.Do(ctx, outReq, compiled.config.Upstream.Service)
		attempts = 1
	} else {
		if compiled.static == nil {
			httpx.ErrorJSON(w, http.StatusServiceUnavailable, "upstream is not configured")
			return
		}

		response, attempts, err = compiled.static.Do(ctx, outReq)
		if attempts > 1 {
			g.upstreamRetries.Add(float64(attempts-1), compiled.config.Name)
		}
	}

	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}

		g.logger.Warn("proxy request failed",
			"request_id", httpx.RequestIDFromContext(ctx),
			"route", compiled.config.Name,
			"error", err,
		)
		httpx.ErrorJSON(w, http.StatusBadGateway, "upstream request failed")
		return
	}
	defer response.Body.Close()

	httpx.CopyHeaders(w.Header(), response.Header)
	w.Header().Set(httpx.RequestIDHeader, httpx.RequestIDFromContext(ctx))
	w.Header().Set("X-Gateway-Route", compiled.config.Name)
	w.WriteHeader(response.StatusCode)

	if _, err := io.Copy(w, response.Body); err != nil {
		g.logger.Debug("copy upstream response failed",
			"request_id", httpx.RequestIDFromContext(ctx),
			"route", compiled.config.Name,
			"error", err,
		)
	}
}

func (g *Gateway) handleReady(w http.ResponseWriter) {
	ready := g.ready.Load() && g.routes.Load() != nil
	if ready && g.mesh != nil {
		ready = g.mesh.Ready()
	}

	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}

	payload := map[string]any{
		"ready": ready,
		"mesh":  g.mesh != nil,
	}

	if g.mesh != nil {
		payload["mesh_version"] = g.mesh.Version()
	}

	httpx.WriteJSON(w, status, payload)
}

func (g *Gateway) handleMetrics(w http.ResponseWriter) {
	g.updateUpstreamGauges()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = g.metrics.WritePrometheus(w)
}

func (g *Gateway) updateUpstreamGauges() {
	g.upstreamHealthy.Reset()

	table := g.routes.Load()
	if table == nil {
		return
	}

	for name, cluster := range table.static {
		for _, endpoint := range cluster.Endpoints() {
			value := 0.0
			if endpoint.Healthy && !endpoint.Ejected {
				value = 1
			}
			g.upstreamHealthy.Set(value, name, endpoint.Addr)
		}
	}

	if g.mesh == nil {
		return
	}

	for _, compiled := range table.byName {
		if !compiled.config.Upstream.IsMesh() {
			continue
		}

		for _, endpoint := range g.meshEndpoints(compiled.config.Upstream.Service, compiled.config.Upstream.Subset) {
			value := 0.0
			if endpoint.Healthy && !endpoint.Ejected {
				value = 1
			}

			cluster := compiled.config.Name
			if endpoint.Subset != "" {
				cluster += "/" + endpoint.Subset
			}

			g.upstreamHealthy.Set(value, cluster, endpoint.Addr)
		}
	}

	g.meshConfigVersion.Set(float64(g.mesh.Version()))
}

// meshEndpoints 返回一个网格服务的全部子集实例。
//
// DestinationRule 定义了 subset 时，按 subset 分别展示，这样某个版本的
// 实例被离群摘除时，看板和 /api/routes 能直接看到。
func (g *Gateway) meshEndpoints(service, pinnedSubset string) []upstream.EndpointStatus {
	if g.mesh == nil {
		return nil
	}

	if pinnedSubset != "" {
		return g.mesh.router.Endpoints(service, pinnedSubset)
	}

	snapshot := g.mesh.client.Snapshot()
	if snapshot == nil {
		return nil
	}

	dr := snapshot.FindDestinationRule(service)
	if dr == nil || len(dr.Spec.Subsets) == 0 {
		return g.mesh.router.Endpoints(service, "")
	}

	var endpoints []upstream.EndpointStatus
	for _, subset := range dr.Spec.Subsets {
		endpoints = append(endpoints, g.mesh.router.Endpoints(service, subset.Name)...)
	}

	return endpoints
}

func (g *Gateway) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/dashboard" {
		httpx.ErrorJSON(w, http.StatusNotFound, "not found")
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(dashboardHTML)
}

func (g *Gateway) applyCORS(w http.ResponseWriter, r *http.Request) {
	cfg := g.cfg.Load()
	if cfg == nil {
		return
	}

	origin := r.Header.Get("Origin")
	if origin == "" {
		return
	}

	allowed := false

	for _, candidate := range cfg.CORS.AllowedOrigins {
		if strings.EqualFold(candidate, origin) {
			allowed = true
			break
		}
	}

	if !allowed {
		return
	}

	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Vary", "Origin")

	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID, X-Canary")
	w.Header().Set("Access-Control-Max-Age", "600")
}

func isObservabilityPath(path string) bool {
	switch path {
	case "/health", "/ready", "/metrics", "/", "/dashboard",
		"/api/stats", "/api/routes", "/api/mesh/status", "/api/mesh/config",
		"/api/logs/stream", "/favicon.ico":
		return true
	default:
		return strings.HasPrefix(path, "/assets/")
	}
}

type matchView struct {
	Host        string            `json:"host,omitempty"`
	PathPrefix  string            `json:"path_prefix"`
	Methods     []string          `json:"methods,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	StripPrefix string            `json:"strip_prefix,omitempty"`
}

type upstreamView struct {
	Service      string   `json:"service,omitempty"`
	Subset       string   `json:"subset,omitempty"`
	Instances    []string `json:"instances,omitempty"`
	TimeoutMS    int64    `json:"timeout_ms"`
	LoadBalancer string   `json:"load_balancer,omitempty"`
}

type routeView struct {
	Name      string                    `json:"name"`
	Match     matchView                 `json:"match"`
	Upstream  upstreamView              `json:"upstream"`
	Auth      config.AuthPolicy         `json:"auth"`
	RateLimit *config.Limit             `json:"rate_limit,omitempty"`
	Endpoints []upstream.EndpointStatus `json:"endpoints"`
}

type meshSummary struct {
	ControlPlaneURL string `json:"control_plane_url"`
	Version         uint64 `json:"version"`
	Services        int    `json:"services"`
	Instances       int    `json:"instances"`
}

func (g *Gateway) meshSummary() *meshSummary {
	if g.mesh == nil {
		return nil
	}

	snapshot := g.mesh.client.Snapshot()
	if snapshot == nil {
		return &meshSummary{ControlPlaneURL: g.mesh.controlPlaneURL}
	}

	instances := 0
	for _, service := range snapshot.Services {
		instances += len(service.Instances)
	}

	return &meshSummary{
		ControlPlaneURL: g.mesh.controlPlaneURL,
		Version:         snapshot.Version,
		Services:        len(snapshot.Services),
		Instances:       instances,
	}
}

func (g *Gateway) handleWhoami(w http.ResponseWriter, r *http.Request) {
	claims, ok := g.authorize(w, r, config.AuthPolicy{Required: true})
	if !ok {
		return
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"user_id":    claims.UserID,
		"roles":      claims.Roles,
		"jti":        claims.ID,
		"expires_at": claims.ExpiresAt.Time,
		"request_id": httpx.RequestIDFromContext(r.Context()),
	})
}

func (g *Gateway) handleRoutes(w http.ResponseWriter, r *http.Request) {
	if _, ok := g.authorize(w, r, config.AuthPolicy{Required: true}); !ok {
		return
	}

	cfg := g.cfg.Load()
	table := g.routes.Load()
	if table == nil {
		httpx.ErrorJSON(w, http.StatusServiceUnavailable, "gateway is not ready")
		return
	}

	views := make([]routeView, 0, len(cfg.Routes))

	for _, routeConfig := range cfg.Routes {
		view := routeView{
			Name: routeConfig.Name,
			Match: matchView{
				Host:        routeConfig.Match.Host,
				PathPrefix:  routeConfig.Match.PathPrefix,
				Methods:     routeConfig.Match.Methods,
				Headers:     routeConfig.Match.Headers,
				StripPrefix: routeConfig.Match.StripPrefix,
			},
			Upstream: upstreamView{
				Service:      routeConfig.Upstream.Service,
				Subset:       routeConfig.Upstream.Subset,
				TimeoutMS:    routeConfig.Upstream.Timeout.Milliseconds(),
				LoadBalancer: routeConfig.Upstream.LoadBalancer,
			},
			Auth:      routeConfig.Auth,
			RateLimit: routeConfig.RateLimit,
		}

		for _, instance := range routeConfig.Upstream.Instances {
			view.Upstream.Instances = append(view.Upstream.Instances, instance.Address)
		}

		if routeConfig.Upstream.IsMesh() {
			view.Endpoints = g.meshEndpoints(routeConfig.Upstream.Service, routeConfig.Upstream.Subset)
		} else if cluster, ok := table.static[routeConfig.Name]; ok {
			view.Endpoints = cluster.Endpoints()
		}

		views = append(views, view)
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"routes": views,
		"mesh":   g.meshSummary(),
	})
}

type statsResponse struct {
	StatsSnapshot
	Upstreams []upstreamStatusView `json:"upstreams"`
	Mesh      *meshSummary         `json:"mesh,omitempty"`
}

type upstreamStatusView struct {
	Route     string                    `json:"route"`
	Cluster   string                    `json:"cluster"`
	Endpoints []upstream.EndpointStatus `json:"endpoints"`
}

func (g *Gateway) handleStats(w http.ResponseWriter, r *http.Request) {
	if _, ok := g.authorize(w, r, config.AuthPolicy{Required: true}); !ok {
		return
	}

	cfg := g.cfg.Load()
	table := g.routes.Load()
	if table == nil {
		httpx.ErrorJSON(w, http.StatusServiceUnavailable, "gateway is not ready")
		return
	}

	response := statsResponse{
		StatsSnapshot: g.stats.snapshot(),
		Mesh:          g.meshSummary(),
	}

	for _, routeConfig := range cfg.Routes {
		item := upstreamStatusView{
			Route:   routeConfig.Name,
			Cluster: routeConfig.Upstream.Service,
		}
		if item.Cluster == "" {
			item.Cluster = routeConfig.Name
		}

		if routeConfig.Upstream.IsMesh() {
			item.Endpoints = g.meshEndpoints(routeConfig.Upstream.Service, routeConfig.Upstream.Subset)
		} else if cluster, ok := table.static[routeConfig.Name]; ok {
			item.Endpoints = cluster.Endpoints()
		}

		response.Upstreams = append(response.Upstreams, item)
	}

	httpx.WriteJSON(w, http.StatusOK, response)
}

func (g *Gateway) handleMeshProxy(w http.ResponseWriter, r *http.Request, path string) {
	if _, ok := g.authorize(w, r, config.AuthPolicy{Required: true}); !ok {
		return
	}

	if g.mesh == nil {
		httpx.ErrorJSON(w, http.StatusServiceUnavailable, "mesh is not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, g.mesh.controlPlaneURL+path, nil)
	if err != nil {
		httpx.ErrorJSON(w, http.StatusInternalServerError, "build control plane request failed")
		return
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		httpx.ErrorJSON(w, http.StatusBadGateway, "control plane unavailable")
		return
	}
	defer response.Body.Close()

	httpx.CopyHeaders(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

func (g *Gateway) handleLogs(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("access_token")
	if token == "" {
		token = bearerToken(r)
	}

	_, err := g.auth.Verify(token)
	if err != nil {
		g.authFailures.Inc("invalid_logs_token")
		httpx.ErrorJSON(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	stream, err := httpx.NewSSE(w)
	if err != nil {
		httpx.ErrorJSON(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}

	for _, line := range g.logs.Lines() {
		if err := stream.Event("log", line); err != nil {
			return
		}
	}

	lines, cancel := g.logs.Subscribe()
	defer cancel()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case line, ok := <-lines:
			if !ok {
				return
			}
			if err := stream.Event("log", line); err != nil {
				return
			}
		case <-heartbeat.C:
			if err := stream.Comment("ping"); err != nil {
				return
			}
		}
	}
}
