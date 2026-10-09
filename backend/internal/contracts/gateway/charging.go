// Package gateway 定义 gateway 内部 HTTP 接口的请求与响应契约。
package gateway

import (
	"errors"
	"fmt"
	"time"

	"chargepilot/internal/protocol"
)

// ErrInvalidResponse 表示响应不满足端点的状态码、字段或受理约束。
var ErrInvalidResponse = errors.New("invalid gateway response")

// BusinessError 保留 gateway 返回的非零业务 code。
type BusinessError struct {
	Code int /* 网关非零业务错误码 */
}

// Error 将 e 的业务码格式化为错误文本；不改变网关的业务状态。
func (e BusinessError) Error() string {
	return fmt.Sprintf("gateway rejected request: code %d", e.Code)
}

const (
	// MaxChargingEvidenceGap 是连续取证允许的最大相邻间隔及末条年龄。
	// 当前充电心跳每 15 秒采样，允许一个周期的传输抖动；超过 30 秒须重建窗口。
	// 协议适配器改变采样周期时须核对该证据契约，用例不依赖协议帧常量。
	MaxChargingEvidenceGap = 30 * time.Second
	ChargingEvidencePath   = "/api/v1/internal/charging-evidence"
	StopPath               = "/api/v1/internal/charge-orders/stop"
	StartPath              = "/api/v1/internal/charge-orders/start"
	MaxEvidenceBatch       = 20
	StopSourceAuto         = "auto"
	StopSourceUser         = "user_app"
)

// EvidenceQueryRequest 固定一个订单的设备端口、开始时刻和事件游标，作为批量充电取证中的单项查询。
type EvidenceQueryRequest struct {
	DeviceID  string    `json:"device_id"`  // 网关设备业务标识
	PortNo    uint8     `json:"port_no"`    // 目标物理端口号，从 1 开始
	StartedAt time.Time `json:"started_at"` // 订单开始充电的 UTC 时刻，取证窗口下界
	AfterID   uint64    `json:"after_id"`   // 上页最后事件主键，首次为 0
}

// EvidenceRequest 按输入顺序批量请求网关的端口心跳证据；每项保留自己的取证窗口与游标。
type EvidenceRequest struct {
	Requests []EvidenceQueryRequest `json:"requests"` // 按输入顺序批量查询设备端口，最多 20 项
}

// EvidenceSample 保留既有遥测 JSON 大小写，只暴露端口级心跳证据。
// ChargedMWh 的单位为 mWh，PowerDeciWatts 的单位为 0.1 W。
type EvidenceSample struct {
	Type          protocol.EventType       `json:"Type"`          // 证据事件类型，当前为心跳
	DeviceID      string                   `json:"DeviceID"`      // 网关设备业务标识
	ReceivedAt    time.Time                `json:"ReceivedAt"`    // 心跳接收的 UTC 时刻，必须保持入库顺序
	ChargingPorts []protocol.PortTelemetry `json:"ChargingPorts"` // 仅含目标端口；空集合表示该心跳未上报目标端口，必须中断连续窗口
}

// EvidencePageReply 按请求顺序返回证据；Complete=false 时 NextAfterID 必须大于请求游标。
type EvidencePageReply struct {
	DeviceID    string           `json:"device_id"`     // 网关设备业务标识
	PortNo      uint8            `json:"port_no"`       // 目标物理端口号，从 1 开始
	Samples     []EvidenceSample `json:"samples"`       // 按原顺序排列的心跳样本，保留目标端口未上报的情况
	Complete    bool             `json:"complete"`      // 是否已读取窗口内全部事件
	NextAfterID uint64           `json:"next_after_id"` // 未完成时必须大于请求游标的事件主键
}

// EvidenceReply 按请求顺序返回各端口的证据页；是否已读完整窗口由各页 Complete 和后续游标表示。
type EvidenceReply struct {
	Evidence []EvidencePageReply `json:"evidence"` // 按请求顺序对应的证据页
}

// StopRequest 提交订单号、所属用户和来源，请求网关检查权限并保存停止命令；不表示设备已经停电。
type StopRequest struct {
	OrderNo string `json:"order_no" binding:"required,max=64"`                  // 充电订单号，重试沿用
	UserID  uint64 `json:"user_id" binding:"required"`                          // 订单所属用户 ID
	Source  string `json:"source" binding:"required,oneof=user_app admin auto"` // 发起方标签，用于网关授权及审计
}

// StopReply 只确认停止命令已受理，Stopped 保持 false；设备结束由异步回执确认。
type StopReply struct {
	Accepted  bool   `json:"accepted"`   // 网关已保存停止命令
	Stopped   bool   `json:"stopped"`    // 是否设备已停止；受理阶段固定为 false
	CommandID string `json:"command_id"` // 持久命令标识，用于网关重试与设备回执
	Status    string `json:"status"`     // 命令持久化状态
}

// StartRequest 提交订单号，请求网关检查权限并保存启动命令；重试沿用同一订单。
type StartRequest struct {
	OrderNo string `json:"order_no" binding:"required,min=1,max=64"` // 充电订单号，重试沿用
}

// StartReply 是持久化 START 命令的请求处理结果。
// DeviceAcknowledged 只表达 gateway 已持有设备回执，不代替 central 的订单状态更新。
type StartReply struct {
	OrderNo            string `json:"order_no"`            // 充电订单号，重试沿用
	CommandID          string `json:"command_id"`          // 持久命令标识，用于网关重试与设备回执
	Status             string `json:"status"`              // 命令持久化状态
	DeviceAcknowledged bool   `json:"device_acknowledged"` // 网关是否已经持有设备启动回执
}
