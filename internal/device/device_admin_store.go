package device

import (
	"context"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/operations"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// FindDeviceByID 在站点与厂商数据范围内按设备号取设备详情。
// 范围条件与列表共用同一组 Scope 函数，避免详情比列表看得宽。
func FindDeviceByID(ctx context.Context, db *gorm.DB, scope operations.DataScope, deviceID string) (Device, error) {
	var row Device
	query := ScopeVendors(scope, ScopeStations(scope, DeviceQuery(ctx, db), "d.station_id"), "d.vendor_id")
	err := query.Select(DeviceColumns).Where("d.device_id = ?", deviceID).Take(&row).Error
	return row, err
}

// LockDeviceByID 在行锁下按设备号取设备，写操作前用它核对归属。
func LockDeviceByID(ctx context.Context, tx *gorm.DB, scope operations.DataScope, deviceID string) (Device, error) {
	var row Device
	query := ScopeVendors(scope, ScopeStations(scope, DeviceQuery(ctx, tx), "d.station_id"), "d.vendor_id")
	err := query.Clauses(clause.Locking{Strength: "UPDATE"}).Select(DeviceColumns).
		Where("d.device_id=?", deviceID).Take(&row).Error
	return row, err
}

// SaveDeviceStatus 写入设备的运营状态与更新时间。
func SaveDeviceStatus(tx *gorm.DB, id uint64, status string, updatedAt time.Time) error {
	return tx.Table("device_meta").Where("id=?", id).
		Updates(map[string]any{"status": status, "updated_at": updatedAt}).Error
}

// SaveDeviceMetadata 写入设备可编辑的元信息。updated_at 由调用方按
// NextDeviceUpdateTime 推算后传入，保证同一秒内的连续修改仍然单调递增。
func SaveDeviceMetadata(tx *gorm.DB, id uint64, values map[string]any) error {
	return tx.Table("device_meta").Where("id=?", id).Updates(values).Error
}
