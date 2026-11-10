package main

import (
	"encoding/base64"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"
	"fmt"
)

// 创建反向代理
func newReverseProxy(target string, prefix string) (*httputil.ReverseProxy, error) {

	// 1. 解析后端服务地址
	targetURL, err := url.Parse(target)
	if err != nil {
		return nil, err
	}

	// 2. 创建 ReverseProxy
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {

			// 修改目标服务器地址
			pr.SetURL(targetURL)

			// 删除外部 API 路径前缀
			pr.Out.URL.Path = strings.TrimPrefix(
				pr.Out.URL.Path,
				prefix,
			)

			// 清除可能不再匹配的原始编码路径
			pr.Out.URL.RawPath = ""

			// 设置代理请求头
			pr.SetXForwarded()
		},
	}

	return proxy, nil
}

func main() {

	// 3. 创建用户服务代理
	userProxy, err := newReverseProxy(
		"http://127.0.0.1:9090",
		"/api",
	)
	if err != nil {
		log.Fatal(err)
	}

	// 4. 创建订单服务代理
	orderProxy, err := newReverseProxy(
		"http://127.0.0.1:9091",
		"/api",
	)
	if err != nil {
		log.Fatal(err)
	}

	// 5. 创建 Gateway 路由器
	// mux := http.NewServeMux()

	// mux.Handle("/api/users/", userProxy)
	// mux.Handle("/api/orders/", orderProxy)

	apiMux := http.NewServeMux()

	apiMux.Handle("/api/users/", userProxy)
	apiMux.Handle("/api/orders/", orderProxy)

	apiMux.HandleFunc("GET /api/whoami", func(
    w http.ResponseWriter,
    r *http.Request,
) {
    userID, ok := r.Context().Value(
        userIDContextKey{},
    ).(string)

    if !ok {
        http.Error(w, "Missing user identity", 500)
        return
    }

    fmt.Fprintf(w, "Current User ID: %s\n", userID)
})

	// 2. 创建 Gateway 主路由器
	mux := http.NewServeMux()

	// 3. API 路由统一经过认证中间件

	// 从环境变量读取 Base64 编码的密钥
	secretText := os.Getenv("JWT_SECRET")

	// 解码成原始字节
	jwtSecret, err := base64.StdEncoding.DecodeString(secretText)
	if err != nil || len(jwtSecret) < 32 {
		log.Fatal("JWT_SECRET must encode at least 32 random bytes")
	}

	// 注册 API 认证中间件
	mux.Handle("/api/", authMiddleware(apiMux, jwtSecret))

	// 6. 健康检查接口
	mux.HandleFunc("GET /health", func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		w.Write([]byte("Gateway OK"))
	})

	// 7. 启动 Gateway
	handler := loggingMiddleware(mux)

	server := &http.Server{
		Addr:              ":8080",
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Println("Gateway listening on :8080")

	log.Fatal(server.ListenAndServe())
}
