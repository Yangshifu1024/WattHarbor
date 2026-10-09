package central

import (
	"chargepilot/internal/domain/charging"
	"time"
)

const (
	StartResultRoute = "/api/v1/internal/charge-orders/:order_no/start-result"
	EndResultRoute   = "/api/v1/internal/charge-orders/:order_no/end-result"
)

// StartResultRequest 提交原启动命令的设备回执，主键、业务号和发生时刻共同核对原身份。
type StartResultRequest struct {
	CommandID     string    `json:"command_id" binding:"required,uuid"`  // 原持久命令UUID，不生成新的回执身份
	ChargeOrderID uint64    `json:"charge_order_id" binding:"required"`  // 原中央充电订单主键
	OrderNo       string    `json:"order_no" binding:"required,max=64"`  // 原业务订单号
	DeviceID      string    `json:"device_id" binding:"required,max=64"` // 原设备业务身份
	PortNo        uint8     `json:"port_no" binding:"required"`          // 原物理端口号，从1开始
	PortID        uint64    `json:"port_id" binding:"required"`          // 原开通端口主键；可空字段表示未知
	Success       bool      `json:"success"`                             // 原设备启动是否成功
	ResultCode    uint8     `json:"result_code"`                         // 原协议结果码；可空字段表示未收到回执
	OccurredAt    time.Time `json:"occurred_at" binding:"required"`      // 原设备回执发生UTC时刻
}

// EndResultRequest 的电量单位为 Wh，时长单位为秒；Segments 沿用冻结实测证据。
type EndResultRequest struct {
	OrderNo        string            `json:"order_no" binding:"required,max=64"`       // 原业务订单号
	ChargeOrderID  uint64            `json:"charge_order_id" binding:"required"`       // 原中央充电订单主键
	StartCommandID string            `json:"start_command_id" binding:"required,uuid"` // 原启动命令UUID
	StopCommandID  string            `json:"stop_command_id" binding:"required,uuid"`  // 原停止命令UUID
	DeviceID       string            `json:"device_id" binding:"required,max=64"`      // 原设备业务身份
	PortNo         uint8             `json:"port_no" binding:"required"`               // 原物理端口号，从1开始
	PortID         uint64            `json:"port_id" binding:"required"`               // 原开通端口主键；可空字段表示未知
	Meter          charging.EndMeter `json:"meter" binding:"required"`                 // 原结束计量，电量Wh、时长秒及冻结分段证据
}

// ResultReply 只确认回执已持久化；不表示计费或退款已经完成。
type ResultReply struct {
	Accepted bool `json:"accepted"` // 该端点原持久受理标志，不代替物理设备确认或资金完成
	Replayed bool `json:"replayed"` // 是否复用原持久回执
}
