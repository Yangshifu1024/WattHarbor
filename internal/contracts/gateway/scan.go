package gateway

import "errors"

// ScanResolvePath 是原设备／端口发现端点，保持二维码发现 URL 契约。
const ScanResolvePath = "/api/v1/internal/scan/resolve"

// ErrScanNotFound 表示网关已响应但扫描身份不存在，不代表设备离线。
var ErrScanNotFound = errors.New("scan identity not found")

// ScanRequest 是原二维码／端口业务编码的查询，不带中央权限或服务配置。
type ScanRequest struct {
	Code string // 原编码，客户端转义、服务端按既有规则校验
}

// ScanPort 是单个物理端口的原发现事实，不包含中央运营状态。
type ScanPort struct {
	PortID    string `json:"port_id"`     // 原端口业务编码
	DeviceID  string `json:"device_id"`   // 原设备身份
	PortNo    uint8  `json:"port_no"`     // 原物理端口号
	Status    string `json:"port_status"` // 原网关端口状态
	Online    bool   `json:"online"`      // 原网关连接判定
	Available bool   `json:"available"`   // 原物理可用判定
}

// ScanReply 是网关原扫码响应，设备／单端口两种形状按原omitempty保留。
type ScanReply struct {
	Kind      string     `json:"kind"`            // device或port
	DeviceID  string     `json:"device_id"`       // 原设备身份
	StationID uint64     `json:"station_id"`      // 原冗余站点主键
	Port      *ScanPort  `json:"port,omitempty"`  // 单端口事实；设备扫码省略
	Ports     []ScanPort `json:"ports,omitempty"` // 设备端口；单端口扫码省略
}
