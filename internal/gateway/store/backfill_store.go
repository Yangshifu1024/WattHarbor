package store

import (
	"context"
	"database/sql"
	"time"

	"gorm.io/gorm"
)

// 历史遥测补传所需的读取与写入。补传是迟到数据的补录：设备已经离线，
// 但设备端缓存里还留着当时��读数，由用户或运维触发补传。
//
// 两道防伪闸门：设备必须已开通、端口必须真实存在。补传只接受这两者，
// 否则会把无法关联硬件来源的读数写进遥测表，曲线里出现查无此端口的点。

// DeviceStatus 取设备状态。取不到返回 gorm.ErrRecordNotFound，
// 调用方据此拒绝补传。
func DeviceStatus(ctx context.Context, db *gorm.DB, deviceID string) (string, error) {
	var row struct {
		Status string
	}
	err := db.WithContext(ctx).Table("device").Select("status").
		Where("device_id = ? AND deleted_at IS NULL", deviceID).Take(&row).Error
	return row.Status, err
}

// DevicePortNumbers 返回设备已开通且未软删的端口号。
func DevicePortNumbers(ctx context.Context, db *gorm.DB, deviceID string) ([]int16, error) {
	var rows []struct {
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
	PortNo sql.NullInt16 `gorm:"column:port_no"`
	Metric string        `gorm:"column:metric"`
	TS     time.Time     `gorm:"column:ts"`
}

// ListExistingTelemetry 返回时间窗内已存在的读数键。
// 一次查完再逐条比对，避免对最多 6000 条补传逐行查重。
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

// InsertTelemetryBatch 批量写入补传读数。调用方需先完成去重。
func InsertTelemetryBatch(ctx context.Context, db *gorm.DB, rows []map[string]any) error {
	if len(rows) == 0 {
		return nil
	}
	return db.WithContext(ctx).Table("telemetry").CreateInBatches(rows, telemetryInsertBatch).Error
}
