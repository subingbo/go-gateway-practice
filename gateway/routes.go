package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// API 路由统一使用的超时时间
const apiTimeout = 10 * time.Second

// proxyRoute 描述一条需要反向代理的 API 路由
type proxyRoute struct {
	// Name 是展示给管理页面和日志看的中文名称
	Name string

	// ServiceName 是注册在 Nacos 中的逻辑服务名
	// Gateway 后续使用它查询健康实例，而不是依赖固定 IP
	ServiceName string

	// Path 是 Gateway 对外匹配的路由前缀
	Path string

	// FallbackTargets 是 Nacos 暂时不可用时使用的静态兜底地址
	// 它不是用来保存 Nacos 返回结果的；动态实例由负载均衡器自己维护
	FallbackTargets []string

	// Prefix 是转发给后端前需要从请求路径中删除的前缀
	Prefix string
}

// proxyRoutes 是网关当前代理的上游路由表
var proxyRoutes = []proxyRoute{
	{
		Name:        "用户服务",
		ServiceName: "user-service",
		Path:        "/api/users/",
		FallbackTargets: []string{
			"http://127.0.0.1:9090",
			"http://127.0.0.1:9092",
		},
		Prefix: "/api",
	},
	{
		Name:        "订单服务",
		ServiceName: "order-service",
		Path:        "/api/orders/",
		FallbackTargets: []string{
			"http://127.0.0.1:9091",
		},
		Prefix: "/api",
	},
}

// routeInfo 是 /api/routes 返回给看板的路由描述
type routeInfo struct {
	Name        string `json:"name"`
	Method      string `json:"method"`
	Path        string `json:"path"`
	Upstream    string `json:"upstream,omitempty"`
	Auth        bool   `json:"auth"`
	TimeoutMS   int64  `json:"timeout_ms,omitempty"`
	Description string `json:"description,omitempty"`
}

// routeInfos 汇总代理路由与网关自身的接口
func routeInfos() []routeInfo {
	infos := make([]routeInfo, 0, len(proxyRoutes)+7)

	for _, route := range proxyRoutes {
		infos = append(infos, routeInfo{
			Name:        route.Name,
			Method:      "*",
			Path:        route.Path,
			Upstream:    strings.Join(route.FallbackTargets, ", "),
			Auth:        true,
			TimeoutMS:   apiTimeout.Milliseconds(),
			Description: "反向代理，转发前剥离 /api 前缀",
		})
	}

	infos = append(infos,
		routeInfo{
			Name:        "登录",
			Method:      "POST",
			Path:        "/auth/login",
			Auth:        false,
			Description: "校验密码并签发 JWT",
		},
		routeInfo{
			Name:        "刷新令牌",
			Method:      "POST",
			Path:        "/auth/refresh",
			Auth:        false,
			Description: "使用 Refresh Token Cookie 换取新的双 Token",
		},
		routeInfo{
			Name:        "退出登录",
			Method:      "POST",
			Path:        "/auth/logout",
			Auth:        false,
			Description: "撤销 Refresh Token 并清除 Cookie",
		},
		routeInfo{
			Name:        "当前用户",
			Method:      "GET",
			Path:        "/api/whoami",
			Auth:        true,
			TimeoutMS:   apiTimeout.Milliseconds(),
			Description: "从 Context 读取当前用户",
		},
		routeInfo{
			Name:        "路由表",
			Method:      "GET",
			Path:        "/api/routes",
			Auth:        true,
			TimeoutMS:   apiTimeout.Milliseconds(),
			Description: "返回网关路由与上游配置",
		},
		routeInfo{
			Name:        "网关指标",
			Method:      "GET",
			Path:        "/api/stats",
			Auth:        true,
			TimeoutMS:   apiTimeout.Milliseconds(),
			Description: "返回进程内请求统计",
		},
		routeInfo{
			Name:        "健康检查",
			Method:      "GET",
			Path:        "/health",
			Auth:        false,
			Description: "网关存活检查",
		},
	)

	return infos
}

// routesHandler 返回当前网关路由表
func routesHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		if err := json.NewEncoder(w).Encode(map[string]any{
			"routes": routeInfos(),
		}); err != nil {
			http.Error(w, "Encode response failed", http.StatusInternalServerError)
		}
	}
}
