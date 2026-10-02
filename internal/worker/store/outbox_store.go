package store

import (
	"context"
	"time"

	dbstore "github.com/ChargePilot2026/charge-pilot/internal/infrastructure/database"
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
func ListPublishableOutbox(tx *gorm.DB, table string) ([]dbstore.EventOutboxRecord, error) {
	rows := []dbstore.EventOutboxRecord{}
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

// InitDLQCursor 确保某条流的重放游标存在。已存在时不做任何写入——
// 用 OnConflict DoNothing 是为了让"首次死信"这件事幂等。
func InitDLQCursor(ctx context.Context, db *gorm.DB, stream string) error {
	return db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
		Table("dlq_replay_cursor").Create(map[string]any{"stream": stream}).Error
}

// AppendDeadLetter 落一条死信。dlq_log 按 created_month 分区，月份由调用方给出。
func AppendDeadLetter(ctx context.Context, db *gorm.DB, stream, entryID, reason, payload string, month time.Time) error {
	return db.WithContext(ctx).Table("dlq_log").Create(map[string]any{
		"stream": stream, "entry_id": entryID, "reason": reason,
		"payload_json": payload, "status": "open", "created_month": month,
	}).Error
}

// ReplayCursor 是某条流的重放进度。滑动游标保证每一轮都从上次停下的地方继续，
// 不会重复扫同一批。
type ReplayCursor struct {
	LastID        *string `gorm:"column:last_id"`
	ScannedTotal  uint64  `gorm:"column:scanned_total"`
	ReplayedTotal uint64  `gorm:"column:replayed_total"`
}

// LoadReplayCursor 读回重放游标。取不到时返回零值，调用方按"从头重放"处理。
func LoadReplayCursor(ctx context.Context, db *gorm.DB, stream string) (ReplayCursor, error) {
	var cursor ReplayCursor
	err := db.WithContext(ctx).Table("dlq_replay_cursor").Where("stream = ?", stream).Take(&cursor).Error
	return cursor, err
}

// AdvanceReplayCursor 推进重放游标。lastID 为 nil 表示这一轮没扫到记录，
// 游标不前移，下一轮重扫同一位置。
func AdvanceReplayCursor(ctx context.Context, db *gorm.DB, stream string, lastID *string, scanned, replayed int) error {
	updates := map[string]any{
		"scanned_total":  gorm.Expr("scanned_total + ?", scanned),
		"replayed_total": gorm.Expr("replayed_total + ?", replayed),
	}
	if lastID != nil {
		updates["last_id"] = *lastID
	}
	return db.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).
		Table("dlq_replay_cursor").Where("stream = ?", stream).Updates(updates).Error
}
