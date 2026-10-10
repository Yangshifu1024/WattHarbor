// Package ops 管理后台认证、账号、角色、范围、公告、看板和审计，并保存与业务事务一起提交的事件。
package ops

import (
	"database/sql"
	"gorm.io/gorm"
)

// OutboxStatus 是事件 Outbox 的投递状态，对应三张取值串逐字相同的表：
// central_db.event_outbox、central_db.admin_event_outbox 与 gateway_db.event_outbox。
// 取值与三个 migrations 文件中的该 ENUM 列一致。
//
// 归属：本类型是该词表的唯一声明点。三张表跨 central 与 gateway 两个进程，
// 事件在各自库中创建、由 worker 侧统一领取推进，ops 位于 central 侧并被 worker 直接消费，
// 因此不放在 domain 层，也不由设备领域词汇承载。
type OutboxStatus string

const (
	// OutboxPending 表示待发布，由数据库默认值填充，不由 Go 侧赋值。
	OutboxPending OutboxStatus = "pending"
	// OutboxPublished 表示已发布，投递器成功后写入。
	OutboxPublished OutboxStatus = "published"
	// OutboxFailed 表示发布失败，待退避后重试。
	OutboxFailed OutboxStatus = "failed"
)

// Known 判断是否属于该列的数据库合法集合（三值）。
func (s OutboxStatus) Known() bool {
	switch s {
	case OutboxPending, OutboxPublished, OutboxFailed:
		return true
	default:
		return false
	}
}

// PublishableOutboxStatuses 是投递器领取待发布事件的取值串（pending 与 failed）。
// 该串是空格分隔的展示口径，不能直接拼进 SQL 的 IN 括号——那会得到单元素 IN ('pending failed') 而匹配不到任何行。
// SQL 绑定用 PublishableOutboxStatusValues。
const PublishableOutboxStatuses = "pending failed"

// PublishableOutboxStatusValues 是 PublishableOutboxStatuses 对应的取值切片，供 SQL 绑定使用。
var PublishableOutboxStatusValues = []OutboxStatus{OutboxPending, OutboxFailed}

// EventOutboxRecord 是中央和后台 Outbox 共用记录，事件与业务变更同事务保存。
// 后台额外的 stream_message_id 不由该模型读取。
// Status、ScheduledAt 使用带 default 标签的指针，让数据库填充 pending 和当前时间。
// 值类型会写入非法零值，无 default 的指针会写入 NULL；投递器更新状态时才显式赋值。
type EventOutboxRecord struct {
	ID           uint64         `gorm:"column:id;primaryKey"`                             // 队列主键
	EventID      string         `gorm:"column:event_id"`                                  // 稳定事件号，唯一键拒绝重复追加
	Stream       string         `gorm:"column:stream"`                                    // 目标事件流名称
	EnvelopeJSON []byte         `gorm:"column:envelope_json"`                             // 业务载荷 JSON，投递不重新生成内容
	Status       *OutboxStatus  `gorm:"column:status;default:pending"`                    // pending 待发 / published 已发 / failed 待重试；指针保留，nil 由数据库填充 pending
	RetryCount   uint32         `gorm:"column:retry_count"`                               // 已重试次数，用于指数退避
	LastError    sql.NullString `gorm:"column:last_error"`                                // 最近一次失败因
	ScheduledAt  *string        `gorm:"column:scheduled_at;default:CURRENT_TIMESTAMP(3)"` // 下次可投递时间，失败退避时后推
	PublishedAt  *string        `gorm:"column:published_at"`                              // 成功投递时间；nil 表示尚未投递
}

// TableName 指定中央事件队列表。
func (EventOutboxRecord) TableName() string { return "event_outbox" }

// AdminEventOutboxTable 指定后台事件流队列表，记录格式与中央 Outbox 相同。
const AdminEventOutboxTable = "admin_event_outbox"

// OutboxEvent 是需随业务事务提交的事件内容，不携带投递状态或 ORM 列映射。
type OutboxEvent struct {
	EventID      string // 事件号，重试不得换号
	Stream       string // 流名称，不随内部文件重命名
	EnvelopeJSON []byte // 事件 JSON，包含既有字段及单位
}

// AppendEventInTx 在业务事务中追加中央 Outbox，数据库默认状态为 pending；失败须回滚业务。
func AppendEventInTx(tx *gorm.DB, in OutboxEvent) error {
	return tx.Create(&EventOutboxRecord{EventID: in.EventID, Stream: in.Stream, EnvelopeJSON: in.EnvelopeJSON}).Error
}
