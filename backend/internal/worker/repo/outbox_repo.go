package repo

import (
	"chargepilot/internal/ops"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 本仓储在发布事务内领取及更新事件状态队列，记录形状使用 ops.EventOutboxRecord。
// 行锁持续到调用方完成 Redis 发布及 MySQL 事务退出，不创建死信或重放游标。

// outboxBatch 一次领取多少条待发布事件。
const outboxBatch = 100

// outboxTable 按 source 选择表名：仅 admin 使用后台队列，其余来源均使用 event_outbox。
// source 的非空检查由发布用例负责；本函数不新增来源白名单。
func outboxTable(source string) string {
	if source == "admin" {
		return ops.AdminEventOutboxTable
	}
	return "event_outbox"
}

// ListPublishableOutbox 在 tx 中按 source 选择队列，返回最多 100 条到期未发布事件或读取错误。
// 保留 ID 顺序和 FOR UPDATE SKIP LOCKED；锁由调用方事务持有，不提交或延长租约。
func ListPublishableOutbox(tx *gorm.DB, source string) ([]ops.EventOutboxRecord, error) {
	rows := []ops.EventOutboxRecord{}
	err := tx.Table(outboxTable(source)).Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
		Where("status IN ('pending','failed') AND scheduled_at <= NOW(3)").
		Order("id").Limit(outboxBatch).Find(&rows).Error
	return rows, err
}

// MarkOutboxPublished 在 tx 中把 source 队列的 id 标记为 published，设置数据库时间并清空失败原因。
// 返回写入错误，不自行提交或判断 Redis 是否已经接收；主键未命中沿用零行成功行为。
func MarkOutboxPublished(tx *gorm.DB, source string, id uint64) error {
	return tx.Table(outboxTable(source)).Where("id = ?", id).
		Updates(map[string]any{
			"status": "published", "published_at": gorm.Expr("NOW(3)"), "last_error": nil,
		}).Error
}

// MarkOutboxFailed 在 tx 中更新 source 队列的 id：失败次数加 1，保存 lastError，并按 backoffSeconds 延后。
// 返回写入错误，不提交或重试；退避上限仍由发布用例计算，主键未命中沿用零行成功行为。
func MarkOutboxFailed(tx *gorm.DB, source string, id uint64, lastError string, backoffSeconds int) error {
	return tx.Table(outboxTable(source)).Where("id = ?", id).Updates(map[string]any{
		"status": "failed", "retry_count": gorm.Expr("retry_count + 1"),
		"last_error":   lastError,
		"scheduled_at": gorm.Expr("DATE_ADD(NOW(3), INTERVAL ? SECOND)", backoffSeconds),
	}).Error
}
