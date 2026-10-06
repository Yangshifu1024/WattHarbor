package export

import (
	"context"
	"errors"
	"log"
	"time"
)

// Cleaner 提供所属导出任务到期清理能力，调用方等待清理退出后才能释放数据库与文件资源。
type Cleaner interface {
	// CleanupExpired 在请求ctx下清理并推进已到期任务，返回首次推进数及取消／IO／存储错误。
	// 重复调用或文件已不存在不重复计数；部分推进后失败时保留已推进数量。
	CleanupExpired(context.Context) (int, error)
}

// RunCleanup 在ctx下调用cleaner启动清理一次，再每小时清理；取消后同步退出并返回nil。
// 非取消错误记录后等待下一周期，未新建goroutine，返回前清理调用已经退出。
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
