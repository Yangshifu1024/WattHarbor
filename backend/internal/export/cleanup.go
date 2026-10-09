package export

import (
	"context"
	"errors"
	"log"
	"time"
)

// Cleaner 提供到期任务清理，调用方须等待清理退出后再释放数据库和文件资源。
type Cleaner interface {
	// CleanupExpired 清理到期任务，返回首次标记数量和错误。
	// 重复清理不重复计数，部分成功后失败仍保留成功数量。
	CleanupExpired(context.Context) (int, error)
}

// RunCleanup 立即清理一次，随后每小时执行；取消后同步退出。
// 其他错误记录后等待下次，不另起协程，返回前清理已结束。
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
