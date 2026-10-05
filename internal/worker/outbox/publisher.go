package outbox

import (
	"context"
	"database/sql"
	"fmt"
	workerrepo "github.com/ChargePilot2026/charge-pilot/internal/worker/repo"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"time"
)

// Publisher 在所属数据库事务内将原事件队列发布到 Redis，不改变事件身份或消费去重规则。
type Publisher struct {
	Source string        // 原来源标识；非空即可，表选择由 worker repo 保持原规则。
	DB     *gorm.DB      // 所属来源队列的受管理连接，行锁和状态推进共用事务。
	Stream *redis.Client // 受管理的目标事件流客户端，不由本用例关闭。
}

// PublishBatch 在 ctx 下发布一批原事件，返回已推送并推进状态的数量及配置、事务或写入错误。
// 保留 RC 事务、领取行锁、单次 Redis 三秒期限及原退避；提交失败时也保留本轮数量。
// Redis 已接收而 MySQL 提交失败可能产生重复消息，消费端仍须按 event_id 去重。
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

// truncate 按原字节上限 max 截断 value，返回原串或前缀；无 IO，不改为字符计数。
func truncate(value string, max int) string {
	if len(value) > max {
		return value[:max]
	}
	return value
}

// DefaultBusinessStreams 是 worker 运维统计使用的原业务流清单，不建立死信或重放机制。
var DefaultBusinessStreams = []string{
	"charge_events_stream",
	"charge_ended_stream",
	"refund_required_stream",
	"refund_succeeded_stream",
	"device_event_stream",
}
