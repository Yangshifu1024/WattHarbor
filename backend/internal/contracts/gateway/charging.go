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
	// StopSourceAuto 表示自动停机扫描发起停止，当前唯一非用户端生产方。
	StopSourceAuto StopSource = "auto"
	// StopSourceUser 表示用户在客户端主动发起停止。
	StopSourceUser StopSource = "user_app"
)

// StopSource 是停止请求中来源字段的服务间契约取值（user_app／admin／auto）。
//
// 该字段是外部输入：绑定校验后只写诊断日志，不参与任何准入或授权判定；
// 能否停止仍由中央核对订单归属与可停止状态决定，网关不因本字段放行或拒绝。
//
// "admin" 是 oneof 标签接受的合法取值，但当前没有生产方，因此不为其提供常量，
// Known 仍需把它视为已知取值；新增生产方前须先确认其授权路径。
//
// 不得与 internal/worker/outbox.Publisher.Source 混同：后者是开放标签，非空即可，
// 取值集由 cmd/worker/app.go 的 "gateway"、"user"、"admin" 决定，用于选择发布流；
// 两处的 "admin" 只是字面量巧合，语义无关。
type StopSource string

// Known 判断是否属于本端点的合法取值集合（三值）。
// 只用于识别输入取值，不改变 binding 校验行为；未知值仍由 oneof 标签拒绝。
func (s StopSource) Known() bool {
	switch s {
	case StopSourceAuto, StopSourceUser:
		return true
	case "admin": // oneof 接受的取值，当前无生产方，故不提供常量
		return true
	}
	return false
}

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
	OrderNo string     `json:"order_no" binding:"required,max=64"`                  // 充电订单号，重试沿用
	UserID  uint64     `json:"user_id" binding:"required"`                          // 订单所属用户 ID
	Source  StopSource `json:"source" binding:"required,oneof=user_app admin auto"` // 发起方诊断标签；不参与授权判定
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
