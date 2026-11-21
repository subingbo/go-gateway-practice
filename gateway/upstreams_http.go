package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

// UpstreamRequest 表示管理后端实例的请求
type upstreamRequest struct {
	Service string `json:"service"`
	Target  string `json:"target"`
}

// 查询指定服务的后端实例
func listUpstreamsHandler(
	balancers map[string]*RoundRobinBalancer,
) http.HandlerFunc {

	return func(w http.ResponseWriter, r *http.Request) {

		// 1. 获取服务标识
		service := strings.TrimSpace(
			r.URL.Query().Get("service"),
		)

		if service == "" {
			http.Error(
				w,
				"service is required",
				http.StatusBadRequest,
			)
			return
		}

		// 2. 找到对应的负载均衡器
		balancer, ok := balancers[service]

		if !ok {
			http.Error(
				w,
				"service not found",
				http.StatusNotFound,
			)
			return
		}

		// 3. 获取后端实例状态
		targets := balancer.ListTargets()

		// 4. 返回 JSON
		w.Header().Set(
			"Content-Type",
			"application/json",
		)

		w.Header().Set(
			"Cache-Control",
			"no-store",
		)

		if err := json.NewEncoder(w).Encode(map[string]any{
			"service": service,
			"targets": targets,
		}); err != nil {
			http.Error(
				w,
				"Encode response failed",
				http.StatusInternalServerError,
			)
		}
	}
}

// 添加后端实例
func addUpstreamHandler(
	balancers map[string]*RoundRobinBalancer,
) http.HandlerFunc {

	return func(w http.ResponseWriter, r *http.Request) {

		// 1. 解析 JSON 请求体
		var req upstreamRequest

		decoder := json.NewDecoder(
			http.MaxBytesReader(w, r.Body, 1<<20),
		)

		decoder.DisallowUnknownFields()

		if err := decoder.Decode(&req); err != nil {
			http.Error(
				w,
				"Invalid JSON body: "+err.Error(),
				http.StatusBadRequest,
			)
			return
		}

		// 2. 校验参数
		service := strings.TrimSpace(req.Service)
		target := strings.TrimSpace(req.Target)

		if service == "" || target == "" {
			http.Error(
				w,
				"service and target are required",
				http.StatusBadRequest,
			)
			return
		}

		// 3. 找到对应的负载均衡器
		balancer, ok := balancers[service]

		if !ok {
			http.Error(
				w,
				"service not found",
				http.StatusNotFound,
			)
			return
		}

		// 4. 添加后端实例
		if err := balancer.AddTarget(target); err != nil {
			http.Error(
				w,
				err.Error(),
				http.StatusBadRequest,
			)
			return
		}

		// 5. 返回结果
		w.Header().Set(
			"Content-Type",
			"application/json",
		)

		w.WriteHeader(http.StatusCreated)

		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": "upstream added",
			"service": service,
			"target":  target,
		})
	}
}

// 删除后端实例
func removeUpstreamHandler(
	balancers map[string]*RoundRobinBalancer,
) http.HandlerFunc {

	return func(w http.ResponseWriter, r *http.Request) {

		// 1. 获取参数
		service := strings.TrimSpace(
			r.URL.Query().Get("service"),
		)

		target := strings.TrimSpace(
			r.URL.Query().Get("target"),
		)

		if service == "" || target == "" {
			http.Error(
				w,
				"service and target are required",
				http.StatusBadRequest,
			)
			return
		}

		// 2. 找到对应的负载均衡器
		balancer, ok := balancers[service]

		if !ok {
			http.Error(
				w,
				"service not found",
				http.StatusNotFound,
			)
			return
		}

		// 3. 删除后端实例
		if err := balancer.RemoveTarget(target); err != nil {
			http.Error(
				w,
				err.Error(),
				http.StatusNotFound,
			)
			return
		}

		// 4. 返回结果
		w.Header().Set(
			"Content-Type",
			"application/json",
		)

		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": "upstream removed",
			"service": service,
			"target":  target,
		})
	}
}
