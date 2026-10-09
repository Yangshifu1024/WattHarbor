package device

import (
	charging "chargepilot/internal/domain/charging"
	money "chargepilot/internal/domain/money"
	"context"
	"errors"
	"gorm.io/gorm"
)

// DeviceOperationReader 提供中央运营状态，不代替网关设备连通性。
type DeviceOperationReader interface {
	// DeviceStatus 在 context 下查询设备业务标识，返回状态或缺行／存储错误。
	DeviceStatus(context.Context, string) (string, error)
}

// DeviceCapabilityReader 提供指定归属设备的协议事实，供刷卡等消费者窄接口注入。
type DeviceCapabilityReader interface {
	// Capabilities 在 context 下按站点／设备返回适配器与领域能力，或缺失、未知协议、存储错误；只读。
	Capabilities(context.Context, uint64, string) (string, charging.Capabilities, error)
}

// Capabilities 在 ctx 下读取 station／deviceID 唯一未删除设备的原适配器并推导领域能力。
// 返回适配器、能力、缺失／协议／存储错误；不以型号、遥测或客户端声明替代原协议，不写配置。
func (s OperationRepo) Capabilities(ctx context.Context, station uint64, deviceID string) (string, charging.Capabilities, error) {
	var row struct {
		ProtocolAdapter string /* 中央创建设备时选择并冻结的协议标签 */
	}
	if err := s.DB.WithContext(ctx).Table("device_meta").Select("protocol_adapter").Where("station_id=? AND device_id=? AND deleted_at IS NULL", station, deviceID).Take(&row).Error; err != nil {
		return "", charging.Capabilities{}, err
	}
	cap, err := charging.ProtocolCapabilities(row.ProtocolAdapter)
	return row.ProtocolAdapter, cap, err
}

// CountStationDevice 在 ctx 下统计 station／deviceID 的未删除归属行，返回数量或存储错误；只读。
// 消费者核对数量恰为一后才能使用当前报价，不将无归属设备当作站点默认目标。
func (s OperationRepo) CountStationDevice(ctx context.Context, station uint64, deviceID string) (int64, error) {
	var count int64
	err := s.DB.WithContext(ctx).Table("device_meta").Where("station_id=? AND device_id=? AND deleted_at IS NULL", station, deviceID).Count(&count).Error
	return count, err
}

// OperationRepo 读取 device_meta 的运营状态。
type OperationRepo struct {
	DB *gorm.DB /* central 元数据连接，由进程管理 */
}

// DeviceStatus 使用 ctx 与 device 查询未删除元数据；缺行返回 gorm.ErrRecordNotFound。
// 返回 enabled／disabled／fault／retired 状态，不执行写入，连接缺失返回错误。
func (s OperationRepo) DeviceStatus(ctx context.Context, device string) (string, error) {
	if s.DB == nil {
		return "", errors.New("device operation storage unavailable")
	}
	var row struct {
		Status string /* 中央运营状态 */
	}
	err := s.DB.WithContext(ctx).Table("device_meta").Select("status").Where("device_id=? AND deleted_at IS NULL", device).Take(&row).Error
	return row.Status, err
}

// VendorID 在 ctx 下读取 station／deviceID 唯一未删除设备的厂商归属。
// 返回厂商 ID 或缺行／存储错误，不读取网关凭据或修改设备配置，供 pricing 等范围核对使用。
func (s OperationRepo) VendorID(ctx context.Context, station uint64, deviceID string) (uint64, error) {
	var row struct {
		VendorID uint64 /* 中央设备元数据中的厂商身份 */
	}
	err := s.DB.WithContext(ctx).Table("device_meta").Select("vendor_id").Where("station_id=? AND device_id=? AND deleted_at IS NULL", station, deviceID).Take(&row).Error
	return row.VendorID, err
}

// ApplyDeviceStatus 将 status 加入 result 的设备及端口展示。
// 只有原物理 available 且运营 enabled 时允许使用，不改写原 online 或物理状态。
// 调用方须传非 nil 结果；函数不执行 IO。
func ApplyDeviceStatus(result *money.ScanResult, status string) {
	result.DeviceStatus = status
	apply := func(p *money.ScanPort) {
		p.DeviceStatus = status
		p.Available = p.Available && status == "enabled"
	}
	if result.Port != nil {
		apply(result.Port)
	}
	for i := range result.Ports {
		apply(&result.Ports[i])
	}
}

// NewDevicesOnly 在 tx 下批量过滤 devices 中已有未删除设备，返回原顺序新设备或读取错误。
// 空输入无 IO；只供导入兼容检查选择首次设备，不覆盖已有归属、协议或计费配置。
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
