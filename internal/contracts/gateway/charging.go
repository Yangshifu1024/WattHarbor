// Package gateway 定义 gateway 内部 HTTP 接口的请求与响应契约。
package gateway

import (
	"errors"
	"fmt"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
)

// ErrInvalidResponse 表示响应不满足端点的状态码、字段或受理约束。
var ErrInvalidResponse = errors.New("invalid gateway response")

// BusinessError 保留 gateway 返回的非零业务 code。
type BusinessError struct{ Code int }

func (e BusinessError) Error() string {
	return fmt.Sprintf("gateway rejected request: code %d", e.Code)
}

const (
	ChargingEvidencePath = "/api/v1/internal/charging-evidence"
	StopPath             = "/api/v1/internal/charge-orders/stop"
	StartPath            = "/api/v1/internal/charge-orders/start"
	MaxEvidenceBatch     = 20
	StopSourceAuto       = "auto"
	StopSourceUser       = "user_app"
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

type StartRequest struct {
	OrderNo string `json:"order_no" binding:"required,min=1,max=64"`
}

// StartAcceptance 是持久化 START 命令的受理结果。
// DeviceAcknowledged 只表达 gateway 已持有设备回执，不代替 central 的订单状态推进。
type StartAcceptance struct {
	OrderNo            string `json:"order_no"`
	CommandID          string `json:"command_id"`
	Status             string `json:"status"`
	DeviceAcknowledged bool   `json:"device_acknowledged"`
}
