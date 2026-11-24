package repo

import (
	"chargepilot/internal/domain/devices"
	"chargepilot/internal/domain/money"
	"chargepilot/internal/protocol"
	"database/sql"
	"time"
)

// deviceRow 读取设备自增 ID，供登记校验和更新设备资料。
type deviceRow struct {
	// ID device 表的自增主键。
	ID uint64 `gorm:"column:id;primaryKey"`
}

// TableName 返回固定表名 device，供 GORM 定位模型。
func (deviceRow) TableName() string { return "device" }

// devicePortRow 映射 device_port 的端口编号、编码、业务状态及订单占用，协议上报状态另行保存。
type devicePortRow struct {
	// ID device_port 表的自增主键。
	ID uint64 `gorm:"column:id;primaryKey"`
	// DeviceID 设备业务编号。
	DeviceID string `gorm:"column:device_id"`
	// PortNo 协议端口序号。
	PortNo uint8 `gorm:"column:port_no"`
	// PortCode 端口业务编码。
	PortCode string `gorm:"column:port_code"`
	// Status 状态；词表见 domain/money.PortStatus。
	Status money.PortStatus `gorm:"column:status"`
	// CurrentOrderID 占用端口的订单号，NULL 表示无占用。
	CurrentOrderID sql.NullString `gorm:"column:current_order_id"`
}

// TableName 返回固定表名 device_port，供 GORM 定位模型。
func (devicePortRow) TableName() string { return "device_port" }

// chargeCommandRow 映射 charge_command 的 START 及补偿 STOP 身份、授权字段、端口归属与回执状态。
type chargeCommandRow struct {
	// ConsumerType 协议消费类型，2 为扫码、3 为卡。
	ConsumerType uint8
	// CardNumber 协议卡号。
	CardNumber uint32
	// CardBalanceUnits 已持久的卡余额协议值，单位 0.1 元。
	CardBalanceUnits uint16
	// CommandID 持久命令 UUID。
	CommandID string `gorm:"column:command_id;primaryKey"`
	// StopCommandID 启动补偿使用的 STOP 命令 UUID。
	StopCommandID string `gorm:"column:stop_command_id"`
	// ChargeOrderID 中央充电订单编号。
	ChargeOrderID uint64 `gorm:"column:charge_order_id"`
	// PaymentOrderID 中央支付订单编号。
	PaymentOrderID uint64 `gorm:"column:payment_order_id"`
	// OrderNo 业务订单号。
	OrderNo string `gorm:"column:order_no"`
	// UserID 订单用户编号。
	UserID uint64 `gorm:"column:user_id"`
	// DeviceID 设备业务编号。
	DeviceID string `gorm:"column:device_id"`
	// PortNo 协议端口序号。
	PortNo uint8 `gorm:"column:port_no"`
	// PortCode 端口业务编码。
	PortCode string `gorm:"column:port_code"`
	// PortID 关联 device_port 自增主键，可空。
	PortID sql.NullInt64 `gorm:"column:port_id"`
	// OwnsPort 命令是否持有业务端口。
	OwnsPort bool `gorm:"column:owns_port"`
	// Status 命令持久化状态，对应 charge_command.status。
	Status devices.ChargeCommandStatus `gorm:"column:status"`
	// SessionID START 六字节命令会话的十六进制文本，可空；不等同于设备 TCP 连接会话。
	SessionID sql.NullString `gorm:"column:session_id"`
	// StopSessionID 补偿 STOP 的六字节会话十六进制文本。
	StopSessionID sql.NullString `gorm:"column:stop_session_id"`
	// ResultReported 启动结果是否已向中央报告。
	ResultReported bool `gorm:"column:result_reported"`
	// ResultCode 协议结果码，保留可空映射。
	ResultCode sql.NullInt16 `gorm:"column:result_code"`
	// AckAt 设备命令确认时刻，保留可空映射。
	AckAt sql.NullTime `gorm:"column:ack_at"`
	// ChargeMode 协议充电模式。
	ChargeMode uint8 `gorm:"column:charge_mode"`
	// Quantity 命令充电量或时长单位值。
	Quantity uint16 `gorm:"column:quantity"`
}

