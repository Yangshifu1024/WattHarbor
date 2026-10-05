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
	CommandID     string     `json:"command_id"`      // 原持久命令UUID，不生成新的回执身份
	StopCommandID string     `json:"stop_command_id"` // 原停止命令UUID
	ChargeOrderID uint64     `json:"charge_order_id"` // 原中央充电订单主键
	OrderNo       string     `json:"order_no"`        // 原业务订单号
	DeviceID      string     `json:"device_id"`       // 原设备业务身份
	PortNo        uint8      `json:"port_no"`         // 原物理端口号，从1开始
	PortID        *int64     `json:"port_id"`         // 原开通端口主键；可空字段表示未知
	Status        string     `json:"status"`          // 原持久命令状态
	ResultCode    *int16     `json:"result_code"`     // 原协议结果码；可空字段表示未收到回执
	AckAt         *time.Time `json:"ack_at"`          // 原启动回执UTC时刻；可空表示未确认
}

// StartResultsReply 返回原已确认待投递启动命令，不领取或修改状态。
type StartResultsReply struct {
	Items []SyncCommand `json:"items"` // 原有序记录集合；空集合保持数组
}

// CommandReply 表达原订单对应命令是否存在，缺失不补造成功命令。
type CommandReply struct {
	Found   bool         `json:"found"`             // 是否找到原查询身份
	Command *SyncCommand `json:"command,omitempty"` // 原持久命令；未找到时省略
}

// SyncEvent 只提供持久化事件编号与既有 JSON 字符串，不改变协议字段或单位。
type SyncEvent struct {
	ID      uint64 `json:"id"`      // 原持久主键，保持uint64整数精度
	Payload string `json:"payload"` // 原冻结事件JSON，不重写协议字段或单位
}

// EventsReply 返回原待消费事件或计量事件集合，不转换其原载荷。
type EventsReply struct {
	Items []SyncEvent `json:"items"` // 原有序记录集合；空集合保持数组
}

// MarkStartResultsRequest 在中央接受启动回执后请求网关标记原命令已报告，保留原命令 UUID 批次。
type MarkStartResultsRequest struct {
	CommandIDs []string `json:"command_ids" binding:"required,min=1,max=50"` // 本批原启动命令UUID，最多50条
}

// MarkDeviceEventsRequest 请求网关将已处理的原设备事件批次推进为已消费，不修改事件载荷或生成新的身份。
type MarkDeviceEventsRequest struct {
	IDs []uint64 `json:"ids" binding:"required,min=1,max=100"` // 本批原持久事件主键，最多100条
}

// MarkReply.Marked 允许为 0，表示本次重放没有再次推进记录。
type MarkReply struct {
	Marked int `json:"marked"` // 本次实际推进数；0为正常幂等重放
}

// ReleasePortRequest 以原端口主键和订单号限定网关占用释放，防止迟到结束流程释放后来订单的占用。
type ReleasePortRequest struct {
	PortID  uint64 `json:"port_id" binding:"required"`         // 原开通端口主键；可空字段表示未知
	OrderNo string `json:"order_no" binding:"required,max=64"` // 原业务订单号
}

// ReleasePortReply 保留实际释放与原已释放两种幂等结果，不确认资金结算。
type ReleasePortReply struct {
	Released bool `json:"released"` // 原端口是否完成释放
	Already  bool `json:"already"`  // 原端口是否此前已释放
}

// MeterSamplesRequest 以结束事件 ID 与 UTC 时间固定证据窗口。
type MeterSamplesRequest struct {
	DeviceID string    // 原设备业务身份
	AckAt    time.Time // 原启动回执UTC时刻；可空表示未确认
	EndAt    time.Time // 原结束UTC时刻，固定取证上界
	EndID    uint64    // 原结束事件主键，固定取证游标
}

// FreezeEndDeliveryRequest 首次冻结原结束事件对应订单的投递载荷；重放必须使用既有权威载荷，不能更换事件或订单身份。
type FreezeEndDeliveryRequest struct {
	DeviceEventID uint64 `json:"device_event_id" binding:"required"`   // 原设备结束事件主键
	ChargeOrderID uint64 `json:"charge_order_id" binding:"required"`   // 原中央充电订单主键
	Payload       string `json:"payload" binding:"required,max=65535"` // 原冻结事件JSON，不重写协议字段或单位
}

// Frozen=false 表示首次冻结成功，Frozen=true 表示返回既有冻结，两者均有效。
type FreezeEndDeliveryReply struct {
	Payload string `json:"payload"` // 原冻结事件JSON，不重写协议字段或单位
	Frozen  bool   `json:"frozen"`  // false为首次冻结，true为返回既有权威载荷
}

// CompensateStartRequest 在原订单路由中引用原 START 命令，请求持久受理补偿停止；受理不证明设备从未启动或授权退款。
type CompensateStartRequest struct {
	CommandID string `json:"command_id" binding:"required,uuid"` // 原持久命令UUID，不生成新的回执身份
}

// 补偿停止受理不证明设备从未启动，也不授权资金退款。
type CompensateStartReply struct {
	Accepted bool `json:"accepted"` // 该端点原持久受理标志，不代替物理设备确认或资金完成
}
