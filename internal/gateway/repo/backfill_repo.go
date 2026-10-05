package repo

import (
	"context"
	"database/sql"
	"time"

	"gorm.io/gorm"
)

// 补传提供原设备、端口、既存读数查询及批量插入；原协议输入校验由控制层负责。

// DeviceStatus 接收 ctx、db 与 deviceID，读取未删除设备状态；返回原状态或缺失、SQL 错误，无写入。
func DeviceStatus(ctx context.Context, db *gorm.DB, deviceID string) (string, error) {
	var row struct {
		// Status 所属记录的原状态值。
		Status string
	}
	err := db.WithContext(ctx).Table("device").Select("status").
		Where("device_id = ? AND deleted_at IS NULL", deviceID).Take(&row).Error
	return row.Status, err
}

// DevicePortNumbers 接收 ctx、db 与 deviceID，读取未删除端口号；返回列表或 SQL 错误，无写入。
func DevicePortNumbers(ctx context.Context, db *gorm.DB, deviceID string) ([]int16, error) {
	var rows []struct {
		// PortNo 原协议端口序号。
		PortNo int16 `gorm:"column:port_no"`
	}
	err := db.WithContext(ctx).Table("device_port").Select("port_no").
		Where("device_id = ? AND deleted_at IS NULL", deviceID).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]int16, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.PortNo)
	}
	return out, nil
}

// ExistingTelemetryKey 是一条已存在的读数，用来给补传去重。
// PortNo 可空：未绑端口的读数（设备级指标）不按端口区分。
type ExistingTelemetryKey struct {
	// PortNo 原协议端口序号。
	PortNo sql.NullInt16 `gorm:"column:port_no"`
	// Metric 原遥测指标名。
	Metric string `gorm:"column:metric"`
	// TS 原采样时刻，保留所属结构的时间表示。
	TS time.Time `gorm:"column:ts"`
}

// ListExistingTelemetry 接收 ctx、db、deviceID、metrics 与 from/to，读取闭区间原读数键供调用方去重；返回键及 SQL 错误，无写入。
func ListExistingTelemetry(ctx context.Context, db *gorm.DB, deviceID string, metrics []string, from, to time.Time) ([]ExistingTelemetryKey, error) {
	rows := []ExistingTelemetryKey{}
	err := db.WithContext(ctx).Table("telemetry").
		Select("port_no, metric, ts").
		Where("device_id = ? AND metric IN ? AND ts >= ? AND ts <= ?", deviceID, metrics, from, to).
		Find(&rows).Error
	return rows, err
}

// telemetryInsertBatch 是一批插多少条读数。逐条插入在补传上千条时会拖垮事务日志。
const telemetryInsertBatch = 500

// InsertTelemetryBatch 接收 ctx、db 与 rows，按五百条批量插入原补传列值，空列表返回 nil；返回 SQL 错误，去重与事务由调用方负责。
func InsertTelemetryBatch(ctx context.Context, db *gorm.DB, rows []map[string]any) error {
	if len(rows) == 0 {
		return nil
	}
	return db.WithContext(ctx).Table("telemetry").CreateInBatches(rows, telemetryInsertBatch).Error
}
