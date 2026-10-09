package central

import (
	"chargepilot/internal/domain/charging"
	"time"
)

const (
	StartResultRoute = "/api/v1/internal/charge-orders/:order_no/start-result"
	EndResultRoute   = "/api/v1/internal/charge-orders/:order_no/end-result"
)

// StartResultRequest 提交设备启动回执，用命令 ID、订单编号和发生时间核对是否属于同一次启动。
type StartResultRequest struct {
	CommandID     string    `json:"command_id" binding:"required,uuid"`  // 已保存的命令 UUID，重试沿用
	ChargeOrderID uint64    `json:"charge_order_id" binding:"required"`  // 充电订单 ID
	OrderNo       string    `json:"order_no" binding:"required,max=64"`  // 业务订单号
	DeviceID      string    `json:"device_id" binding:"required,max=64"` // 设备编号
	PortNo        uint8     `json:"port_no" binding:"required"`          // 物理端口号，从 1 开始
	PortID        uint64    `json:"port_id" binding:"required"`          // 已开通端口的 ID
	Success       bool      `json:"success"`                             // 设备是否确认启动成功
	ResultCode    uint8     `json:"result_code"`                         // 协议结果码，0 表示成功，1–3 表示拒绝
	OccurredAt    time.Time `json:"occurred_at" binding:"required"`      // 设备回执发生的 UTC 时间
}

// EndResultRequest 提交结束计量和已保存的实测分段；电量为 Wh，时长为秒。
type EndResultRequest struct {
	OrderNo        string            `json:"order_no" binding:"required,max=64"`       // 业务订单号
	ChargeOrderID  uint64            `json:"charge_order_id" binding:"required"`       // 充电订单 ID
	StartCommandID string            `json:"start_command_id" binding:"required,uuid"` // 启动命令 UUID
	StopCommandID  string            `json:"stop_command_id" binding:"required,uuid"`  // 停止命令 UUID
	DeviceID       string            `json:"device_id" binding:"required,max=64"`      // 设备编号
	PortNo         uint8             `json:"port_no" binding:"required"`               // 物理端口号，从 1 开始
	PortID         uint64            `json:"port_id" binding:"required"`               // 已开通端口的 ID
	Meter          charging.EndMeter `json:"meter" binding:"required"`                 // 结束计量：电量为 Wh、时长为秒，附已保存的分段记录
}

// ResultReply 只确认回执已持久化；不表示计费或退款已经完成。
type ResultReply struct {
	Accepted bool `json:"accepted"` // 是否已保存请求；不代表设备操作或资金处理已完成
	Replayed bool `json:"replayed"` // 是否复用已保存的回执
}
