package gateway

import "time"

const (
	StartResultsPath      = "/api/v1/internal/start-results"
	MarkStartResultsPath  = "/api/v1/internal/start-results/mark-reported"
	EndEventsPath         = "/api/v1/internal/end-events"
	ChargeCommandRoute    = "/api/v1/internal/charge-commands/by-order/:order_id"
	MarkDeviceEventsPath  = "/api/v1/internal/device-events/mark-processed"
	ReleasePortPath       = "/api/v1/internal/ports/release"
	MeterSamplesRoute     = "/api/v1/internal/devices/:device_id/meter-samples"
	FreezeEndDeliveryPath = "/api/v1/internal/charge-end-deliveries/freeze"
	CompensateStartRoute  = "/api/v1/internal/charge-orders/:order_no/compensate"
)

// SyncCommand 保留 nullable 端口、结果码与回执时间，不把缺失证据解码成零值。
type SyncCommand struct {
	CommandID     string     `json:"command_id"`
	StopCommandID string     `json:"stop_command_id"`
	ChargeOrderID uint64     `json:"charge_order_id"`
	OrderNo       string     `json:"order_no"`
	DeviceID      string     `json:"device_id"`
	PortNo        uint8      `json:"port_no"`
	PortID        *int64     `json:"port_id"`
	Status        string     `json:"status"`
	ResultCode    *int16     `json:"result_code"`
	AckAt         *time.Time `json:"ack_at"`
}

type StartResultsData struct {
	Items []SyncCommand `json:"items"`
}
type CommandData struct {
	Found   bool         `json:"found"`
	Command *SyncCommand `json:"command,omitempty"`
}

// SyncEvent 只提供持久化事件编号与既有 JSON 字符串，不改变协议字段或单位。
type SyncEvent struct {
	ID      uint64 `json:"id"`
	Payload string `json:"payload"`
}
type EventsData struct {
	Items []SyncEvent `json:"items"`
}

type MarkStartResultsRequest struct {
	CommandIDs []string `json:"command_ids" binding:"required,min=1,max=50"`
}
type MarkDeviceEventsRequest struct {
	IDs []uint64 `json:"ids" binding:"required,min=1,max=100"`
}

// MarkResult.Marked 允许为 0，表示本次重放没有再次推进记录。
type MarkResult struct {
	Marked int `json:"marked"`
}

type ReleasePortRequest struct {
	PortID  uint64 `json:"port_id" binding:"required"`
	OrderNo string `json:"order_no" binding:"required,max=64"`
}
type ReleasePortResult struct {
	Released bool `json:"released"`
	Already  bool `json:"already"`
}

// MeterSamplesQuery 以结束事件 ID 与 UTC 时间固定证据窗口。
type MeterSamplesQuery struct {
	DeviceID string
	AckAt    time.Time
	EndAt    time.Time
	EndID    uint64
}

type FreezeEndDeliveryRequest struct {
	DeviceEventID uint64 `json:"device_event_id" binding:"required"`
	ChargeOrderID uint64 `json:"charge_order_id" binding:"required"`
	Payload       string `json:"payload" binding:"required,max=65535"`
}

// Frozen=false 表示首次冻结成功，Frozen=true 表示返回既有冻结，两者均有效。
type FreezeEndDeliveryResult struct {
	Payload string `json:"payload"`
	Frozen  bool   `json:"frozen"`
}
type CompensateStartRequest struct {
	CommandID string `json:"command_id" binding:"required,uuid"`
}

// 补偿停止受理不证明设备从未启动，也不授权资金退款。
type CompensationAcceptance struct {
	Accepted bool `json:"accepted"`
}
