package device

import (
	"context"
	"errors"
	money "github.com/ChargePilot2026/charge-pilot/internal/domain/money"
	"gorm.io/gorm"
)

// DeviceOperationReader 提供中央运营状态，不代替网关设备连通性。
type DeviceOperationReader interface {
	// DeviceStatus 在 context 下查询设备业务标识，返回状态或缺行／存储错误。
	DeviceStatus(context.Context, string) (string, error)
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
