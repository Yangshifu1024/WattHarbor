package store

import "database/sql"

// 与 worker 同步端点相关的记录类型。这些表都在 gateway_db：命令由 central
// 下发、结果由设备回报，gateway 负责落库与转发，两头的视图都在这里对齐。

// SyncCommandRow 是 start-results / by-order 端点返回的启动命令列子集。
type SyncCommandRow struct {
	CommandID     string        `gorm:"column:command_id;primaryKey"`
	StopCommandID string        `gorm:"column:stop_command_id"`
	ChargeOrderID uint64        `gorm:"column:charge_order_id"`
	OrderNo       string        `gorm:"column:order_no"`
	DeviceID      string        `gorm:"column:device_id"`
	PortNo        uint8         `gorm:"column:port_no"`
	PortID        sql.NullInt64 `gorm:"column:port_id"`
	Status        string        `gorm:"column:status"`
	ResultCode    sql.NullInt16 `gorm:"column:result_code"`
	AckAt         sql.NullTime  `gorm:"column:ack_at"`
}

func (SyncCommandRow) TableName() string { return "charge_command" }

// SyncEventRow 是 end-events / mark-processed / meter-samples 的事件列子集。
type SyncEventRow struct {
	ID          uint64       `gorm:"column:id;primaryKey"`
	EventType   string       `gorm:"column:event_type"`
	EventJSON   []byte       `gorm:"column:event_json"`
	ProcessedAt sql.NullTime `gorm:"column:processed_at"`
}

func (SyncEventRow) TableName() string { return "device_event" }

// SyncPortRow 是 ports/release 的端口列子集。
type SyncPortRow struct {
	ID             uint64         `gorm:"column:id;primaryKey"`
	Status         string         `gorm:"column:status"`
	CurrentOrderID sql.NullString `gorm:"column:current_order_id"`
}

func (SyncPortRow) TableName() string { return "device_port" }

// NullInt16ToAny 把可空整数转成可写入任意值的形式：无效时写 NULL 而不是 0。
// 端口号从 1 起，0 不是合法值——把它写成 0 会让"没有端口"看起来像"0 号端口"。
func NullInt16ToAny(v sql.NullInt16) any {
	if !v.Valid {
		return nil
	}
	return v.Int16
}

// NullTimeToAny 把可空时间转成 UTC 时间或 nil。
func NullTimeToAny(v sql.NullTime) any {
	if !v.Valid {
		return nil
	}
	return v.Time.UTC()
}
