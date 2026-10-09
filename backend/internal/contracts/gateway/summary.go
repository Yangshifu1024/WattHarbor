package gateway

import "time"

// DeviceSummariesPath 是批量设备运行摘要的实际注册端点；以服务令牌鉴权。
const DeviceSummariesPath = "/api/v1/internal/device-summaries"

// DevicePortSummary 保留原始协议读数及未知状态，不把 null 解释为空闲。
type DevicePortSummary struct {
	PortNo     uint8      `json:"port_no"`     // 物理端口号
	StatusCode *uint8     `json:"status_code"` // 最近上报的原始状态；null 表示未知，零为有效值
	StatusAt   *time.Time `json:"status_at"`   // 状态上报时间；无读数为 null
}

// DeviceSummary 是单设备运行事实，不包含 central 的运营启停状态。
type DeviceSummary struct {
	DeviceID        string              `json:"device_id"`         // 设备业务标识
	VendorName      *string             `json:"vendor_name"`       // 未删除厂商名；无可用厂商为 null
	LastHeartbeatAt *time.Time          `json:"last_heartbeat_at"` // 最后心跳；从未上报为 null
	SignalStrength  *uint8              `json:"signal_strength"`   // 信号值；null 为未知，零为有效值
	SignalAt        *time.Time          `json:"signal_at"`         // 信号采样时刻；未知为 null
	Ports           []DevicePortSummary `json:"ports"`             // 端口状态；服务端无端口时返回空数组
}

// DeviceSummariesReply 是一次授权设备集合的摘要响应；未命中的设备不补造运行事实。
type DeviceSummariesReply struct {
	Items []DeviceSummary `json:"items"` // 已找到的网关设备，可能少于请求数
}
