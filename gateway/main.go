package main

import (
	"context"
	"errors"
	"example.com/go-gateway-practice/internal/discovery"
	"example.com/go-gateway-practice/internal/redisstore"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// 创建反向代理
// 支持负载均衡的反向代理包装器
type loadBalancedReverseProxy struct {
	balancer LoadBalancer
	proxy    *httputil.ReverseProxy
}

// 编译期检查：确保实现 http.Handler
var _ http.Handler = (*loadBalancedReverseProxy)(nil)

// ServeHTTP 实现 HTTP Handler 接口
func (p *loadBalancedReverseProxy) ServeHTTP(
	w http.ResponseWriter,
	r *http.Request,
) {

	// 1. 选择健康的后端实例
	target := p.balancer.Next()

	// 2. 没有健康实例，返回 503
	if target == nil {
		log.Printf("No healthy upstream for path: %s", r.URL.Path)

		http.Error(
			w,
			"Service Unavailable",
			http.StatusServiceUnavailable,
		)
		return
	}

	// 3. 将当前请求选择的后端地址放入 Context
	ctx := withSelectedUpstream(
		r.Context(),
		target,
	)

	// 4. 执行反向代理
	p.proxy.ServeHTTP(
		w,
		r.WithContext(ctx),
	)
}

// 创建支持健康检查和负载均衡的反向代理
func newReverseProxy(
	balancer LoadBalancer,
	prefix string,
) (http.Handler, error) {
	return newReverseProxyWithTransport(
		balancer,
		prefix,
		http.DefaultTransport,
	)
}

func newReverseProxyWithTransport(
	balancer LoadBalancer,
	prefix string,
	baseTransport http.RoundTripper,
) (http.Handler, error) {

	if balancer == nil {
		return nil, errors.New("load balancer is required")
	}

	// RoundRobinBalancer 实现这个接口；保留类型断言让简单测试替身仍可只实现 Next。
	reporter, _ := balancer.(upstreamOutcomeReporter)
	retrySelector, _ := balancer.(retryTargetSelector)

	proxy := &httputil.ReverseProxy{
		Transport: newRetryingRoundTripper(
			baseTransport,
			retrySelector,
			reporter,
		),

		Rewrite: func(pr *httputil.ProxyRequest) {

			// 1. 从 Context 获取本次请求选中的后端
			targetURL, ok := selectedUpstreamFromContext(
				pr.In.Context(),
			)

			if !ok {
				log.Printf("Selected upstream missing from request context")
				return
			}

			// 2. 设置后端地址
			pr.SetURL(targetURL)

			// 3. 删除路径前缀
			pr.Out.URL.Path = strings.TrimPrefix(
				pr.Out.URL.Path,
				prefix,
			)

			pr.Out.URL.RawPath = ""

			// 4. 设置转发请求头
			pr.SetXForwarded()
		},

		// 收到任何 HTTP 响应都说明传输链路可用，即使业务状态码是 4xx/5xx，
		// 也应打断“连接级连续失败”序列。
		ModifyResponse: func(resp *http.Response) error {
			if reporter == nil || resp == nil || resp.Request == nil {
				return nil
			}

			targetURL, ok := selectedUpstreamFromContext(
				resp.Request.Context(),
			)
			if ok {
				reporter.ReportSuccess(targetURL)
			}

			return nil
		},

		// 5. 自定义代理错误处理
		ErrorHandler: func(
			w http.ResponseWriter,
			r *http.Request,
			err error,
		) {
			// 客户端主动取消不代表后端实例异常；其他传输错误和网关超时
			// 才进入被动失败滑动窗口。
			if reporter != nil &&
				!errors.Is(r.Context().Err(), context.Canceled) {
				targetURL, ok := failedUpstreamFromError(
					r.Context(),
					err,
				)
				if ok {
					reporter.ReportFailure(targetURL)
				}
			}

			if errors.Is(err, context.DeadlineExceeded) ||
				errors.Is(r.Context().Err(), context.DeadlineExceeded) {

				log.Printf("Backend request timeout: %v", err)

				http.Error(
					w,
					"Gateway Timeout",
					http.StatusGatewayTimeout,
				)
				return
			}

			log.Printf("Reverse proxy error: %v", err)

			http.Error(
				w,
				"Bad Gateway",
				http.StatusBadGateway,
			)
		},
	}

	return &loadBalancedReverseProxy{
		balancer: balancer,
		proxy:    proxy,
	}, nil
}

func main() {

	// 1. 自动加载或生成 JWT 密钥
	jwtSecret, err := loadOrCreateJWTSecret("data/jwt.secret")
	if err != nil {
		log.Fatal(err)
	}

	// 每个客户端 IP 使用独立令牌桶。环境变量可以覆盖默认的每秒速率和突发容量。
	rateLimitSettings, err := loadRateLimitConfig(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	requestLimiter := newClientRateLimiter(
		rateLimitSettings.requestsPerSecond,
		rateLimitSettings.burst,
		defaultRateLimitClientTTL,
	)
	log.Printf(
		"Gateway rate limit configured: rps=%.2f burst=%d",
		rateLimitSettings.requestsPerSecond,
		rateLimitSettings.burst,
	)

	// 2. 初始化 Redis 地址
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "127.0.0.1:6387"
	}

	// 3. 创建 Redis 客户端
	redisCtx, redisCancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)

	redisClient, err := redisstore.New(
		redisCtx,
		redisstore.Config{
			Addr:     redisAddr,
			Password: os.Getenv("REDIS_PASSWORD"),
			DB:       0,
		},
	)

	redisCancel()

	if err != nil {
		log.Fatalf("Initialize Redis failed: %v", err)
	}

	defer func() {
		log.Println("[Shutdown] Redis Close started")

		start := time.Now()

		err := redisClient.Close()

		log.Printf(
			"[Shutdown] Redis Close finished, duration=%s, err=%v",
			time.Since(start),
			err,
		)
	}()
	log.Printf("Redis connected: %s", redisAddr)

	// 4. 初始化 Refresh Token 管理器
	refreshManager, err := NewRefreshTokenManager(
		redisClient,
		defaultRefreshTokenTTL,
	)
	if err != nil {
		log.Fatalf("Initialize refresh manager failed: %v", err)
	}

	// 5. 配置 Cookie 是否启用 Secure
	// 本地 HTTP 开发默认 false，生产环境设置为 true
	refreshCookieSecure := strings.EqualFold(
		strings.TrimSpace(os.Getenv("REFRESH_COOKIE_SECURE")),
		"true",
	)

	// 6. 初始化测试用户
	users, err := NewMemoryUserStore(
		os.Getenv("GATEWAY_ADMIN_USERNAME"),
		os.Getenv("GATEWAY_ADMIN_PASSWORD"),
	)
	if err != nil {
		log.Fatal(err)
	}

	// 可选配置一个普通用户，用于测试 RBAC
	regularUsername := strings.TrimSpace(
		os.Getenv("GATEWAY_USER_USERNAME"),
	)

	regularPassword := os.Getenv("GATEWAY_USER_PASSWORD")

	if regularUsername != "" || regularPassword != "" {

		if regularUsername == "" || regularPassword == "" {
			log.Fatal("both GATEWAY_USER_USERNAME and GATEWAY_USER_PASSWORD are required")
		}

		if err := users.AddRegularUser(
			regularUsername,
			regularPassword,
		); err != nil {
			log.Fatalf("Create regular user failed: %v", err)
		}
	}

	// 7. 创建 API 子路由
	apiMux := http.NewServeMux()

	// 创建根 Context，监听操作系统退出信号
	rootCtx, stopRoot := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stopRoot()

	// 健康检查 Context 继承根 Context
	healthCtx, stopHealthChecks := context.WithCancel(rootCtx)

	defer stopHealthChecks()

	// Gateway 只作为服务消费者，不需要向 Nacos 注册自己。
	// 它使用 NACOS_ADDR 创建客户端，用来查询 user-service 和 order-service。
	nacosConfig, err := loadNacosServerConfig(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}

	nacosClient, err := discovery.NewClient(
		nacosConfig.host,
		nacosConfig.port,
	)
	if err != nil {
		log.Fatalf("Initialize Nacos client: %v", err)
	}
	defer nacosClient.Close()

	log.Printf("Nacos discovery configured: %s", nacosConfig.address)

	// 被动摘除和恢复日志由独立 Goroutine 写出，业务请求只做非阻塞事件投递。
	upstreamEventLogger := newAsyncUpstreamEventLogger(
		log.Default(),
	)

	// 保存服务路由与负载均衡器的映射关系
	balancers := make(
		map[string]*RoundRobinBalancer,
		len(proxyRoutes),
	)

	// 按路由表创建负载均衡器和反向代理
	for _, route := range proxyRoutes {

		// 1. Gateway 启动时先向 Nacos 查询一次。
		// 查询失败时 resolveRouteTargets 会同时返回兜底地址和 error：
		// 地址继续用于启动，error 只负责把服务发现异常记录到日志。
		targets, discoveryWarning := resolveRouteTargets(
			nacosClient,
			route,
		)
		if discoveryWarning != nil {
			log.Printf(
				"Nacos discovery warning, use fallback targets: route=%s targets=%v err=%v",
				route.Name,
				targets,
				discoveryWarning,
			)
		} else {
			log.Printf(
				"Nacos instances discovered: route=%s targets=%v",
				route.Name,
				targets,
			)
		}

		balancer, err := NewRoundRobinBalancer(
			targets,
			WithUpstreamEventPublisher(upstreamEventLogger),
		)
		if err != nil {
			log.Fatalf(
				"Create load balancer for %s failed: %v",
				route.Name,
				err,
			)
		}

		// 2. 保存负载均衡器
		balancers[route.Path] = balancer

		// 3. 启动健康检查
		if err := balancer.StartHealthChecks(
			healthCtx,
			5*time.Second,
			1*time.Second,
		); err != nil {
			log.Fatalf(
				"Start health checks for %s failed: %v",
				route.Name,
				err,
			)
		}

		// 4. 创建反向代理
		proxy, err := newReverseProxy(
			balancer,
			route.Prefix,
		)
		if err != nil {
			log.Fatalf(
				"Create proxy for %s failed: %v",
				route.Name,
				err,
			)
		}

		// 5. 注册路由
		apiMux.Handle(route.Path, proxy)
	}

	// 后台定时刷新 Nacos 实例列表，而不是每个业务请求都查询 Nacos。
	// 请求只读取 balancer 当前保存的内存快照，因此转发链路更快，也不依赖 Nacos 的瞬时状态。
	const nacosRefreshInterval = 5 * time.Second

	var discoveryWG sync.WaitGroup
	discoveryWG.Add(1)

	go func() {
		defer discoveryWG.Done()

		ticker := time.NewTicker(nacosRefreshInterval)
		defer ticker.Stop()

		for {
			select {
			case <-healthCtx.Done():
				log.Println("Nacos target refresh stopped")
				return

			case <-ticker.C:
				// 单个服务刷新失败时保留最后一次可用地址；
				// refreshRouteTargets 仍会继续刷新其他服务。
				for _, refreshErr := range refreshRouteTargets(
					nacosClient,
					proxyRoutes,
					balancers,
				) {
					log.Printf("Nacos target refresh warning: %v", refreshErr)
				}
			}
		}
	}()

	// 9. 获取当前登录用户
	apiMux.HandleFunc("GET /api/whoami", func(
		w http.ResponseWriter,
		r *http.Request,
	) {

		userID, ok := r.Context().Value(
			userIDContextKey{},
		).(string)

		if !ok {
			http.Error(
				w,
				"Missing user identity",
				http.StatusInternalServerError,
			)
			return
		}

		fmt.Fprintf(w, "Current User ID: %s\n", userID)
	})

	// 10. 创建路由表与指标存储
	gatewayMetrics := newMetricsStore()

	apiMux.HandleFunc("GET /api/routes", routesHandler())
	apiMux.HandleFunc("GET /api/stats", statsHandler(gatewayMetrics))

	// // 查询后端实例
	// apiMux.HandleFunc(
	// 	"GET /api/upstreams",
	// 	listUpstreamsHandler(balancers),
	// )

	// // 添加后端实例
	// apiMux.HandleFunc(
	// 	"POST /api/upstreams",
	// 	addUpstreamHandler(balancers),
	// )

	// // 删除后端实例
	// apiMux.HandleFunc(
	// 	"DELETE /api/upstreams",
	// 	removeUpstreamHandler(balancers),
	// )

	// 查询：普通用户和管理员都可以
	apiMux.HandleFunc(
		"GET /api/upstreams",
		listUpstreamsHandler(balancers),
	)

	// 添加：仅管理员
	apiMux.Handle(
		"POST /api/upstreams",
		adminOnlyMiddleware(
			addUpstreamHandler(balancers),
		),
	)

	// 删除：仅管理员
	apiMux.Handle(
		"DELETE /api/upstreams",
		adminOnlyMiddleware(
			removeUpstreamHandler(balancers),
		),
	)

	// 11. 创建 Gateway 主路由
	mux := http.NewServeMux()

	// // 健康检查接口
	// mux.HandleFunc("GET /health", func(
	// 	w http.ResponseWriter,
	// 	r *http.Request,
	// ) {
	// 	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	// 	w.WriteHeader(http.StatusOK)
	// 	_, _ = w.Write([]byte("User Service OK"))
	// })

	// 12. 注册登录接口
	mux.HandleFunc(
		"POST /auth/login",
		loginHandler(
			jwtSecret,
			users,
			refreshManager,
			refreshCookieSecure,
		),
	)

	// 13. 注册 Refresh Token 续签接口
	mux.HandleFunc(
		"POST /auth/refresh",
		refreshHandler(
			jwtSecret,
			refreshManager,
			refreshCookieSecure,
			users,
		),
	)

	// 14. 注册退出登录接口
	mux.HandleFunc(
		"POST /auth/logout",
		logoutHandler(
			refreshManager,
			refreshCookieSecure,
		),
	)

	// 15. API 认证中间件
	apiHandler := authMiddleware(
		apiMux,
		jwtSecret,
	)

	// 16. API 超时中间件
	apiHandler = timeoutMiddleware(
		apiHandler,
		apiTimeout,
	)

	// 注册 API 路由
	mux.Handle("/api/", apiHandler)

	// 17. 健康检查
	// Gateway 自身的健康检查
	mux.HandleFunc("GET /health", func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Gateway OK"))
	})

	// 18. 测试看板静态资源
	registerUI(mux)

	// 19. 日志与指标中间件
	handler := metricsMiddleware(
		loggingMiddleware(
			rateLimitMiddleware(mux, requestLimiter),
		),
		gatewayMetrics,
	)

	// 20. 创建 HTTP Server
	server := &http.Server{
		Addr:              ":8080",
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// 21. 启动 Gateway
	log.Println("Gateway listening on :8080")

	// 用 Channel 接收 HTTP Server 的退出结果
	serverErr := make(chan error, 1)

	// 独立 Goroutine 启动 HTTP Server
	go func() {
		log.Printf("Gateway listening on %s", server.Addr)
		serverErr <- server.ListenAndServe()
	}()

	// 标记 HTTP Server 是否已经退出
	serverExited := false

	// 等待操作系统退出信号，或者 HTTP Server 异常退出
	select {

	case err := <-serverErr:

		serverExited = true

		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("Gateway server failed: %v", err)
		}

	case <-rootCtx.Done():

		log.Printf(
			"Shutdown signal received: %v",
			rootCtx.Err(),
		)
	}

	// 1. 停止 Nacos 定时刷新和健康检查
	stopHealthChecks()

	// 2. 等待 HTTP Server 优雅关闭
	shutdownCtx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)

	if err := server.Shutdown(shutdownCtx); err != nil {

		log.Printf("Graceful shutdown failed: %v", err)

		// 超时后强制关闭连接
		if closeErr := server.Close(); closeErr != nil {
			log.Printf("Force close failed: %v", closeErr)
		}
	}

	cancel()

	// 3. 确认 HTTP Server 的启动 Goroutine 已退出
	if !serverExited {
		err := <-serverErr

		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("Gateway server exit error: %v", err)
		}
	}

	// 4. 等待所有健康检查 Goroutine 退出
	for _, balancer := range balancers {
		balancer.WaitHealthChecks()
	}
	discoveryWG.Wait()
	upstreamEventLogger.Close()

	log.Println("Gateway shutdown sequence completed")
}
