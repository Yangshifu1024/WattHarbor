package gateway

import "errors"

// ScanResolvePath 是二维码解析的设备和端口查询路径。
const ScanResolvePath = "/api/v1/internal/scan/resolve"

// ErrScanNotFound 表示二维码对应的设备或端口不存在，不表示设备离线。
var ErrScanNotFound = errors.New("scan identity not found")

// ScanRequest 指定二维码内容或端口业务编码，不携中央权限或服务配置。
type ScanRequest struct {
	Code string // 二维码编码，客户端转义后由服务端校验
}

// ScanPort 返回单个端口的运行信息，不包含中央运营状态。
type ScanPort struct {
	PortID    string `json:"port_id"`     // 端口业务编码
	DeviceID  string `json:"device_id"`   // 设备编号
	PortNo    uint8  `json:"port_no"`     // 物理端口号
	Status    string `json:"port_status"` // 原网关端口状态
	Online    bool   `json:"online"`      // 原网关连接判定
	Available bool   `json:"available"`   // 原物理可用判定
}

// ScanReply 返回设备或单端口的扫码结果；另一种类型的字段按 omitempty 省略。
type ScanReply struct {
	Kind      string     `json:"kind"`            // device或port
	DeviceID  string     `json:"device_id"`       // 设备编号
	StationID uint64     `json:"station_id"`      // 原冗余站点主键
	Port      *ScanPort  `json:"port,omitempty"`  // 单端口事实；设备扫码省略
	Ports     []ScanPort `json:"ports,omitempty"` // 设备端口；单端口扫码省略
}
