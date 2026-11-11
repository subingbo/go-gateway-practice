package main

import (
	"fmt"
	"log"
	"net/http"
	"time"
)

func main() {
	// 创建订单服务自己的路由器
	mux := http.NewServeMux()

	// 注册查询订单接口

	mux.HandleFunc("GET /orders/slow", func(
		w http.ResponseWriter,
		r *http.Request,
	) {

		log.Println("Slow order request started")

		// 模拟一个需要 5 秒的业务任务
		select {

		case <-time.After(5 * time.Second):

			fmt.Fprintln(w, "Slow order completed")

		case <-r.Context().Done():

			// 客户端或 Gateway 取消了请求
			log.Printf(
				"Slow order canceled: %v",
				r.Context().Err(),
			)

			return
		}
	})

	// 启动订单服务
	log.Println("Order Service listening on :9091")

	log.Fatal(http.ListenAndServe(":9091", mux))
}
