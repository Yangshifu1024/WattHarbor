// 命令 admin 是运营后台进程：只暴露 /api/v1/admin/* 与后台自己的健康检查。
//
// 它与 central 共用 central_db，但只取后台真正需要的依赖：会话在 cache、
// 令牌用同一份 JWT_SECRET、厂商等设备能力经网关读取。微信与支付配置一概不取——
// 后台不替用户登录，也不发起支付。
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"
)

// main 无入参或返回值，建立由中断或 SIGTERM 取消的进程 context，并等待 run 完成。
// run 正常退出或返回含 context.Canceled 的错误时结束；其余错误记录后以状态码 1 退出，资源释放由 run 完成。
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Print(err)
		os.Exit(1)
	}
}
