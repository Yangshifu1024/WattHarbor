package device

import (
	"context"
	"errors"

	contract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/domain/money"
)

// ScanReader 提供网关的连接及端口事实；ctx限制请求，request携原扫描身份。
type ScanReader interface {
	// ResolveScan 返回对应设备或端口，身份不存在及传输失败独立返回错误。
	ResolveScan(ctx context.Context, request contract.ScanRequest) (contract.ScanReply, error)
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
	reply, err := s.Gateway.ResolveScan(ctx, contract.ScanRequest{Code: code})
	if err != nil {
		return money.ScanResult{}, err
	}
	// 传输DTO显式转换为领域发现结果，中央运营状态由随后所属读取补齐。
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

// scanDomainPort 将p的原物理事实映射为领域端口，中央运营状态暂空，无IO。
func scanDomainPort(p contract.ScanPort) money.ScanPort {
	return money.ScanPort{PortID: p.PortID, DeviceID: p.DeviceID, PortNo: p.PortNo, Status: p.Status, Online: p.Online, Available: p.Available}
}
