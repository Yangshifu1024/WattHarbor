// 命令 admin 提供后台接口和健康检查，与 central 共用 central_db。
// 会话使用缓存 Redis 和 JWT_SECRET，设备信息经 gateway 查询；不读取微信或支付凭据。
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"

	"chargepilot/internal/application/admin"
)

// main 等待中断或 SIGTERM 后退出；正常取消直接结束，其他错误记录后以退出码 1 结束。
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := admin.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Print(err)
		os.Exit(1)
	}
}
