// Package gateway 定义 gateway 内部 HTTP 接口的路径、请求和响应结构。
package gateway

import (
	"errors"
	"fmt"
	"time"

	"chargepilot/internal/domain/devices"
	"chargepilot/internal/protocol"
)

// ErrInvalidResponse 表示响应状态、字段或处理结果不符合接口要求。
var ErrInvalidResponse = errors.New("invalid gateway response")

// BusinessError 保留 gateway 返回的非零业务 code。
type BusinessError struct {
	Code int /* 网关非零业务错误码 */
}

// Error 返回包含网关业务错误码的文本。
func (e BusinessError) Error() string {
	return fmt.Sprintf("gateway rejected request: code %d", e.Code)
}

const (
	// MaxChargingEvidenceGap 限定相邻心跳的时间间隔，以及最后一次心跳距当前时间的间隔。
	// DC589 充电时使用 15 秒心跳；间隔超过 30 秒或最后一条读数过旧时，不能据此自动停止。
	// 修改心跳周期时须同步核对该上限。
	MaxChargingEvidenceGap = 30 * time.Second
	ChargingEvidencePath   = "/api/v1/internal/charging-evidence"
	StopPath               = "/api/v1/internal/charge-orders/stop"
	StartPath              = "/api/v1/internal/charge-orders/start"
	MaxEvidenceBatch       = 20
	StopSourceAuto         = "auto"
	StopSourceUser         = "user_app"
)

// EvidenceQueryRequest 查询指定端口开始充电后的心跳，用事件 ID 分页。
type EvidenceQueryRequest struct {
	DeviceID  string    `json:"device_id"`  // 网关设备业务标识
	PortNo    uint8     `json:"port_no"`    // 目标物理端口号，从 1 开始
	StartedAt time.Time `json:"started_at"` // 开始充电的 UTC 时间，查询起点
	AfterID   uint64    `json:"after_id"`   // 上页最后事件主键，首次为 0
}

// EvidenceRequest 批量查询端口心跳，每项分别指定开始时间和分页游标。
type EvidenceRequest struct {
	Requests []EvidenceQueryRequest `json:"requests"` // 按输入顺序批量查询设备端口，最多 20 项
}

// EvidenceSample 返回心跳中的目标端口读数，JSON 字段保留原有大小写。
// ChargedMWh 的单位为 mWh，PowerDeciWatts 的单位为 0.1 W。
type EvidenceSample struct {
	Type          protocol.EventType       `json:"Type"`          // 事件类型，当前为心跳
	DeviceID      string                   `json:"DeviceID"`      // 网关设备业务标识
	ReceivedAt    time.Time                `json:"ReceivedAt"`    // 网关接收心跳的 UTC 时间
	ChargingPorts []protocol.PortTelemetry `json:"ChargingPorts"` // 仅含目标端口；空集合表示该心跳未上报目标端口，必须中断连续窗口
}

// EvidencePageReply 返回一个端口的一页心跳；未查完时 NextAfterID 必须大于请求游标。
type EvidencePageReply struct {
	DeviceID    string           `json:"device_id"`     // 网关设备业务标识
	PortNo      uint8            `json:"port_no"`       // 目标物理端口号，从 1 开始
	Samples     []EvidenceSample `json:"samples"`       // 按原顺序排列的心跳样本，保留目标端口未上报的情况
	Complete    bool             `json:"complete"`      // 是否已读取窗口内全部事件
	NextAfterID uint64           `json:"next_after_id"` // 未完成时必须大于请求游标的事件主键
}

// EvidenceReply 按请求顺序返回各端口的心跳分页结果。
type EvidenceReply struct {
	Evidence []EvidencePageReply `json:"evidence"` // 与请求顺序一致的分页结果
}

// StopRequest 提交订单号、所属用户和来源，请求网关检查权限并保存停止命令；不表示设备已经停电。
type StopRequest struct {
	OrderNo string `json:"order_no" binding:"required,max=64"`                  // 充电订单号，重试沿用
	UserID  uint64 `json:"user_id" binding:"required"`                          // 订单所属用户 ID
	Source  string `json:"source" binding:"required,oneof=user_app admin auto"` // 发起方诊断标签；授权仍由中央核对归属与状态
}

// StopReply 只确认停止命令已受理，Stopped 保持 false；设备结束由异步回执确认。
type StopReply struct {
	Accepted  bool                      `json:"accepted"`   // 网关已保存停止命令
	Stopped   bool                      `json:"stopped"`    // 是否设备已停止；受理阶段固定为 false
	CommandID string                    `json:"command_id"` // 持久命令标识，用于网关重试与设备回执
	Status    devices.StopCommandStatus `json:"status"`     // 命令持久化状态，对应 charge_stop_command.status
}

// StartRequest 提交订单号，请求网关检查权限并保存启动命令；重试沿用同一订单。
type StartRequest struct {
	OrderNo string `json:"order_no" binding:"required,min=1,max=64"` // 充电订单号，重试沿用
}

// StartReply 是持久化 START 命令的请求处理结果。
// DeviceAcknowledged 表示网关已收到启动回执；central 仍需处理该回执才能更新订单。
type StartReply struct {
	OrderNo            string                      `json:"order_no"`            // 充电订单号，重试沿用
	CommandID          string                      `json:"command_id"`          // 持久命令标识，用于网关重试与设备回执
	Status             devices.ChargeCommandStatus `json:"status"`              // 命令持久化状态，对应 charge_command.status
	DeviceAcknowledged bool                        `json:"device_acknowledged"` // 网关是否已经持有设备启动回执
}
