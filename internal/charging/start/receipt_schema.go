// 本文件映射启动阶段独占的持久化记录：原启动命令回执与启动时建立的中央业务端口占用。
// 两张表的写入只发生在启动回执事务内，网关端口、设备会话与命令仍归 gateway；
// 结束阶段释放端口时使用自己的私有模型，不反向引用本包。
package start

import (
	"database/sql"
	"time"
)

// startReceiptRecord 映射原启动命令的幂等回执及可选设备事实。
// 命令身份是主键：同一命令的重复回执只能命中同一行，内容不一致由启动用例判定冲突。
// 该记录不表示支付到账，也不表示订单已进入 charging，订单状态由 order 的所属能力推进。
type startReceiptRecord struct {
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

// TableName 返回启动回执所属固定表名，无参数、错误或 IO。
func (startReceiptRecord) TableName() string { return "charge_start_receipt" }

// activePortChargeRecord 映射启动成功时建立的中央业务端口占用。
// 端口主键是主键：同一端口同一时刻只能属于一笔订单，重复回执由唯一键仲裁。
// 该占用是结束阶段按原订单释放的依据，因此迟到的结束回执不能释放别人的新占用。
type activePortChargeRecord struct {
	PortID        uint64    `gorm:"column:port_id;primaryKey"` // 原中央业务端口主键
	DeviceID      string    `gorm:"column:device_id"`          // 原设备全局身份
	PortNo        uint8     `gorm:"column:port_no"`            // 原设备端口号，从1开始
	ChargeOrderID uint64    `gorm:"column:charge_order_id"`    // 原充电订单主键
	UserID        uint64    `gorm:"column:user_id"`            // 原订单所有人
	StartedAt     time.Time `gorm:"column:started_at"`         // 原端口占用开始 UTC 时刻，持久记录不可空
}

// TableName 返回中央业务端口占用所属固定表名，无参数、错误或 IO。
func (activePortChargeRecord) TableName() string { return "active_port_charge" }
