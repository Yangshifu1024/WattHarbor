// 本文件映射订单、冻结快照、回执、事件及中央端口占用记录，字段保留原持久语义。
package order

import (
	"database/sql"
	"time"
)

// ChargeOrderRecord 映射订单原生命周期、设备指令与计量；主键与原月首共同定位分区记录。
type ChargeOrderRecord struct {
	DiscountCents  int64          `gorm:"column:discount_cents"`                 // 原优惠抵扣，分
	ID             uint64         `gorm:"column:id;primaryKey"`                  // 原订单主键，与原月首共同定位分区
	OrderNo        string         `gorm:"column:order_no"`                       // 原业务充电单号
	UserID         uint64         `gorm:"column:user_id"`                        // 原订单所有人
	DeviceID       string         `gorm:"column:device_id"`                      // 原设备全局身份
	PortNo         uint8          `gorm:"column:port_no"`                        // 原设备端口号，从1开始
	PortCode       sql.NullString `gorm:"column:port_code"`                      // 原端口二维码编码，可空
	PaymentOrderID sql.NullInt64  `gorm:"column:payment_order_id"`               // 原关联支付主键，未关联为空
	Status         string         `gorm:"column:status"`                         // 原持久化生命周期，不以退款申请代替成功事实
	BusinessStatus string         `gorm:"column:business_status;->"`             // STORED generated；写入只由内部生命周期产生。
	PaymentStatus  string         `gorm:"column:payment_status;default:pending"` // 实际支付和退款结果的持久状态。
	StartedAt      sql.NullTime   `gorm:"column:started_at"`                     // 原实际启动 UTC 时刻，未开始为空
	EndedAt        sql.NullTime   `gorm:"column:ended_at"`                       // 原结束 UTC 时刻，未结束为空
	ChargedKWh     sql.NullString `gorm:"column:charged_kwh"`                    // 原累计电量，kWh，保持DECIMAL字符串；缺失为空
	ChargedSeconds sql.NullInt64  `gorm:"column:charged_seconds"`                // 原充电累计时长，秒，缺失为空
	ChargeMode     uint8          `gorm:"column:charge_mode"`                    // 原设备模式码，由协议适配器解释
	ChargeQuantity uint16         `gorm:"column:charge_quantity"`                // 原设备充电数量，单位依原模式
	FailureReason  sql.NullString `gorm:"column:failure_reason"`                 // 原失败原因，无事实为空
	CreatedMonth   time.Time      `gorm:"column:created_month;primaryKey"`       // 原记录 UTC 分区月首，重放不能换分区
}

// TableName 返回 ChargeOrderRecord 所属固定表名，无参数、错误或 IO。
func (ChargeOrderRecord) TableName() string { return "charge_order" }

// StartReceiptRecord 映射原启动命令的幂等回执及可选设备事实，不能替代支付到账。
type StartReceiptRecord struct {
	CommandID     string         `gorm:"column:command_id;primaryKey"` // 原启动命令身份，重复回执沿用
	ChargeOrderID uint64         `gorm:"column:charge_order_id"`       // 原充电订单主键
	OrderNo       sql.NullString `gorm:"column:order_no"`              // 原业务充电单号；回执缺失时为空
	DeviceID      sql.NullString `gorm:"column:device_id"`             // 原设备身份；回执缺失时为空
	PortNo        sql.NullInt16  `gorm:"column:port_no"`               // 原设备端口号，从1开始；回执缺失时为空
	PortID        sql.NullInt64  `gorm:"column:port_id"`               // 原端口主键，回执未提供为空
	Success       bool           `gorm:"column:success"`               // 设备原成功标志，不代表资金或结算完成
	ResultCode    sql.NullInt16  `gorm:"column:result_code"`           // 设备原结果码，未提供为空
	OccurredAt    sql.NullTime   `gorm:"column:occurred_at"`           // 原事件发生 UTC 时刻，回执未提供为空
}

// TableName 返回 StartReceiptRecord 所属固定表名，无参数、错误或 IO。
func (StartReceiptRecord) TableName() string { return "charge_start_receipt" }

// EndReceiptRecord 映射一单一份的原停止命令与结束计量证据，重复结束沿用原身份。
type EndReceiptRecord struct {
	ChargeOrderID uint64 `gorm:"column:charge_order_id;primaryKey"` // 原充电订单主键
	StopCommandID string `gorm:"column:stop_command_id"`            // 原停止命令身份
	MeterJSON     []byte `gorm:"column:meter_json"`                 // 原结束计量证据 JSON，不重算价格
}

// TableName 返回 EndReceiptRecord 所属固定表名，无参数、错误或 IO。
func (EndReceiptRecord) TableName() string { return "charge_end_receipt" }

// ActivePortChargeRecord 映射中央业务端口占用；网关端口与设备会话归 gateway。
type ActivePortChargeRecord struct {
	PortID        uint64    `gorm:"column:port_id;primaryKey"` // 原中央业务端口主键
	DeviceID      string    `gorm:"column:device_id"`          // 原设备全局身份
	PortNo        uint8     `gorm:"column:port_no"`            // 原设备端口号，从1开始
	ChargeOrderID uint64    `gorm:"column:charge_order_id"`    // 原充电订单主键
	UserID        uint64    `gorm:"column:user_id"`            // 原订单所有人
	StartedAt     time.Time `gorm:"column:started_at"`         // 原端口占用开始 UTC 时刻，持久记录不可空
}

// TableName 返回 ActivePortChargeRecord 所属固定表名，无参数、错误或 IO。
func (ActivePortChargeRecord) TableName() string { return "active_port_charge" }

// ChargeEventLogRecord 映射原订单事件身份、来源与发生时刻，提交顺序由所属事务保证。
type ChargeEventLogRecord struct {
	ChargeOrderID uint64    `gorm:"column:charge_order_id"` // 原充电订单主键
	EventID       string    `gorm:"column:event_id"`        // 原业务事件身份
	Event         string    `gorm:"column:event"`           // 原事件类型
	Actor         string    `gorm:"column:actor"`           // 原操作来源标签
	Detail        string    `gorm:"column:detail"`          // 原业务事件说明
	OccurredAt    time.Time `gorm:"column:occurred_at"`     // 原业务事件发生 UTC 时刻，持久记录不可空
}

// TableName 返回 ChargeEventLogRecord 所属固定表名，无参数、错误或 IO。
func (ChargeEventLogRecord) TableName() string { return "charge_event_log" }

// ChargePricingSnapshotRecord 映射原意图与完整冻结价格快照，不读取当前发布模板。
type ChargePricingSnapshotRecord struct {
	ChargeOrderID   uint64 `gorm:"column:charge_order_id;primaryKey"` // 原充电订单主键
	PaymentIntentID string `gorm:"column:payment_intent_id"`          // 原启动支付意图身份
	UserID          uint64 `gorm:"column:user_id"`                    // 原订单所有人
	PortCode        string `gorm:"column:port_code"`                  // 原端口二维码编码，可空
	PricingSnapshot []byte `gorm:"column:pricing_snapshot"`           // 原完整计价快照 JSON，不能替换为当前模板
}

// TableName 返回 ChargePricingSnapshotRecord 所属固定表名，无参数、错误或 IO。
func (ChargePricingSnapshotRecord) TableName() string { return "charge_order_pricing" }
