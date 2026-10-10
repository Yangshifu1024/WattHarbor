package device

import (
	"context"
	"time"

	"chargepilot/internal/domain/devices"
	"chargepilot/internal/domain/operations"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// FindDeviceByID 读取当前范围内的设备资料。
func FindDeviceByID(ctx context.Context, db *gorm.DB, scope operations.DataScope, deviceID string) (Device, error) {
	var row Device
	query := ScopeVendors(scope, ScopeStations(scope, DeviceQuery(ctx, db), "d.station_id"), "d.vendor_id")
	err := query.Select(DeviceColumns).Where("d.device_id = ?", deviceID).Take(&row).Error
	return row, err
}

// LockDeviceByID 在调用方事务中锁定当前范围内的设备。
func LockDeviceByID(ctx context.Context, tx *gorm.DB, scope operations.DataScope, deviceID string) (Device, error) {
	var row Device
	query := ScopeVendors(scope, ScopeStations(scope, DeviceQuery(ctx, tx), "d.station_id"), "d.vendor_id")
	err := query.Clauses(clause.Locking{Strength: "UPDATE"}).Select(DeviceColumns).
		Where("d.device_id=?", deviceID).Take(&row).Error
	return row, err
}

// SaveDeviceStatus 保存设备运营状态和更新时间，不结束设备订单。
// 本函数原样写入调用方给定的状态，不在此收口取值集合；启停准入由用例的 ManageWritable 判定负责。
func SaveDeviceStatus(tx *gorm.DB, id uint64, status devices.DeviceMetaStatus, updatedAt time.Time) error {
	return tx.Table("device_meta").Where("id=?", id).
		Updates(map[string]any{"status": status, "updated_at": updatedAt}).Error
}

// DeviceMetadataWrite 只包含中央设备运营资料，不允许修改编号、归属或计费配置。
type DeviceMetadataWrite struct {
	Model         *string    // 型号，可空
	SerialNo      *string    // 出厂序列号，可空
	WarrantyUntil *time.Time // 保修截止，UTC 毫秒时间，可空
	TagsJSON      string     // 已校验标签的 JSON 数组
	UpdatedAt     time.Time  // 用例生成的严格递增资料版本
}

// SaveDeviceMetadata 在调用方事务中更新五项运营资料。
func SaveDeviceMetadata(tx *gorm.DB, id uint64, values DeviceMetadataWrite) error {
	return tx.Table("device_meta").Where("id=?", id).Updates(map[string]any{
		"model": values.Model, "serial_no": values.SerialNo, "warranty_until": values.WarrantyUntil,
		"tags_json": values.TagsJSON, "updated_at": values.UpdatedAt,
	}).Error
}
