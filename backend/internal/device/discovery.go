package device

import (
	"context"
	"errors"

	contract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/domain/money"
)

// ScanReader 提供扫描所需的网关连接和端口信息。
type ScanReader interface {
	// ResolveScan 读取扫码对应设备或端口，区分不存在和传输错误。
	ResolveScan(ctx context.Context, request contract.ScanRequest) (contract.ScanReply, error)
}

// DiscoveryService 将网关连接信息和中央运营状态合并为匿名扫码结果。
type DiscoveryService struct {
	Gateway    ScanReader            // cmd 注入的具名网关客户端
	Operations DeviceOperationReader // device 元数据读取能力，不替代物理端口状态
}

// ResolveScan 查询扫码内容，再按中央运营状态限制可用性；未知状态不能视为 enabled，物理状态保持不变。
func (s DiscoveryService) ResolveScan(ctx context.Context, code string) (money.ScanResult, error) {
	if s.Gateway == nil || s.Operations == nil {
		return money.ScanResult{}, errors.New("device discovery is not configured")
	}
	reply, err := s.Gateway.ResolveScan(ctx, contract.ScanRequest{Code: code})
	if err != nil {
		return money.ScanResult{}, err
	}
	// 先转换网关响应，再补充中央运营状态。
	out := money.ScanResult{Kind: reply.Kind, DeviceID: reply.DeviceID, StationID: reply.StationID}
	if reply.Port != nil {
		p := scanDomainPort(*reply.Port)
		out.Port = &p
	}
	if reply.Ports != nil {
		out.Ports = make([]money.ScanPort, len(reply.Ports))
		for i, p := range reply.Ports {
			out.Ports[i] = scanDomainPort(p)
		}
	}
	status, err := s.Operations.DeviceStatus(ctx, out.DeviceID)
	if err != nil {
		return money.ScanResult{}, err
	}
	ApplyDeviceStatus(&out, status)
	return out, nil
}

// scanDomainPort 转换网关端口状态，中央运营状态稍后补充。
func scanDomainPort(p contract.ScanPort) money.ScanPort {
	return money.ScanPort{PortID: p.PortID, DeviceID: p.DeviceID, PortNo: p.PortNo, Status: p.Status, Online: p.Online, Available: p.Available}
}
