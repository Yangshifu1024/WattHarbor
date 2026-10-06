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
