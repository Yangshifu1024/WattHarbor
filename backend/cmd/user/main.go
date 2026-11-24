// 命令 user 提供 C 端接口和支付回调。
// 充电订单采样、曲线、采样和设备摘要通过 GATEWAY_INTERNAL_URL 查询 gateway 的内部接口，并携服务令牌。
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"

	"chargepilot/internal/application/user"
)

// main 等待中断或 SIGTERM 后退出；正常取消直接结束，其他错误记录后以退出码 1 结束。
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := user.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Print(err)
		os.Exit(1)
	}
}
