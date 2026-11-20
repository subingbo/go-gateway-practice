package main

import (
	"context"
	"errors"
	"example.com/go-gateway-practice/internal/redisstore"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"os"
	"strings"
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

	if balancer == nil {
		return nil, errors.New("load balancer is required")
	}

	proxy := &httputil.ReverseProxy{
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

		// 5. 自定义代理错误处理
		ErrorHandler: func(
			w http.ResponseWriter,
			r *http.Request,
			err error,
		) {

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
		if err := redisClient.Close(); err != nil {
			log.Printf("Close Redis client failed: %v", err)
		}
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

	// 7. 创建 API 子路由
	apiMux := http.NewServeMux()

	// 创建健康检查 Context
	healthCtx, stopHealthChecks := context.WithCancel(
		context.Background(),
	)
	defer stopHealthChecks()

	// 按路由表创建负载均衡器和反向代理
	for _, route := range proxyRoutes {

		// 1. 创建负载均衡器
		balancer, err := NewRoundRobinBalancer(route.Targets)
		if err != nil {
			log.Fatalf(
				"Create load balancer for %s failed: %v",
				route.Name,
				err,
			)
		}

		// 2. 启动健康检查
		// 每 5 秒检查一次，通过 HTTP GET /health 检查后端，单次检查最多等待 1 秒
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

		// 3. 创建反向代理
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

		// 4. 注册路由
		apiMux.Handle(route.Path, proxy)
	}
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
		loggingMiddleware(mux),
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

	if err := server.ListenAndServe(); err != nil &&
		err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
