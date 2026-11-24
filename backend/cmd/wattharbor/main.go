// 命令 wattharbor 运行单进程部署。
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"wattharbor/internal/application/monolith"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := monolith.Run(ctx); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
