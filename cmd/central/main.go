// 命令 central 是对内进程：只暴露 /api/v1/internal/*，不直接对外。
//
// 这些端点由 gateway（设备侧授权）与 worker（结果回传、计费退款派发）
// 携服务令牌调用。生产编排里本进程不映射端口，
// 对外入口只有 Caddy；开发栈同样不映射，保持隔离策略一致。
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Print(err)
		os.Exit(1)
	}
}
