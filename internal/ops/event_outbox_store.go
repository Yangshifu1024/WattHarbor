// Package ops 持有运营能力与同业务事务提交的中央事件 Outbox。
package ops

import (
	"database/sql"
	"gorm.io/gorm"
)

// EventOutboxRecord 是 event_outbox 与 admin_event_outbox 两张表共用的记录类型。
//
// 各用例通过 ops 的事务内能力追加事件，ops 持有表模型；列形状一致，
// admin_event_outbox 多一个可空 stream_message_id，本模型不读取它。
//
// 事件与业务写入同事务提交，是幂等重放与断线补偿的依据。
// Status / ScheduledAt 用指针并带 default 标签：新建事件时不设置它们，gorm 便省略这两列，
// 交给数据库的列默认值（status='pending'、scheduled_at=CURRENT_TIMESTAMP(3)）。
//
// 用值类型时 Go 零值会被显式写成空串，撞上枚举与 datetime 报 Data truncated；
// 用无 default 的指针则会被显式写成 NULL，报 Column cannot be null。两者都不可取。
// 这两个字段只在投递器更新投递状态时才需要写回。
type EventOutboxRecord struct {
	ID           uint64         `gorm:"column:id;primaryKey"`                             // 队列主键
	EventID      string         `gorm:"column:event_id"`                                  // 稳定事件身份，唯一键仲裁重复追加
	Stream       string         `gorm:"column:stream"`                                    // 原目标事件流名称
	EnvelopeJSON []byte         `gorm:"column:envelope_json"`                             // 原业务载荷 JSON，投递不重新生成内容
	Status       *string        `gorm:"column:status;default:pending"`                    // pending 待发 / published 已发 / failed 待重试
	RetryCount   uint32         `gorm:"column:retry_count"`                               // 已重试次数，用于指数退避
	LastError    sql.NullString `gorm:"column:last_error"`                                // 最近一次失败原因
	ScheduledAt  *string        `gorm:"column:scheduled_at;default:CURRENT_TIMESTAMP(3)"` // 下次可投递时间，失败退避时后推
	PublishedAt  *string        `gorm:"column:published_at"`                              // 成功投递时间；nil 表示尚未投递
}

// TableName 返回中央 Outbox 所属表，不执行查询或修改。
func (EventOutboxRecord) TableName() string { return "event_outbox" }

// AdminEventOutboxTable 是后台发往事件流的队列表。
// 与 EventOutboxRecord 列形状相同，仅表名不同。
const AdminEventOutboxTable = "admin_event_outbox"

// OutboxEvent 是需随业务事务提交的事件内容，不携带投递状态或 ORM 列映射。
type OutboxEvent struct {
	EventID      string // 原事件幂等身份，重试不得换号
	Stream       string // 原流名称，不随内部文件重命名
	EnvelopeJSON []byte // 原事件 JSON，包含既有字段及单位
}

// AppendEventInTx 将 in 加入调用方 tx 的中央 Outbox；数据库默认设为 pending。
// 唯一键或存储错误要求回滚业务变更，不在此投递网络或提交事务。
func AppendEventInTx(tx *gorm.DB, in OutboxEvent) error {
	return tx.Create(&EventOutboxRecord{EventID: in.EventID, Stream: in.Stream, EnvelopeJSON: in.EnvelopeJSON}).Error
}
