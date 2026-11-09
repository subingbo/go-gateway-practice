package main

import (
	"fmt"
	"log"
	"net/http"
)

func main() {
	// 创建用户服务自己的路由器
	mux := http.NewServeMux()

	// 注册查询用户接口
	mux.HandleFunc("GET /users/{id}", func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		id := r.PathValue("id")

		fmt.Fprintf(w, "Service: User\n")
		fmt.Fprintf(w, "User ID: %s\n", id)
		fmt.Fprintf(w, "Method: %s\n", r.Method)
		fmt.Fprintf(w, "Path: %s\n", r.URL.Path)
	})

	// 启动用户服务
	log.Println("User Service listening on :9090")

	log.Fatal(http.ListenAndServe(":9090", mux))
}
