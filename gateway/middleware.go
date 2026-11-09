
package main

import (
    "log"
    "net/http"
    "time"
)

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
func authMiddleware(next http.Handler) http.Handler {

    return http.HandlerFunc(func(
        w http.ResponseWriter,
        r *http.Request,
    ) {

        // 从 HTTP 请求头中获取 Authorization
        token := r.Header.Get("Authorization")

        // 验证 Token
        if token != "Bearer dev-token" {

            // 返回 401 Unauthorized
            http.Error(
                w,
                "Unauthorized",
                http.StatusUnauthorized,
            )

            return
        }

        // 认证通过，交给下一个 Handler
        next.ServeHTTP(w, r)
    })
}

