// 命令 worker 扫描待处理订单、同步设备回执、执行计费退款，并发布 Outbox 事件。
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"

	"wattharbor/internal/application/worker"
)

// main 等待中断或 SIGTERM 后退出；正常取消直接结束，其他错误记录后以退出码 1 结束。
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Print(err)
		os.Exit(1)
	}
}
