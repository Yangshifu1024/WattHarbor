package gateway

import (
	"time"

	"wattharbor/internal/domain/devices"
)

const (
	StartResultsPath      = "/api/v1/internal/start-results"
	DeferSyncDeliveryPath = "/api/v1/internal/sync-deliveries/defer"
	MarkStartResultsPath  = "/api/v1/internal/start-results/mark-reported"
	EndEventsPath         = "/api/v1/internal/end-events"
	ChargeCommandRoute    = "/api/v1/internal/charge-commands/by-order/:order_id"
	MarkDeviceEventsPath  = "/api/v1/internal/device-events/mark-processed"
	ReleasePortPath       = "/api/v1/internal/ports/release"
	MeterSamplesRoute     = "/api/v1/internal/devices/:device_id/meter-samples"
	FreezeEndDeliveryPath = "/api/v1/internal/charge-end-deliveries/freeze"
	CompensateStartRoute  = "/api/v1/internal/charge-orders/:order_no/compensate"
)

// DeferSyncDeliveryRequest 仅登记同步投递失败分类，不提供业务状态修改能力。
type DeferSyncDeliveryRequest struct {
	Kind     string `json:"kind" binding:"required,oneof=start end"`                            // start 命令或 end 事件
	Key      string `json:"key" binding:"required,max=64"`                                      // 命令 UUID 或事件十进制主键
	Category string `json:"category" binding:"required,oneof=invalid unknown dependency retry"` // 固定脱敏分类
}

// DeferSyncDeliveryReply 确认退避已持久化，不表示业务回执已确认。
type DeferSyncDeliveryReply struct {
	Deferred bool `json:"deferred"` // true 表示元数据提交成功
}

// SyncCommand 保留端口、结果码和回执时间的空值，缺失记录不解码为零。
type SyncCommand struct {
	CommandID     string                      `json:"command_id"`      // 已保存的命令 UUID，重试沿用
	StopCommandID string                      `json:"stop_command_id"` // 停止命令 UUID
	ChargeOrderID uint64                      `json:"charge_order_id"` // 充电订单 ID
	OrderNo       string                      `json:"order_no"`        // 业务订单号
	DeviceID      string                      `json:"device_id"`       // 设备编号
	PortNo        uint8                       `json:"port_no"`         // 物理端口号，从 1 开始
	PortID        *int64                      `json:"port_id"`         // 已开通端口的 ID
	Status        devices.ChargeCommandStatus `json:"status"`          // charge_command.status 词表，见 domain/devices
	ResultCode    *int16                      `json:"result_code"`     // 协议结果码，未收到回执时为空
	AckAt         *time.Time                  `json:"ack_at"`          // 启动确认时间，UTC；未确认时为空
}

// StartResultsReply 返回已确认且待上报的启动命令，不修改命令状态。
type StartResultsReply struct {
	Items []SyncCommand `json:"items"` // 按原顺序排列的记录；无记录时返回空数组
}

// CommandReply 返回订单对应的启动命令；未找到时不生成成功结果。
type CommandReply struct {
	Found   bool         `json:"found"`             // 是否找到请求的订单命令
	Command *SyncCommand `json:"command,omitempty"` // 已保存的命令，未找到时省略
}

// SyncEvent 只提供持久化事件编号与既有 JSON 字符串，不改变协议字段或单位。
type SyncEvent struct {
	ID      uint64 `json:"id"`      // 已保存的 ID，保持 uint64 精度
	Payload string `json:"payload"` // 首次保存的事件 JSON，字段和单位保持不变
}

// EventsReply 返回待处理设备事件或计量事件，保留原始 JSON。
type EventsReply struct {
	Items []SyncEvent `json:"items"` // 按原顺序排列的记录；无记录时返回空数组
}

// MarkStartResultsRequest 在 central 接受启动回执后，按命令 UUID 请求 gateway 标记已上报。
type MarkStartResultsRequest struct {
	CommandIDs []string `json:"command_ids" binding:"required,min=1,max=50"` // 本批启动命令 UUID，最多50条
}

// MarkDeviceEventsRequest 按事件 ID 请求 gateway 标记已处理，不改变事件内容。
type MarkDeviceEventsRequest struct {
	IDs []uint64 `json:"ids" binding:"required,min=1,max=100"` // 本批事件 ID，最多 100 条
}

// MarkReply 返回实际更新条数；重复请求可以为零。
type MarkReply struct {
	Marked int `json:"marked"` // 本次实际更新条数；已被标记的记录不会再次计数
}

// ReleasePortRequest 以端口主键和订单号限定网关占用释放，防止迟到结束流程释放后来订单的占用。
type ReleasePortRequest struct {
	PortID  uint64 `json:"port_id" binding:"required"`         // 已开通端口的 ID
	OrderNo string `json:"order_no" binding:"required,max=64"` // 业务订单号
}

// ReleasePortReply 返回是否释放占用及是否此前已释放，不表示资金已结算。
type ReleasePortReply struct {
	Released bool `json:"released"` // 端口是否完成释放
	Already  bool `json:"already"`  // 端口是否此前已释放
}

// MeterSamplesRequest 用启动、结束时间和结束事件 ID 限定计量记录范围。
type MeterSamplesRequest struct {
	DeviceID string    // 设备编号
	AckAt    time.Time // 启动确认时间，UTC
	EndAt    time.Time // 结束时间，UTC，固定查询上界
	EndID    uint64    // 结束事件 ID，固定查询游标
}

// FreezeEndDeliveryRequest 按结束事件和订单保存首次投递数据；重复提交必须使用已保存的数据，不得更换编号。
type FreezeEndDeliveryRequest struct {
	DeviceEventID uint64 `json:"device_event_id" binding:"required"`   // 设备结束事件 ID
	ChargeOrderID uint64 `json:"charge_order_id" binding:"required"`   // 充电订单 ID
	Payload       string `json:"payload" binding:"required,max=65535"` // 首次保存的事件 JSON，字段和单位保持不变
}

// FreezeEndDeliveryReply 返回首次保存的数据；Frozen=true 表示此前已保存。
type FreezeEndDeliveryReply struct {
	Payload string `json:"payload"` // 首次保存的事件 JSON，字段和单位保持不变
	Frozen  bool   `json:"frozen"`  // false 表示首次保存，true 表示返回已保存的数据
}

// CompensateStartRequest 指定启动命令，请求网关保存补偿停止任务；接受请求不证明设备从未启动，也不授权退款。
type CompensateStartRequest struct {
	CommandID string `json:"command_id" binding:"required,uuid"` // 已保存的命令 UUID，重试沿用
}

// CompensateStartReply 确认网关已接受补偿停止请求；不代表设备已停止或可以退款。
type CompensateStartReply struct {
	Accepted bool `json:"accepted"` // 是否已保存请求；不代表设备操作或资金处理已完成
}
