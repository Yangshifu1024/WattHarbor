// 本文件映射订单、冻结快照、回执、事件及中央端口占用记录，字段保留存储含义。
package order

import (
	"database/sql"
	"time"
)

// ChargeOrderRecord 映射订单生命周期、设备指令与计量；主键与月首共同定位分区记录。
type ChargeOrderRecord struct {
	DiscountCents  int64          `gorm:"column:discount_cents"`                 // 优惠抵扣，分
	ID             uint64         `gorm:"column:id;primaryKey"`                  // 订单主键，与月首共同定位分区
	OrderNo        string         `gorm:"column:order_no"`                       // 业务充电单号
	UserID         uint64         `gorm:"column:user_id"`                        // 订单所有人
	DeviceID       string         `gorm:"column:device_id"`                      // 设备全局编号
	PortNo         uint8          `gorm:"column:port_no"`                        // 设备端口号，从 1 开始
	PortCode       sql.NullString `gorm:"column:port_code"`                      // 端口二维码编码，可空
	PaymentOrderID sql.NullInt64  `gorm:"column:payment_order_id"`               // 关联支付主键，未关联为空
	Status         string         `gorm:"column:status"`                         // 订单状态；refunding 表示申请中，refunded 表示已退款
	BusinessStatus string         `gorm:"column:business_status;->"`             // STORED generated；写入只由内部生命周期产生。
	PaymentStatus  string         `gorm:"column:payment_status;default:pending"` // 实际支付和退款结果的持久状态。
	StartedAt      sql.NullTime   `gorm:"column:started_at"`                     // 实际启动 UTC 时刻，未开始为空
	EndedAt        sql.NullTime   `gorm:"column:ended_at"`                       // 结束 UTC 时刻，未结束为空
	ChargedKWh     sql.NullString `gorm:"column:charged_kwh"`                    // 累计电量，kWh，保持DECIMAL字符串；缺失为空
	ChargedSeconds sql.NullInt64  `gorm:"column:charged_seconds"`                // 充电累计时长，秒，缺失为空
	ChargeMode     uint8          `gorm:"column:charge_mode"`                    // 设备模式码，由协议适配器解释
	ChargeQuantity uint16         `gorm:"column:charge_quantity"`                // 设备充电数量，单位依模式
	FailureReason  sql.NullString `gorm:"column:failure_reason"`                 // 失败原因，未提供时为空
	CreatedMonth   time.Time      `gorm:"column:created_month;primaryKey"`       // 记录 UTC 分区月首，重放不能换分区
}

// TableName 返回 ChargeOrderRecord 所属固定表名。
func (ChargeOrderRecord) TableName() string { return "charge_order" }

// ChargeEventLogRecord 映射订单事件标识、来源与发生时刻，提交顺序由所属事务保证。
type ChargeEventLogRecord struct {
	ChargeOrderID uint64    `gorm:"column:charge_order_id"` // 充电订单主键
	EventID       string    `gorm:"column:event_id"`        // 业务事件编号
	Event         string    `gorm:"column:event"`           // 事件类型
	Actor         string    `gorm:"column:actor"`           // 首次操作来源标签
	Detail        string    `gorm:"column:detail"`          // 业务事件说明
	OccurredAt    time.Time `gorm:"column:occurred_at"`     // 业务事件发生 UTC 时刻，持久记录不可空
}

// TableName 返回 ChargeEventLogRecord 所属固定表名。
func (ChargeEventLogRecord) TableName() string { return "charge_event_log" }

// ChargePricingSnapshotRecord 映射支付意图与完整冻结价格快照，不读取当前发布模板。
type ChargePricingSnapshotRecord struct {
	ChargeOrderID   uint64 `gorm:"column:charge_order_id;primaryKey"` // 充电订单主键
	PaymentIntentID string `gorm:"column:payment_intent_id"`          // 关联支付申请 UUID
	UserID          uint64 `gorm:"column:user_id"`                    // 订单所有人
	PortCode        string `gorm:"column:port_code"`                  // 端口二维码编码，可空
	PricingSnapshot []byte `gorm:"column:pricing_snapshot"`           // 完整计价快照 JSON，不能替换为当前模板
}

// TableName 返回 ChargePricingSnapshotRecord 所属固定表名。
func (ChargePricingSnapshotRecord) TableName() string { return "charge_order_pricing" }
