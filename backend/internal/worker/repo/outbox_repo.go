// Package repo 提供 worker 定时任务、执行日志和事件发布队列的表操作。
package repo

import (
	"chargepilot/internal/ops"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 发布事务通过本仓储领取事件、保存发布状态和失败次数。
// 行锁持续到调用方完成 Redis 发布及 MySQL 事务退出，不创建死信或重放游标。

// outboxBatch 一次领取多少条待发布事件。
const outboxBatch = 100

// outboxTable 按 source 选择表名：仅 admin 使用后台队列，其余来源均使用 event_outbox。
// source 的非空检查由发布用例负责；本函数不新增来源白名单。
func outboxTable(source string) string {
	if source == "gateway_unified" {
		return "gateway_event_outbox"
	}
	if source == "admin" {
		return ops.AdminEventOutboxTable
	}
	return "event_outbox"
}

// ListPublishableOutbox 按 ID 领取最多 100 条到期待发布事件，跳过其他事务已锁定的行。
// 调用方须使用事务，行锁持续到该事务结束。
// status 的取值绑定 ops.PublishableOutboxStatusValues（对应三张 outbox 表的 status 词表）；
// 不把展示用白名单串拼进 IN 括号，那会得到单元素列表而匹配不到行。
func ListPublishableOutbox(tx *gorm.DB, source string) ([]ops.EventOutboxRecord, error) {
	rows := []ops.EventOutboxRecord{}
	err := tx.Table(outboxTable(source)).Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
		Where("status IN ? AND scheduled_at <= NOW(3)", ops.PublishableOutboxStatusValues).
		Order("id").Limit(outboxBatch).Find(&rows).Error
	return rows, err
}

// MarkOutboxPublished 在调用方事务内标记事件已发布，记录时间并清空错误。
func MarkOutboxPublished(tx *gorm.DB, source string, id uint64) error {
	return tx.Table(outboxTable(source)).Where("id = ?", id).
		Updates(map[string]any{
			"status": ops.OutboxPublished, "published_at": gorm.Expr("NOW(3)"), "last_error": nil,
		}).Error
}

// MarkOutboxFailed 在调用方事务内记录发送错误、增加重试次数，并延后指定秒数。
func MarkOutboxFailed(tx *gorm.DB, source string, id uint64, lastError string, backoffSeconds int) error {
	return tx.Table(outboxTable(source)).Where("id = ?", id).Updates(map[string]any{
		"status": ops.OutboxFailed, "retry_count": gorm.Expr("retry_count + 1"),
		"last_error":   lastError,
		"scheduled_at": gorm.Expr("DATE_ADD(NOW(3), INTERVAL ? SECOND)", backoffSeconds),
	}).Error
}
