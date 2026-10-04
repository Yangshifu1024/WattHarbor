// Package gateway 定义 gateway 内部 HTTP 接口的请求与响应契约。
package gateway

import (
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
)

const (
	ChargingEvidencePath = "/api/v1/internal/charging-evidence"
	StopPath             = "/api/v1/internal/charge-orders/stop"
	MaxEvidenceBatch     = 20
	StopSourceAuto       = "auto"
)

type EvidenceQuery struct {
	DeviceID  string    `json:"device_id"`
	PortNo    uint8     `json:"port_no"`
	StartedAt time.Time `json:"started_at"`
	AfterID   uint64    `json:"after_id"`
}

type EvidenceRequest struct {
	Requests []EvidenceQuery `json:"requests"`
}

// EvidenceSample 保留既有遥测 JSON 大小写，只暴露端口级心跳证据。
// ChargedMWh 的单位为 mWh，PowerDeciWatts 的单位为 0.1 W。
type EvidenceSample struct {
	Type          protocol.EventType       `json:"Type"`
	DeviceID      string                   `json:"DeviceID"`
	ReceivedAt    time.Time                `json:"ReceivedAt"`
	ChargingPorts []protocol.PortTelemetry `json:"ChargingPorts"`
}

// EvidencePage 按请求顺序返回；Complete=false 时 NextAfterID 必须推进游标。
type EvidencePage struct {
	DeviceID    string           `json:"device_id"`
	PortNo      uint8            `json:"port_no"`
	Samples     []EvidenceSample `json:"samples"`
	Complete    bool             `json:"complete"`
	NextAfterID uint64           `json:"next_after_id"`
}

type EvidenceData struct {
	Evidence []EvidencePage `json:"evidence"`
}

type StopRequest struct {
	OrderNo string `json:"order_no" binding:"required,max=64"`
	UserID  uint64 `json:"user_id" binding:"required"`
	Source  string `json:"source" binding:"required,oneof=user_app admin auto"`
}

// StopAcceptance 只确认停止命令已受理，Stopped 保持 false；设备结束由异步回执确认。
type StopAcceptance struct {
	Accepted  bool   `json:"accepted"`
	Stopped   bool   `json:"stopped"`
	CommandID string `json:"command_id"`
	Status    string `json:"status"`
}
