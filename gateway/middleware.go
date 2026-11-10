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

		// 3. 提取 JWT 字符串
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

		// 5. 验证通过，记录用户 ID
		log.Printf("Authenticated user: %s", claims.UserID)

		// 6. 创建携带用户 ID 的 Context
		ctx := context.WithValue(
			r.Context(),
			userIDContextKey{},
			claims.UserID,
		)

		// 7. 将新 Context 绑定到 Request
		newRequest := r.WithContext(ctx)

		// 8. 继续调用下一个 Handler
		next.ServeHTTP(w, newRequest)

		// 6. 继续交给下一个 Handler
		next.ServeHTTP(w, r)
	})
}
