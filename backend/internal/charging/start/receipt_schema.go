// Package start 校验启动订单，保存设备启动回执；成功后更新订单和端口占用，失败时退款。
package start

import (
	"database/sql"
	"time"
)

// startReceiptRecord 以命令编号保存唯一启动回执，内容不同须报冲突。
// 支付与订单状态由各自模块处理。
type startReceiptRecord struct {
	CommandID     string         `gorm:"column:command_id;primaryKey"` // 启动命令编号，重复回执沿用
	ChargeOrderID uint64         `gorm:"column:charge_order_id"`       // 充电订单主键
	OrderNo       sql.NullString `gorm:"column:order_no"`              // 业务充电单号；回执缺失时为空
	DeviceID      sql.NullString `gorm:"column:device_id"`             // 设备编号；回执缺失时为空
	PortNo        sql.NullInt16  `gorm:"column:port_no"`               // 设备端口号，从 1 开始；回执缺失时为空
	PortID        sql.NullInt64  `gorm:"column:port_id"`               // 端口主键，回执未提供为空
	Success       bool           `gorm:"column:success"`               // 设备成功标志，不代表资金或结算完成
	ResultCode    sql.NullInt16  `gorm:"column:result_code"`           // 设备结果码，未提供为空
	OccurredAt    sql.NullTime   `gorm:"column:occurred_at"`           // 事件发生 UTC 时刻，回执未提供为空
}

// TableName 返回启动回执所属固定表名。
func (startReceiptRecord) TableName() string { return "charge_start_receipt" }

// activePortChargeRecord 保存启动成功后的端口占用，同一端口只能属于一笔订单。
// 结束时必须匹配订单，防止迟到回执释放新占用。
type activePortChargeRecord struct {
	PortID        uint64    `gorm:"column:port_id;primaryKey"` // 中央业务端口主键
	DeviceID      string    `gorm:"column:device_id"`          // 设备全局编号
	PortNo        uint8     `gorm:"column:port_no"`            // 设备端口号，从 1 开始
	ChargeOrderID uint64    `gorm:"column:charge_order_id"`    // 充电订单主键
	UserID        uint64    `gorm:"column:user_id"`            // 订单所有人
	StartedAt     time.Time `gorm:"column:started_at"`         // 端口占用开始 UTC 时刻，持久记录不可空
}

// TableName 返回中央业务端口占用所属固定表名。
func (activePortChargeRecord) TableName() string { return "active_port_charge" }
