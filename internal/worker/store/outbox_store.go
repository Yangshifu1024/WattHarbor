package store

import (
	"github.com/ChargePilot2026/charge-pilot/internal/ops"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// EventOutboxRecord 是事件外盒的记录形状，各 schema 的外盒表列一致，
// 复��� infrastructure/database 里的定义——它是记录结构，不是查询入口。

// 事件外盒的领取与状态推进，以及死信的落库与重放游标。
//
// 领取用 SKIP LOCKED：多副本同时扫同一张表时各拿各的，不互相排队等待。
// 这与调度用的租约是两种机制——外盒的领取窗口极短，用不着租约。

// outboxBatch 一次领取多少条待发布事件。
const outboxBatch = 100

// ListPublishableOutbox 返回到期且未发布的事件，按 ID 升序。
// SKIP LOCKED 让并发的发布者各取各的，而不是排队等同一批。
func ListPublishableOutbox(tx *gorm.DB, table string) ([]ops.EventOutboxRecord, error) {
	rows := []ops.EventOutboxRecord{}
	err := tx.Table(table).Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
		Where("status IN ('pending','failed') AND scheduled_at <= NOW(3)").
		Order("id").Limit(outboxBatch).Find(&rows).Error
	return rows, err
}

// MarkOutboxPublished 把事件标记为已发布并清掉上次失败的原因。
func MarkOutboxPublished(tx *gorm.DB, table string, id uint64) error {
	return tx.Table(table).Where("id = ?", id).
		Updates(map[string]any{
			"status": "published", "published_at": gorm.Expr("NOW(3)"), "last_error": nil,
		}).Error
}

// MarkOutboxFailed 标记一次发布失败：重试次数 +1，并按 backoff 秒推迟重试。
// 退避是 2 的 retry_count 次幂，封顶由调用方的 shift 上界控制——
// 无上限的指数退避会让一条坏事件永远排不上队。
func MarkOutboxFailed(tx *gorm.DB, table string, id uint64, lastError string, backoffSeconds int) error {
	return tx.Table(table).Where("id = ?", id).Updates(map[string]any{
		"status": "failed", "retry_count": gorm.Expr("retry_count + 1"),
		"last_error":   lastError,
		"scheduled_at": gorm.Expr("DATE_ADD(NOW(3), INTERVAL ? SECOND)", backoffSeconds),
	}).Error
}

// ReplayCursor 是某条流的重放进度。滑动游标保证每一轮都从上次停下的地方继续，
// 不会重复扫同一批。
type ReplayCursor struct {
	LastID        *string `gorm:"column:last_id"`
	ScannedTotal  uint64  `gorm:"column:scanned_total"`
	ReplayedTotal uint64  `gorm:"column:replayed_total"`
}
