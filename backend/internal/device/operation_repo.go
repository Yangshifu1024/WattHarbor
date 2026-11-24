package device

import (
	"context"
	"errors"
	"gorm.io/gorm"
	charging "wattharbor/internal/domain/charging"
	"wattharbor/internal/domain/devices"
	money "wattharbor/internal/domain/money"
	"wattharbor/internal/protocol"
)

// DeviceOperationReader 读取中央运营状态，不能代替网关连接状态。
type DeviceOperationReader interface {
	// DeviceStatus 按设备号查询运营状态。
	DeviceStatus(context.Context, string) (devices.DeviceMetaStatus, error)
}

// DeviceCapabilityReader 查询站点所属设备的协议能力，供刷卡等用例调用。
type DeviceCapabilityReader interface {
	// Capabilities 返回设备适配器和支持的功能；协议未知时报错。
	Capabilities(context.Context, uint64, string) (protocol.AdapterID, charging.Capabilities, error)
}

// Capabilities 按站点和设备号读取唯一未删除设备，通过已登记协议计算支持的功能。
// 不能用型号、遥测或客户端声明代替协议。
func (s OperationRepo) Capabilities(ctx context.Context, station uint64, deviceID string) (protocol.AdapterID, charging.Capabilities, error) {
	var row struct {
		ProtocolAdapter protocol.AdapterID /* 中央创建设备时选择并冻结的协议标签 */
	}
	if err := s.DB.WithContext(ctx).Table("device_meta").Select("protocol_adapter").Where("station_id=? AND device_id=? AND deleted_at IS NULL", station, deviceID).Take(&row).Error; err != nil {
		return "", charging.Capabilities{}, err
	}
	cap, err := charging.ProtocolCapabilities(row.ProtocolAdapter)
	return row.ProtocolAdapter, cap, err
}

// CountStationDevice 统计站点所属的未删除设备；只有恰好一条时才能报价，缺设备不能套用站点默认规则。
func (s OperationRepo) CountStationDevice(ctx context.Context, station uint64, deviceID string) (int64, error) {
	var count int64
	err := s.DB.WithContext(ctx).Table("device_meta").Where("station_id=? AND device_id=? AND deleted_at IS NULL", station, deviceID).Count(&count).Error
	return count, err
}

// OperationRepo 读取 device_meta 的运营状态。
type OperationRepo struct {
	DB *gorm.DB /* central 元数据连接，由进程管理 */
}

// DeviceStatus 读取未删除设备的 enabled／disabled／fault／retired 状态；缺失返回 gorm.ErrRecordNotFound。
func (s OperationRepo) DeviceStatus(ctx context.Context, device string) (devices.DeviceMetaStatus, error) {
	if s.DB == nil {
		return "", errors.New("device operation storage unavailable")
	}
	var row struct {
		Status devices.DeviceMetaStatus /* 中央运营状态，对应 device_meta.status */
	}
	err := s.DB.WithContext(ctx).Table("device_meta").Select("status").Where("device_id=? AND deleted_at IS NULL", device).Take(&row).Error
	return row.Status, err
}

// VendorID 读取指定站点所属设备的厂商 ID，供计费范围检查。
func (s OperationRepo) VendorID(ctx context.Context, station uint64, deviceID string) (uint64, error) {
	var row struct {
		VendorID uint64 /* 中央设备元数据中的厂商 ID */
	}
	err := s.DB.WithContext(ctx).Table("device_meta").Select("vendor_id").Where("station_id=? AND device_id=? AND deleted_at IS NULL", station, deviceID).Take(&row).Error
	return row.VendorID, err
}

// ApplyDeviceStatus 补充设备和端口的运营状态；仅物理 available 且运营 enabled 时允许使用。
// 连接和物理状态保持不变，调用方须传非 nil 结果。
func ApplyDeviceStatus(result *money.ScanResult, status devices.DeviceMetaStatus) {
	result.DeviceStatus = status
	apply := func(p *money.ScanPort) {
		p.DeviceStatus = status
		p.Available = p.Available && status == devices.DeviceEnabled
	}
	if result.Port != nil {
		apply(result.Port)
	}
	for i := range result.Ports {
		apply(&result.Ports[i])
	}
}

// NewDevicesOnly 排除已有未删除设备，按输入顺序返回新设备，供导入计费兼容检查；空清单不查询。
func (s OperationRepo) NewDevicesOnly(tx *gorm.DB, devices []ImportDevice) ([]ImportDevice, error) {
	if len(devices) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(devices))
	for _, d := range devices {
		ids = append(ids, d.DeviceID)
	}
	var known []string
	if err := tx.Table("device_meta").Where("device_id IN ? AND deleted_at IS NULL", ids).
		Pluck("device_id", &known).Error; err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(known))
	for _, id := range known {
		seen[id] = true
	}
	arriving := make([]ImportDevice, 0, len(devices))
	for _, d := range devices {
		if !seen[d.DeviceID] {
			arriving = append(arriving, d)
		}
	}
	return arriving, nil
}
