
package main

import (
	"context"
	"fmt"
	"time"
)

func worker(ctx context.Context) {

	for {
		select {

		case <-ctx.Done():

			fmt.Println("收到取消信号")
			fmt.Println("取消原因:", ctx.Err())

			return

		case <-time.After(time.Second):

			fmt.Println("Worker 正在执行任务")
		}
	}
}

func main() {

	// 创建一个可以取消的 Context
	ctx, cancel := context.WithCancel(
		context.Background(),
	)

	// 启动后台任务
	go worker(ctx)

	// 主 Goroutine 等待 3 秒
	time.Sleep(3 * time.Second)

	// 取消 Context
	fmt.Println("准备停止 Worker")

	cancel()

	// 等待一小段时间观察结果
	time.Sleep(100 * time.Millisecond)

	fmt.Println("Main 结束")
}
