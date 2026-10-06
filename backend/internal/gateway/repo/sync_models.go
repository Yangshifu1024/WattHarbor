package repo

import "database/sql"

// worker 同步端点使用的 gateway_db 记录。central 提供启动授权，gateway
// 持久化并下发设备命令；设备回执由 gateway 保存后供 worker 推进中央业务状态。

// SyncCommandRow 是 start-results / by-order 端点返回的启动命令列子集。
type SyncCommandRow struct {
	// CommandID 原持久命令 UUID。
	CommandID string `gorm:"column:command_id;primaryKey"`
	// StopCommandID 启动补偿使用的原 STOP 命令 UUID。
	StopCommandID string `gorm:"column:stop_command_id"`
	// ChargeOrderID 中央充电订单身份。
	ChargeOrderID uint64 `gorm:"column:charge_order_id"`
	// OrderNo 原业务订单号。
	OrderNo string `gorm:"column:order_no"`
	// DeviceID 原设备业务编号。
	DeviceID string `gorm:"column:device_id"`
	// PortNo 原协议端口序号。
	PortNo uint8 `gorm:"column:port_no"`
	// PortID 原关联 device_port 自增主键，可空。
	PortID sql.NullInt64 `gorm:"column:port_id"`
	// Status 所属记录的原状态值。
	Status string `gorm:"column:status"`
	// ResultCode 原协议结果码，保留可空映射。
	ResultCode sql.NullInt16 `gorm:"column:result_code"`
	// AckAt 原设备命令确认时刻，保留可空映射。
	AckAt sql.NullTime `gorm:"column:ack_at"`
}

// TableName 返回原固定表名 charge_command，供 GORM 定位模型；无数据库 IO。
func (SyncCommandRow) TableName() string { return "charge_command" }

// SyncEventRow 是 end-events / mark-processed / meter-samples 的事件列子集。
type SyncEventRow struct {
	// ID device_event 表的原自增主键。
	ID uint64 `gorm:"column:id;primaryKey"`
	// EventType 原协议事件类型。
	EventType string `gorm:"column:event_type"`
	// EventJSON 原持久协议事件 JSON 字节。
	EventJSON []byte `gorm:"column:event_json"`
	// ProcessedAt 原事件处理标记时刻，可空。
	ProcessedAt sql.NullTime `gorm:"column:processed_at"`
}

// TableName 返回原固定表名 device_event，供 GORM 定位模型；无数据库 IO。
func (SyncEventRow) TableName() string { return "device_event" }

// SyncPortRow 是 ports/release 的端口列子集。
type SyncPortRow struct {
	// ID device_port 表的原自增主键。
	ID uint64 `gorm:"column:id;primaryKey"`
	// Status 所属记录的原状态值。
	Status string `gorm:"column:status"`
	// CurrentOrderID 原占用端口的订单号，NULL 表示无占用。
	CurrentOrderID sql.NullString `gorm:"column:current_order_id"`
}

// TableName 返回原固定表名 device_port，供 GORM 定位模型；无数据库 IO。
func (SyncPortRow) TableName() string { return "device_port" }

// NullInt16ToAny 接收可空 v，有效时返回原整数，否则返回 nil 供写入 NULL；不改变数值，无 IO。
func NullInt16ToAny(v sql.NullInt16) any {
	if !v.Valid {
		return nil
	}
	return v.Int16
}

// NullTimeToAny 接收可空 v，有效时返回 UTC 时间，否则返回 nil，无 IO。
func NullTimeToAny(v sql.NullTime) any {
	if !v.Valid {
		return nil
	}
	return v.Time.UTC()
}
