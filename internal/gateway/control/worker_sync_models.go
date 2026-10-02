package control

import (
	"database/sql"
)

func nullableInt(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}

// workerSyncCommandRow 是 start-results / by-order 端点返回的启动命令列子集。
type workerSyncCommandRow struct {
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

func (workerSyncCommandRow) TableName() string { return "charge_command" }

// workerSyncEventRow 是 end-events / mark-processed / meter-samples 的事件列子集。
type workerSyncEventRow struct {
	ID          uint64       `gorm:"column:id;primaryKey"`
	EventType   string       `gorm:"column:event_type"`
	EventJSON   []byte       `gorm:"column:event_json"`
	ProcessedAt sql.NullTime `gorm:"column:processed_at"`
}

func (workerSyncEventRow) TableName() string { return "device_event" }

// workerSyncPortRow 是 ports/release 的端口列子集。
type workerSyncPortRow struct {
	ID             uint64         `gorm:"column:id;primaryKey"`
	Status         string         `gorm:"column:status"`
	CurrentOrderID sql.NullString `gorm:"column:current_order_id"`
}

func (workerSyncPortRow) TableName() string { return "device_port" }

func nullableInt16(v sql.NullInt16) any {
	if !v.Valid {
		return nil
	}
	return v.Int16
}

func nullableTime(v sql.NullTime) any {
	if !v.Valid {
		return nil
	}
	return v.Time.UTC()
}
