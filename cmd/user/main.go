// 命令 user 是 C 端进程：只暴露 /api/v1/user/* 与支付回调，不含任何服务间接口。
//
// 充电过程中的实时读数（遥测、历史曲线、充电采样、设备摘要）由 gateway 的
// /api/v1/internal/devices/* 提供，这里经 GATEWAY_INTERNAL_URL 携服务令牌回调。
// 这些端点不在 central 进程里，改动时不要按路径里的 /internal 误判调用对象。
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
