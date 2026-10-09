// Package outbox 将数据库中待发布事件推送到 Redis 流并记录发布状态。
package outbox

import (
	workerrepo "chargepilot/internal/worker/repo"
	"context"
	"database/sql"
	"fmt"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"time"
)

// Publisher 将数据库事件队列推送到 Redis，并保存发送结果。
type Publisher struct {
	Source string        // 原来源标识；非空即可，表选择由 worker repo 保持规则。
	DB     *gorm.DB      // 来源队列的受管理连接，行锁和状态更新共用事务。
	Stream *redis.Client // 受管理的目标事件流客户端，不由本用例关闭。
}

// PublishBatch 在 READ COMMITTED 事务内锁定待发布事件，每次 Redis 请求最多等待 3 秒。
// 失败事件记录退避时间，其余继续发布；返回已推送并更新状态的数量及事务错误。
// 提交失败仍保留本轮计数，Redis 已接收的消息可能重复发布，处理方须按 event_id 去重。
func (p Publisher) PublishBatch(ctx context.Context) (int, error) {
	if p.DB == nil || p.Stream == nil || p.Source == "" {
		return 0, fmt.Errorf("outbox publisher is not configured")
	}
	published := 0
	err := p.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rows, err := workerrepo.ListPublishableOutbox(tx, p.Source)
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
				if err := workerrepo.MarkOutboxFailed(tx, p.Source, row.ID, truncate(pushErr.Error(), 255), backoff); err != nil {
					return err
				}
				continue
			}
			if err := workerrepo.MarkOutboxPublished(tx, p.Source, row.ID); err != nil {
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

// truncate 按字节截断文本，最多保留 max 字节。
func truncate(value string, max int) string {
	if len(value) > max {
		return value[:max]
	}
	return value
}

// DefaultBusinessStreams 列出 worker 运维接口统计长度的业务流。
var DefaultBusinessStreams = []string{
	"charge_events_stream",
	"charge_ended_stream",
	"refund_required_stream",
	"refund_succeeded_stream",
	"device_event_stream",
}
