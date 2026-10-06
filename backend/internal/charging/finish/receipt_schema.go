// 本文件映射结束阶段独占的持久化记录，以及结束用例对启动回执的私有只读投影。
// 结束回执与端口释放的写入只发生在结束回执事务内；启动回执由启动用例拥有写模型，
// 本包只按原列读取成功凭据核对端口身份，两个生命周期阶段因此互不引用。
package finish

import (
	"database/sql"
	"time"
)

// endReceiptRecord 映射一单一份的原停止命令与结束计量证据。
// 订单主键是主键：一笔订单只能有一份结束凭据，重复结束沿用原命令身份，
// 内容不一致由结束用例判定冲突。计量 JSON 是计费的原始证据，不重算价格。
type endReceiptRecord struct {
	ChargeOrderID uint64 `gorm:"column:charge_order_id;primaryKey"` // 原充电订单主键
	StopCommandID string `gorm:"column:stop_command_id"`            // 原停止命令身份
	MeterJSON     []byte `gorm:"column:meter_json"`                 // 原结束计量证据 JSON，不重算价格
}

// TableName 返回结束回执所属固定表名，无参数、错误或 IO。
func (endReceiptRecord) TableName() string { return "charge_end_receipt" }

// startReceiptView 是结束用例核对启动凭据所需的最小只读投影。
// 结束前必须确认原启动回执成功、命令身份一致且占用端口与本次结束相同，
// 否则拒绝结束；缺少任何一项都按冲突处理，不据缺失元数据补造启动事实。
type startReceiptView struct {
	CommandID     string        `gorm:"column:command_id;primaryKey"` // 原启动命令身份
	ChargeOrderID uint64        `gorm:"column:charge_order_id"`       // 原充电订单主键
	Success       bool          `gorm:"column:success"`               // 原设备成功标志，false 表示从未成功起充
	PortID        sql.NullInt64 `gorm:"column:port_id"`               // 原启动占用的端口主键；回执未提供为空
}

// TableName 返回启动回执所属固定表名，无参数、错误或 IO。
func (startReceiptView) TableName() string { return "charge_start_receipt" }

// activePortChargeRecord 映射中央业务端口占用，仅用于结束阶段按原订单释放。
// 网关端口、设备会话与端口命令归 gateway；本模型不代表网关侧的端口状态。
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
