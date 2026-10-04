package central

import (
	"github.com/ChargePilot2026/charge-pilot/internal/domain/charging"
	"time"
)

const (
	StartResultRoute = "/api/v1/internal/charge-orders/:order_no/start-result"
	EndResultRoute   = "/api/v1/internal/charge-orders/:order_no/end-result"
)

type StartResult struct {
	CommandID     string    `json:"command_id" binding:"required,uuid"`
	ChargeOrderID uint64    `json:"charge_order_id" binding:"required"`
	OrderNo       string    `json:"order_no" binding:"required,max=64"`
	DeviceID      string    `json:"device_id" binding:"required,max=64"`
	PortNo        uint8     `json:"port_no" binding:"required"`
	PortID        uint64    `json:"port_id" binding:"required"`
	Success       bool      `json:"success"`
	ResultCode    uint8     `json:"result_code"`
	OccurredAt    time.Time `json:"occurred_at" binding:"required"`
}

// EndResult 的电量单位为 Wh，时长单位为秒；Segments 沿用冻结实测证据。
type EndResult struct {
	OrderNo        string            `json:"order_no" binding:"required,max=64"`
	ChargeOrderID  uint64            `json:"charge_order_id" binding:"required"`
	StartCommandID string            `json:"start_command_id" binding:"required,uuid"`
	StopCommandID  string            `json:"stop_command_id" binding:"required,uuid"`
	DeviceID       string            `json:"device_id" binding:"required,max=64"`
	PortNo         uint8             `json:"port_no" binding:"required"`
	PortID         uint64            `json:"port_id" binding:"required"`
	Meter          charging.EndMeter `json:"meter" binding:"required"`
}

// ResultAcceptance 只确认回执已持久化；不表示计费或退款已经完成。
type ResultAcceptance struct {
	Accepted bool `json:"accepted"`
	Replayed bool `json:"replayed"`
}
