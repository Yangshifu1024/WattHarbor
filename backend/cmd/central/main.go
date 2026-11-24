// 命令 central 提供内部业务接口，由 gateway 和 worker 携服务令牌调用。
// 开发和生产都不映射宿主机端口，只能从容器网络访问。
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"

	"wattharbor/internal/application/central"
)

// main 等待中断或 SIGTERM 后退出；正常取消直接结束，其他错误记录后以退出码 1 结束。
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := central.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Print(err)
		os.Exit(1)
	}
}
