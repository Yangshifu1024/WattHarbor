package repo

import (
	"database/sql"
	"time"
)

// deviceRow 仅投影 device 自增主键，供设备登记校验与按原主键更新登记事实。
type deviceRow struct {
	// ID device 表的原自增主键。
	ID uint64 `gorm:"column:id;primaryKey"`
}

// TableName 返回原固定表名 device，供 GORM 定位模型；无数据库 IO。
func (deviceRow) TableName() string { return "device" }

// devicePortRow 映射 device_port 的端口身份、编码、业务状态及原订单占用，协议上报状态另行保存。
type devicePortRow struct {
	// ID device_port 表的原自增主键。
	ID uint64 `gorm:"column:id;primaryKey"`
	// DeviceID 原设备业务编号。
	DeviceID string `gorm:"column:device_id"`
	// PortNo 原协议端口序号。
	PortNo uint8 `gorm:"column:port_no"`
	// PortCode 原端口业务编码。
	PortCode string `gorm:"column:port_code"`
	// Status 所属记录的原状态值。
	Status string `gorm:"column:status"`
	// CurrentOrderID 原占用端口的订单号，NULL 表示无占用。
	CurrentOrderID sql.NullString `gorm:"column:current_order_id"`
}

// TableName 返回原固定表名 device_port，供 GORM 定位模型；无数据库 IO。
func (devicePortRow) TableName() string { return "device_port" }

// chargeCommandRow 映射 charge_command 的原 START 及补偿 STOP 身份、授权字段、端口归属与回执状态。
type chargeCommandRow struct {
	// ConsumerType 原协议消费类型，2 为扫码、3 为卡。
	ConsumerType uint8
	// CardNumber 原协议卡号。
	CardNumber uint32
	// CardBalanceUnits 原已持久的卡余额协议值，单位 0.1 元。
	CardBalanceUnits uint16
	// CommandID 原持久命令 UUID。
	CommandID string `gorm:"column:command_id;primaryKey"`
	// StopCommandID 启动补偿使用的原 STOP 命令 UUID。
	StopCommandID string `gorm:"column:stop_command_id"`
	// ChargeOrderID 中央充电订单身份。
	ChargeOrderID uint64 `gorm:"column:charge_order_id"`
	// PaymentOrderID 中央支付订单身份。
	PaymentOrderID uint64 `gorm:"column:payment_order_id"`
	// OrderNo 原业务订单号。
	OrderNo string `gorm:"column:order_no"`
	// UserID 原订单用户身份。
	UserID uint64 `gorm:"column:user_id"`
	// DeviceID 原设备业务编号。
	DeviceID string `gorm:"column:device_id"`
	// PortNo 原协议端口序号。
	PortNo uint8 `gorm:"column:port_no"`
	// PortCode 原端口业务编码。
	PortCode string `gorm:"column:port_code"`
	// PortID 原关联 device_port 自增主键，可空。
	PortID sql.NullInt64 `gorm:"column:port_id"`
	// OwnsPort 原命令是否持有业务端口。
	OwnsPort bool `gorm:"column:owns_port"`
	// Status 所属记录的原状态值。
	Status string `gorm:"column:status"`
	// SessionID 原 START 六字节命令会话的十六进制文本，可空；不等同于设备 TCP 连接会话。
	SessionID sql.NullString `gorm:"column:session_id"`
	// StopSessionID 原补偿 STOP 的六字节会话十六进制文本。
	StopSessionID sql.NullString `gorm:"column:stop_session_id"`
	// ResultReported 原启动结果是否已向中央报告。
	ResultReported bool `gorm:"column:result_reported"`
	// ResultCode 原协议结果码，保留可空映射。
	ResultCode sql.NullInt16 `gorm:"column:result_code"`
	// AckAt 原设备命令确认时刻，保留可空映射。
	AckAt sql.NullTime `gorm:"column:ack_at"`
	// ChargeMode 原协议充电模式。
	ChargeMode uint8 `gorm:"column:charge_mode"`
	// Quantity 原命令充电量或时长单位值。
	Quantity uint16 `gorm:"column:quantity"`
}

// TableName 返回原固定表名 charge_command，供 GORM 定位模型；无数据库 IO。
func (chargeCommandRow) TableName() string { return "charge_command" }

