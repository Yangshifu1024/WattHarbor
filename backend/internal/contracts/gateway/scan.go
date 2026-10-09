package gateway

import "errors"

// ScanResolvePath 是二维码解析的设备和端口查询路径。
const ScanResolvePath = "/api/v1/internal/scan/resolve"

// ErrScanNotFound 表示二维码对应的设备或端口不存在，不表示设备离线。
var ErrScanNotFound = errors.New("scan identity not found")

// ScanRequest 指定待解析的二维码或端口编码。
type ScanRequest struct {
	Code string // 二维码编码，客户端转义后由服务端校验
}

// ScanPort 返回单个端口的运行信息，不包含中央运营状态。
type ScanPort struct {
	PortID    string `json:"port_id"`     // 端口业务编码
	DeviceID  string `json:"device_id"`   // 设备编号
	PortNo    uint8  `json:"port_no"`     // 物理端口号
	Status    string `json:"port_status"` // 网关记录的端口状态
	Online    bool   `json:"online"`      // 最近心跳是否处于网关规定的在线时间范围内
	Available bool   `json:"available"`   // 端口是否可用
}

// ScanReply 返回设备或单端口的扫码结果；另一种类型的字段按 omitempty 省略。
type ScanReply struct {
	Kind      string     `json:"kind"`            // device 或 port
	DeviceID  string     `json:"device_id"`       // 设备编号
	StationID uint64     `json:"station_id"`      // 网关保存的站点 ID
	Port      *ScanPort  `json:"port,omitempty"`  // 单个端口的信息；设备扫码时省略
	Ports     []ScanPort `json:"ports,omitempty"` // 设备端口；单端口扫码省略
}
