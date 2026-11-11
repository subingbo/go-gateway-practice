package main

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"
)

type userIDContextKey struct{}

// 日志中间件
func loggingMiddleware(next http.Handler) http.Handler {

	return http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {

		// 记录请求开始时间
		start := time.Now()

		log.Printf(
			"Request Started | Method: %s | Path: %s | Remote: %s",
			r.Method,
			r.URL.Path,
			r.RemoteAddr,
		)

		// 交给下一个 Handler
		next.ServeHTTP(w, r)

		// 计算请求处理耗时
		duration := time.Since(start)

		log.Printf(
			"Request Completed | Method: %s | Path: %s | Duration: %s",
			r.Method,
			r.URL.Path,
			duration,
		)
	})
}

// 认证中间件

func authMiddleware(
	next http.Handler,
	secret []byte,
) http.Handler {

	return http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		// 1. 读取 Authorization 请求头
		authorization := r.Header.Get("Authorization")

		// 2. 检查 Bearer 前缀
		if !strings.HasPrefix(authorization, "Bearer ") {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// 3. 提取 JWT
		tokenString := strings.TrimPrefix(
			authorization,
			"Bearer ",
		)

		// 4. 验证 JWT
		claims, err := verifyJWT(tokenString, secret)
		if err != nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// 5. 获取用户 ID
		log.Printf("Authenticated user: %s", claims.UserID)

		// 6. 将用户 ID 保存到 Context
		ctx := context.WithValue(
			r.Context(),
			userIDContextKey{},
			claims.UserID,
		)

		// 7. 将新 Context 绑定到 Request
		newRequest := r.WithContext(ctx)

		// 8. 只调用一次下游 Handler
		next.ServeHTTP(w, newRequest)
	})
}

// 超时中间件
func timeoutMiddleware(
	next http.Handler,
	timeout time.Duration,
) http.Handler {

	return http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {

		// 创建具有超时时间的 Context
		ctx, cancel := context.WithTimeout(
			r.Context(),
			timeout,
		)

		// 请求结束时释放资源
		defer cancel()

		// 创建携带超时 Context 的新 Request
		newRequest := r.WithContext(ctx)

		// 继续执行下一个 Handler
		next.ServeHTTP(w, newRequest)
	})
}
