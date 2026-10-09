// 本文件定义结束回执与中央端口占用模型；结束事务只读启动回执来核对命令和端口。
package finish

import (
	"database/sql"
	"time"
)

// endReceiptRecord 保存每笔订单唯一的结束回执，重复回执沿用停止命令。
// 内容不同须报冲突；计量 JSON 保留原始证据，不重新计价。
type endReceiptRecord struct {
	ChargeOrderID uint64 `gorm:"column:charge_order_id;primaryKey"` // 充电订单主键
	StopCommandID string `gorm:"column:stop_command_id"`            // 停止命令编号
	MeterJSON     []byte `gorm:"column:meter_json"`                 // 结束计量证据 JSON，不重算价格
}

// TableName 返回结束回执所属固定表名。
func (endReceiptRecord) TableName() string { return "charge_end_receipt" }

// startReceiptView 保存结束前须核对的启动信息：成功回执、同一命令与同一端口。
// 缺少任一项都报冲突，不能补造启动记录。
type startReceiptView struct {
	CommandID     string        `gorm:"column:command_id;primaryKey"` // 启动命令编号
	ChargeOrderID uint64        `gorm:"column:charge_order_id"`       // 充电订单主键
	Success       bool          `gorm:"column:success"`               // 设备成功标志，false 表示从未成功起充
	PortID        sql.NullInt64 `gorm:"column:port_id"`               // 启动占用的端口主键；回执未提供为空
}

// TableName 返回启动回执所属固定表名。
func (startReceiptView) TableName() string { return "charge_start_receipt" }

// activePortChargeRecord 映射中央业务端口占用，仅用于结束阶段按订单释放。
// 网关端口、设备会话与端口命令归 gateway；本模型不代表网关侧的端口状态。
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
