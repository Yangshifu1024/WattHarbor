// Package database 承载 central_db 的记录类型与查询入口。
package database

import "database/sql"

// EventOutboxRecord 是 event_outbox 与 admin_event_outbox 两张表共用的记录类型。
//
// 两张表由 order、payment、settlement、refund 等多个聚合与后台在同一事务内共写，
// 不隶属任何单一聚合，故放在持久化层而不是某个聚合内。列形状一致，
// admin_event_outbox 多一个可空的 stream_message_id，读取时不需要它。
//
// 事件与业务写入同事务提交，是幂等重放与断线补偿的依据。
type EventOutboxRecord struct {
	ID           uint64         `gorm:"column:id;primaryKey"`
	EventID      string         `gorm:"column:event_id"`
	Stream       string         `gorm:"column:stream"`
	EnvelopeJSON []byte         `gorm:"column:envelope_json"`
	Status       string         `gorm:"column:status"`       // pending 待发 / published 已发 / failed 待重试
	RetryCount   uint32         `gorm:"column:retry_count"`  // 已重试次数，用于指数退避
	LastError    sql.NullString `gorm:"column:last_error"`   // 最近一次失败原因
	ScheduledAt  string         `gorm:"column:scheduled_at"` // 下次可投递时间，失败退避时后推
	PublishedAt  *string        `gorm:"column:published_at"` // 成功投递时间；指针，未投递为 null
}

func (EventOutboxRecord) TableName() string { return "event_outbox" }

// AdminEventOutboxTable 是后台发往事件流的队列表。
// 与 EventOutboxRecord 列形状相同，仅表名不同。
const AdminEventOutboxTable = "admin_event_outbox"
