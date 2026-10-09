package gateway

import "time"

// 告警证据与处理标记端点由gateway提供；设备协议载荷保持原始 JSON，不在契约解析故障语义。
const (
	DeviceFaultsPath     = "/api/v1/internal/device-faults"
	MarkDeviceFaultsPath = DeviceFaultsPath + "/mark-processed"
	LatestHeartbeatPath  = "/api/v1/internal/devices/"
	MaxDeviceFaultBatch  = 100 // 每批故障查询或标记数量上限
)

// DeviceFaultReport 保存故障事件的编号、接收时间和原始内容。
type DeviceFaultReport struct {
	ID         uint64    `json:"id"`          // 事件自增 ID，不能转换为浮点数
	EventKey   string    `json:"event_key"`   // 事件幂等键
	DeviceID   string    `json:"device_id"`   // 设备编号，必须与载荷一致
	ReceivedAt time.Time `json:"received_at"` // 持久接收时刻，毫秒精度
	Payload    string    `json:"payload"`     // protocol.Event 原始 JSON，未经业务改写
}

// DeviceFaultsReply 按 ID 递增返回待处理故障；无记录时返回空数组。
type DeviceFaultsReply struct {
	Reports []DeviceFaultReport `json:"reports"` /* 原待处理事件 */
}

// MarkDeviceFaultsRequest 指定已经处理成功、需要标记的事件 ID。
type MarkDeviceFaultsRequest struct {
	IDs []uint64 `json:"ids"` /* 1至100个事件 ID */
}

// MarkDeviceFaultsReply 是本次实际首次更新条数，并发重放可为零。
type MarkDeviceFaultsReply struct {
	Marked int64 `json:"marked"` /* 实际改变的行数，非负 */
}

// HeartbeatAfterKeyReply 返回指定故障事件之后的心跳及两者的接收时间。
// 事件存在但没有后续心跳时，found=false 且 AfterReceivedAt 有值；事件不存在时该字段为空。
type HeartbeatAfterKeyReply struct {
	Found           bool       `json:"found"`                       // 是否存在接收时间晚于指定事件的心跳
	AfterReceivedAt *time.Time `json:"after_received_at,omitempty"` // 事件键或 after 指定的时间，缺失不补造
	ReceivedAt      *time.Time `json:"received_at,omitempty"`       // 最新心跳时刻，found时必须存在
	Payload         string     `json:"payload,omitempty"`           // 心跳原始 JSON，found时必须存在
}
