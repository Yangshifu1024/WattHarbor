package outbox

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	workerstore "github.com/ChargePilot2026/charge-pilot/internal/worker/store"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"time"
)

type Publisher struct {
	Source string
	DB     *gorm.DB
	Stream *redis.Client
}

// PublishBatch 会一直锁住 MySQL 的行，直到 Redis 确认收到这次发布。
// 如果 MySQL 提交失败，Redis 侧可能已经收到一份重复消息；
// 消费端必须按 event_id 去重。
func (p Publisher) PublishBatch(ctx context.Context) (int, error) {
	if p.DB == nil || p.Stream == nil || p.Source == "" {
		return 0, fmt.Errorf("outbox publisher is not configured")
	}
	table := "event_outbox"
	if p.Source == "admin" {
		table = ops.AdminEventOutboxTable
	}
	published := 0
	err := p.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rows, err := workerstore.ListPublishableOutbox(tx, table)
		if err != nil {
			return err
		}
		for _, row := range rows {
			pushCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			_, pushErr := p.Stream.XAdd(pushCtx, &redis.XAddArgs{Stream: row.Stream, Values: map[string]any{
				"event_id": row.EventID, "source": p.Source, "payload": string(row.EnvelopeJSON),
			}}).Result()
			cancel()
			if pushErr != nil {
				backoff := 1 << min(row.RetryCount, 8)
				if err := workerstore.MarkOutboxFailed(tx, table, row.ID, truncate(pushErr.Error(), 255), backoff); err != nil {
					return err
				}
				continue
			}
			if err := workerstore.MarkOutboxPublished(tx, table, row.ID); err != nil {
				return err
			}
			published++
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return published, fmt.Errorf("publish outbox batch: %w", err)
	}
	return published, nil
}

func truncate(value string, max int) string {
	if len(value) > max {
		return value[:max]
	}
	return value
}

// DefaultBusinessStreams 是 worker 消费的业务流清单。原先定义在同包的
// dlq.go 里，但 DLQ 整套已删除（从未启用），清单本身却还在被 publisher
// 装配使用——它属于在跑的代码，跟着走。
var DefaultBusinessStreams = []string{
	"charge_events_stream",
	"charge_ended_stream",
	"refund_required_stream",
	"refund_succeeded_stream",
	"device_event_stream",
}
