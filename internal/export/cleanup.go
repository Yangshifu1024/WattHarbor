package export

import (
	"context"
	"errors"
	"log"
	"time"
)

type Cleaner interface {
	CleanupExpired(context.Context) (int, error)
}

// RunCleanup 启动时清理一次，之后每小时清理过期导出文件；返回时清理已退出。
func RunCleanup(ctx context.Context, cleaner Cleaner) error {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		if count, err := cleaner.CleanupExpired(ctx); err != nil {
			if !errors.Is(err, context.Canceled) {
				log.Printf("export cleanup: %v", err)
			}
		} else if count > 0 {
			log.Printf("expired %d export files", count)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
