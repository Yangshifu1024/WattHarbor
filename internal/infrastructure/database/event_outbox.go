// Package database 承载 central_db 的记录类型与查询入口。
package database

// EventOutboxRecord 是 event_outbox 表的记录类型。
//
// event_outbox 由 order、payment、settlement、refund 多个聚合在同一事务内
// 共写，不隶属任何单一聚合，故放在持久化层而不是某个聚合内。
// 事件与业务写入同事务提交，是幂等重放与断线补偿的依据。
type EventOutboxRecord struct {
	ID           uint64 `gorm:"column:id;primaryKey"`
	EventID      string `gorm:"column:event_id"`
	Stream       string `gorm:"column:stream"`
	EnvelopeJSON []byte `gorm:"column:envelope_json"`
}

func (EventOutboxRecord) TableName() string { return "event_outbox" }