// chargeStopCommandRow 映射 charge_stop_command 的原用户 STOP 身份、启动关联、命令会话与设备回执。
type chargeStopCommandRow struct {
	// CommandID 原持久命令 UUID。
	CommandID string `gorm:"column:command_id;primaryKey"`
	// StartCommandID 停机对应的原 START 命令 UUID。
	StartCommandID string `gorm:"column:start_command_id"`
	// ChargeOrderID 中央充电订单身份。
	ChargeOrderID uint64 `gorm:"column:charge_order_id"`
	// OrderNo 原业务订单号。
	OrderNo string `gorm:"column:order_no"`
	// UserID 原订单用户身份。
	UserID uint64 `gorm:"column:user_id"`
	// DeviceID 原设备业务编号。
	DeviceID string `gorm:"column:device_id"`
	// PortNo 原协议端口序号。
	PortNo uint8 `gorm:"column:port_no"`
	// PortID 原关联 device_port 自增主键，可空。
	PortID sql.NullInt64 `gorm:"column:port_id"`
	// Status 所属记录的原状态值。
	Status string `gorm:"column:status"`
	// SessionID 原用户 STOP 六字节命令会话的十六进制文本，可空；重试复用该身份。
	SessionID sql.NullString `gorm:"column:session_id"`
	// ResultCode 原协议结果码，保留可空映射。
	ResultCode sql.NullInt16 `gorm:"column:result_code"`
	// RejectedAt 原用户停机被设备拒绝的时刻。
	RejectedAt sql.NullTime `gorm:"column:rejected_at"`
}

// TableName 返回原固定表名 charge_stop_command，供 GORM 定位模型；无数据库 IO。
func (chargeStopCommandRow) TableName() string { return "charge_stop_command" }

// deviceEventRow 映射 device_event 的自增主键、幂等键、原协议载荷及接收和处理时刻。
type deviceEventRow struct {
	// ID device_event 表的原自增主键。
	ID uint64 `gorm:"column:id;primaryKey"`
	// EventKey 原持久事件幂等键。
	EventKey string `gorm:"column:event_key"`
	// Protocol 所属设备或连接的原协议名称。
	Protocol string `gorm:"column:protocol_name"`
	// DeviceID 原设备业务编号。
	DeviceID string `gorm:"column:device_id"`
	// EventType 原协议事件类型。
	EventType string `gorm:"column:event_type"`
	// PortNo 原协议端口序号。
	PortNo uint8 `gorm:"column:port_no"`
	// EventJSON 原持久协议事件 JSON 字节。
	EventJSON []byte `gorm:"column:event_json"`
	// ProcessedAt 原事件处理标记时刻，可空。
	ProcessedAt sql.NullTime `gorm:"column:processed_at"`
	// ReceivedAt 原事件接收时刻。
	ReceivedAt time.Time `gorm:"column:received_at"`
}

// TableName 返回原固定表名 device_event，供 GORM 定位模型；无数据库 IO。
func (deviceEventRow) TableName() string { return "device_event" }

// deviceOutboxRow 映射 event_outbox 的原事件身份、流名和统一信封，供原投递编排使用。
type deviceOutboxRow struct {
	// EventID 原 Outbox 事件身份。
	EventID string `gorm:"column:event_id;primaryKey"`
	// Stream 原事件投递流名。
	Stream string `gorm:"column:stream"`
	// EnvelopeJSON 原 Outbox 事件信封 JSON 字节。
	EnvelopeJSON []byte `gorm:"column:envelope_json"`
}

// TableName 返回原固定表名 event_outbox，供 GORM 定位模型；无数据库 IO。
func (deviceOutboxRow) TableName() string { return "event_outbox" }

// telemetryRow 映射 telemetry 的原设备、可空端口、指标、十进制值及采样时刻插入列，不修改库内分区规则。
type telemetryRow struct {
	// DeviceID 原设备业务编号。
	DeviceID string `gorm:"column:device_id"`
	// PortNo 原协议端口序号。
	PortNo sql.NullInt16 `gorm:"column:port_no"`
	// Metric 原遥测指标名。
	Metric string `gorm:"column:metric"`
	// ValueNum 原十进制遥测数值文本。
	ValueNum string `gorm:"column:value_num"`
	// TS 原采样时刻，保留所属结构的时间表示。
	TS time.Time `gorm:"column:ts"`
}

// TableName 返回原固定表名 telemetry，供 GORM 定位模型；无数据库 IO。
func (telemetryRow) TableName() string { return "telemetry" }
