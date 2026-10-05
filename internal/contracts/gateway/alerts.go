package gateway

import "time"

// 告警证据与处理标记端点由gateway提供；设备协议载荷保持原JSON，不在契约解析故障语义。
const (
	DeviceFaultsPath     = "/api/v1/internal/device-faults"
	MarkDeviceFaultsPath = DeviceFaultsPath + "/mark-processed"
	LatestHeartbeatPath  = "/api/v1/internal/devices/"
	MaxDeviceFaultBatch  = 100 // 单轮原故障及处理主键上限
)

// DeviceFaultReport 保留gateway所属事件身份、原接收时刻和载荷。
type DeviceFaultReport struct {
	ID         uint64    `json:"id"`          // 原事件自增主键，不能转换为浮点数
	EventKey   string    `json:"event_key"`   // 原幂等事件键
	DeviceID   string    `json:"device_id"`   // 原持久设备身份，必须与载荷一致
	ReceivedAt time.Time `json:"received_at"` // 持久接收时刻，毫秒精度
	Payload    string    `json:"payload"`     // 原protocol.Event JSON，未经业务改写
}

// DeviceFaultData 是原升序待处理批；空批必须为空数组。
type DeviceFaultData struct {
	Reports []DeviceFaultReport `json:"reports"` /* 原待处理事件 */
}

// MarkDeviceFaultsRequest 只标记调用用例已成功处理的原主键。
type MarkDeviceFaultsRequest struct {
	IDs []uint64 `json:"ids"` /* 1至100个原事件主键 */
}

// MarkDeviceFaultsData 是本次实际首次推进条数，并发重放可为零。
type MarkDeviceFaultsData struct {
	Marked int64 `json:"marked"` /* 实际改变的行数，非负 */
}

// HeartbeatEvidence 保留原found／载荷，增加原故障水印接收时刻用于乱序核对。
// 有水印但无后续心跳时found=false且AfterReceivedAt存在；原键不存在时该时刻缺失。
type HeartbeatEvidence struct {
	Found           bool       `json:"found"`                       // 有严格晚于水印的原心跳
	AfterReceivedAt *time.Time `json:"after_received_at,omitempty"` // 原事件键或明确after的时刻，缺失不补造
	ReceivedAt      *time.Time `json:"received_at,omitempty"`       // 最新心跳时刻，found时必须存在
	Payload         string     `json:"payload,omitempty"`           // 原心跳JSON，found时必须存在
}
