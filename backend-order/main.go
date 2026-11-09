package main

import (
	"fmt"
	"log"
	"net/http"
)

func main() {
	// 创建订单服务自己的路由器
	mux := http.NewServeMux()

	// 注册查询订单接口
	mux.HandleFunc("GET /orders/{id}", func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		id := r.PathValue("id")

		w.Write([]byte("Order OK\n"))

		fmt.Fprintf(w, "Service: Order\n")
		fmt.Fprintf(w, "Order ID: %s\n", id)
		fmt.Fprintf(w, "Method: %s\n", r.Method)
		fmt.Fprintf(w, "Path: %s\n", r.URL.Path)
	})

	// 启动订单服务
	log.Println("Order Service listening on :9091")

	log.Fatal(http.ListenAndServe(":9091", mux))
}
