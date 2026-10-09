// Package ops 持有运营能力与同业务事务提交的中央事件 Outbox。
package ops

import (
	"database/sql"
	"gorm.io/gorm"
)

// EventOutboxRecord 是中央和后台 Outbox 共用记录，事件与业务变更同事务保存。
// 后台额外的 stream_message_id 不由该模型读取。
// Status、ScheduledAt 使用带 default 标签的指针，让数据库填充 pending 和当前时间。
// 值类型会写入非法零值，无 default 的指针会写入 NULL；投递器更新状态时才显式赋值。
type EventOutboxRecord struct {
	ID           uint64         `gorm:"column:id;primaryKey"`                             // 队列主键
	EventID      string         `gorm:"column:event_id"`                                  // 稳定事件身份，唯一键仲裁重复追加
	Stream       string         `gorm:"column:stream"`                                    // 目标事件流名称
	EnvelopeJSON []byte         `gorm:"column:envelope_json"`                             // 业务载荷 JSON，投递不重新生成内容
	Status       *string        `gorm:"column:status;default:pending"`                    // pending 待发 / published 已发 / failed 待重试
	RetryCount   uint32         `gorm:"column:retry_count"`                               // 已重试次数，用于指数退避
	LastError    sql.NullString `gorm:"column:last_error"`                                // 最近一次失败因
	ScheduledAt  *string        `gorm:"column:scheduled_at;default:CURRENT_TIMESTAMP(3)"` // 下次可投递时间，失败退避时后推
	PublishedAt  *string        `gorm:"column:published_at"`                              // 成功投递时间；nil 表示尚未投递
}

// TableName 返回中央 Outbox 所属表，不执行查询或修改。
func (EventOutboxRecord) TableName() string { return "event_outbox" }

// AdminEventOutboxTable 指定后台事件流队列表，记录格式与中央 Outbox 相同。
const AdminEventOutboxTable = "admin_event_outbox"

// OutboxEvent 是需随业务事务提交的事件内容，不携带投递状态或 ORM 列映射。
type OutboxEvent struct {
	EventID      string // 事件幂等身份，重试不得换号
	Stream       string // 流名称，不随内部文件重命名
	EnvelopeJSON []byte // 事件 JSON，包含既有字段及单位
}

// AppendEventInTx 在业务事务中追加中央 Outbox，数据库默认状态为 pending；失败须回滚业务。
func AppendEventInTx(tx *gorm.DB, in OutboxEvent) error {
	return tx.Create(&EventOutboxRecord{EventID: in.EventID, Stream: in.Stream, EnvelopeJSON: in.EnvelopeJSON}).Error
}
