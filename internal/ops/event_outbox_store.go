// Package database 承载 central_db 的记录类型与查询入口。
package ops

import "database/sql"

// EventOutboxRecord 是 event_outbox 与 admin_event_outbox 两张表共用的记录类型。
//
// 两张表由 order、payment、settlement、refund 等多个聚合与后台在同一事务内共写，
// 不隶属任何单一聚合，故放在持久化层而不是某个聚合内。列形状一致，
// admin_event_outbox 多一个可空的 stream_message_id，读取时不需要它。
//
// 事件与业务写入同事务提交，是幂等重放与断线补偿的依据。
// Status / ScheduledAt 用指针并带 default 标签：新建事件时不设置它们，gorm 便省略这两列，
// 交给数据库的列默认值（status='pending'、scheduled_at=CURRENT_TIMESTAMP(3)）。
//
// 用值类型时 Go 零值会被显式写成空串，撞上枚举与 datetime 报 Data truncated；
// 用无 default 的指针则会被显式写成 NULL，报 Column cannot be null。两者都不可取。
// 这两个字段只在投递器更新投递状态时才需要写回。
type EventOutboxRecord struct {
	ID           uint64         `gorm:"column:id;primaryKey"`
	EventID      string         `gorm:"column:event_id"`
	Stream       string         `gorm:"column:stream"`
	EnvelopeJSON []byte         `gorm:"column:envelope_json"`
	Status       *string        `gorm:"column:status;default:pending"`                    // pending 待发 / published 已发 / failed 待重试
	RetryCount   uint32         `gorm:"column:retry_count"`                               // 已重试次数，用于指数退避
	LastError    sql.NullString `gorm:"column:last_error"`                                // 最近一次失败原因
	ScheduledAt  *string        `gorm:"column:scheduled_at;default:CURRENT_TIMESTAMP(3)"` // 下次可投递时间，失败退避时后推
	PublishedAt  *string        `gorm:"column:published_at"`                              // 成功投递时间；nil 表示尚未投递
}

func (EventOutboxRecord) TableName() string { return "event_outbox" }

// AdminEventOutboxTable 是后台发往事件流的队列表。
// 与 EventOutboxRecord 列形状相同，仅表名不同。
const AdminEventOutboxTable = "admin_event_outbox"
