
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"
)

// 创建反向代理
func newReverseProxy(
	target string,
	prefix string,
) (*httputil.ReverseProxy, error) {

	// 1. 解析后端服务地址
	targetURL, err := url.Parse(target)
	if err != nil {
		return nil, err
	}

	// 2. 创建 ReverseProxy
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {

			pr.SetURL(targetURL)

			pr.Out.URL.Path = strings.TrimPrefix(
				pr.Out.URL.Path,
				prefix,
			)

			pr.Out.URL.RawPath = ""

			pr.SetXForwarded()
		},

		// 3. 自定义代理错误处理
		ErrorHandler: func(
			w http.ResponseWriter,
			r *http.Request,
			err error,
		) {

			// 请求超时
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

			// 其他代理错误
			log.Printf("Reverse proxy error: %v", err)

			http.Error(
				w,
				"Bad Gateway",
				http.StatusBadGateway,
			)
		},
	}

	return proxy, nil
}

func main() {

	// 1. 自动加载或生成 JWT 密钥
	jwtSecret, err := loadOrCreateJWTSecret("data/jwt.secret")
	if err != nil {
		log.Fatal(err)
	}

	// 2. 初始化测试用户
	users, err := NewMemoryUserStore(
		os.Getenv("GATEWAY_ADMIN_USERNAME"),
		os.Getenv("GATEWAY_ADMIN_PASSWORD"),
	)
	if err != nil {
		log.Fatal(err)
	}

	// 3. 创建 API 子路由
	apiMux := http.NewServeMux()

	// 4. 按路由表注册反向代理
	for _, route := range proxyRoutes {
		proxy, err := newReverseProxy(
			route.Target,
			route.Prefix,
		)
		if err != nil {
			log.Fatalf(
				"Create proxy for %s failed: %v",
				route.Name,
				err,
			)
		}

		apiMux.Handle(route.Path, proxy)
	}

	// 5. 获取当前登录用户
	apiMux.HandleFunc("GET /api/whoami", func(
		w http.ResponseWriter,
		r *http.Request,
	) {

		userID, ok := r.Context().Value(
			userIDContextKey{},
		).(string)

		if !ok {
			http.Error(w, "Missing user identity", http.StatusInternalServerError)
			return
		}

		fmt.Fprintf(w, "Current User ID: %s\n", userID)
	})

	// 6. 创建路由表与指标存储
	gatewayMetrics := newMetricsStore()

	apiMux.HandleFunc("GET /api/routes", routesHandler())
	apiMux.HandleFunc("GET /api/stats", statsHandler(gatewayMetrics))

	// 7. 创建 Gateway 主路由
	mux := http.NewServeMux()

	// 8. 注册登录接口
	// 登录不需要 JWT 认证
	mux.HandleFunc(
		"POST /auth/login",
		loginHandler(jwtSecret, users),
	)

	// 9. API 认证中间件
	apiHandler := authMiddleware(
		apiMux,
		jwtSecret,
	)

	// 10. API 超时中间件
	apiHandler = timeoutMiddleware(
		apiHandler,
		apiTimeout,
	)

	// 注册 API 路由
	mux.Handle("/api/", apiHandler)

	// 11. 健康检查
	mux.HandleFunc("GET /health", func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		w.Write([]byte("Gateway OK"))
	})

	// 12. 测试看板静态资源
	registerUI(mux)

	// 13. 日志与指标中间件
	handler := metricsMiddleware(
		loggingMiddleware(mux),
		gatewayMetrics,
	)

	// 14. 创建 HTTP Server
	server := &http.Server{
		Addr:              ":8080",
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// 15. 启动 Gateway
	log.Println("Gateway listening on :8080")

	if err := server.ListenAndServe(); err != nil &&
		err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