// TableName 返回固定表名 charge_command，供 GORM 定位模型。
func (chargeCommandRow) TableName() string { return "charge_command" }

// chargeStopCommandRow 映射 charge_stop_command 的用户 STOP 身份、启动关联、命令会话与设备回执。
type chargeStopCommandRow struct {
	// CommandID 持久命令 UUID。
	CommandID string `gorm:"column:command_id;primaryKey"`
	// StartCommandID 停机对应的 START 命令 UUID。
	StartCommandID string `gorm:"column:start_command_id"`
	// ChargeOrderID 中央充电订单编号。
	ChargeOrderID uint64 `gorm:"column:charge_order_id"`
	// OrderNo 业务订单号。
	OrderNo string `gorm:"column:order_no"`
	// UserID 订单用户编号。
	UserID uint64 `gorm:"column:user_id"`
	// DeviceID 设备业务编号。
	DeviceID string `gorm:"column:device_id"`
	// PortNo 协议端口序号。
	PortNo uint8 `gorm:"column:port_no"`
	// PortID 关联 device_port 自增主键，可空。
	PortID sql.NullInt64 `gorm:"column:port_id"`
	// Status 命令持久化状态，对应 charge_stop_command.status；该列没有 stopping。
	Status devices.StopCommandStatus `gorm:"column:status"`
	// SessionID 用户 STOP 六字节命令会话的十六进制文本，可空；重试复用该身份。
	SessionID sql.NullString `gorm:"column:session_id"`
	// ResultCode 协议结果码，保留可空映射。
	ResultCode sql.NullInt16 `gorm:"column:result_code"`
	// RejectedAt 用户停机被设备拒绝的时刻。
	RejectedAt sql.NullTime `gorm:"column:rejected_at"`
}

// TableName 返回固定表名 charge_stop_command，供 GORM 定位模型。
func (chargeStopCommandRow) TableName() string { return "charge_stop_command" }

// deviceEventRow 映射 device_event 的自增主键、幂等键、协议载荷及接收和处理时刻。
type deviceEventRow struct {
	// ID device_event 表的自增主键。
	ID uint64 `gorm:"column:id;primaryKey"`
	// EventKey 持久事件幂等键。
	EventKey string `gorm:"column:event_key"`
	// Protocol 上报事件的设备协议适配器，对应 device_event.protocol_name。
	Protocol protocol.AdapterID `gorm:"column:protocol_name"`
	// DeviceID 设备业务编号。
	DeviceID string `gorm:"column:device_id"`
	// EventType 协议事件类型，对应 device_event.event_type。
	EventType protocol.EventType `gorm:"column:event_type"`
	// PortNo 协议端口序号。
	PortNo uint8 `gorm:"column:port_no"`
	// EventJSON 持久协议事件 JSON 字节。
	EventJSON []byte `gorm:"column:event_json"`
	// ProcessedAt 事件处理标记时刻，可空。
	ProcessedAt sql.NullTime `gorm:"column:processed_at"`
	// ReceivedAt 事件接收时刻。
	ReceivedAt time.Time `gorm:"column:received_at"`
}

// TableName 返回固定表名 device_event，供 GORM 定位模型。
func (deviceEventRow) TableName() string { return "device_event" }

// deviceOutboxRow 映射 event_outbox 的事件标识、流名和统一事件结构，供投递编排使用。
type deviceOutboxRow struct {
	// EventID Outbox 事件标识。
	EventID string `gorm:"column:event_id;primaryKey"`
	// Stream 事件投递流名。
	Stream string `gorm:"column:stream"`
	// EnvelopeJSON Outbox 事件结构 JSON 字节。
	EnvelopeJSON []byte `gorm:"column:envelope_json"`
}

// TableName 返回固定表名 event_outbox，供 GORM 定位模型。
func (deviceOutboxRow) TableName() string { return "event_outbox" }
