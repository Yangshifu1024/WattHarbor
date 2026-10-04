package device

import (
	"context"
	"errors"

	"github.com/ChargePilot2026/charge-pilot/internal/domain/money"
)

// ScanReader 提供网关的连接及端口事实；context 限制请求，code 为原扫描身份。
type ScanReader interface {
	// ResolveScan 返回对应设备或端口，身份不存在及传输失败独立返回错误。
	ResolveScan(context.Context, string) (money.ScanResult, error)
}

// DiscoveryService 将网关连接事实与中央运营状态组合为匿名发现结果。
type DiscoveryService struct {
	Gateway    ScanReader            // cmd 注入的具名网关客户端
	Operations DeviceOperationReader // device 元数据读取能力，不替代物理端口状态
}

// ResolveScan 在 ctx 下查询 code，并以中央运营状态限制可用性。
// 任一依赖缺失或读取失败均返回错误，不把未知状态当作 enabled；不修改物理状态。
func (s DiscoveryService) ResolveScan(ctx context.Context, code string) (money.ScanResult, error) {
	if s.Gateway == nil || s.Operations == nil {
		return money.ScanResult{}, errors.New("device discovery is not configured")
	}
	out, err := s.Gateway.ResolveScan(ctx, code)
	if err != nil {
		return money.ScanResult{}, err
	}
	status, err := s.Operations.DeviceStatus(ctx, out.DeviceID)
	if err != nil {
		return money.ScanResult{}, err
	}
	ApplyDeviceStatus(&out, status)
	return out, nil
}
