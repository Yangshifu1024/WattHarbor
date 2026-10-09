package query

import (
	"context"
	"errors"
	"fmt"
	"time"

	contract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/domain/operations"
	"chargepilot/internal/order"
)

// ErrProcessUnavailable 是原后台过程依赖故障，不泄露服务身份或存储内容。
var ErrProcessUnavailable = errors.New("充电过程暂时无法读取，请稍后重试")

// ProcessOrders 提供已授权原订单的规范身份，查询不允许覆盖业务号、设备或端口。
type ProcessOrders interface {
	// ProcessIdentity 在 ctx 下核对 actor 与 id，返回原身份或禁止／缺行／存储错误，无写入。
	ProcessIdentity(ctx context.Context, actor operations.Profile, id uint64) (order.ProcessIdentity, error)
}

// ProcessReader 提供原网关过程页，授权与规范身份由所属用例确定。
type ProcessReader interface {
	// OrderProcess 在 ctx 下按 q 原身份及游标读取，返回原页或输入／调用／契约错误，不修改设备。
	OrderProcess(ctx context.Context, q contract.OrderProcessRequest) (contract.OrderProcessReply, error)
}

// ProcessPoint 是本用例公开过程读模型，保留原单位、精确身份及未上报空值。
type ProcessPoint struct {
	ID               uint64    `json:"id"`                // 原过程行主键
	TS               time.Time `json:"ts"`                // 原接收时刻
	PowerW           float64   `json:"power_w"`           // 原功率，W
	ChargedKWh       float64   `json:"charged_kwh"`       // 原累计电量，kWh
	RemainingKWh     float64   `json:"remaining_kwh"`     // 原剩余电量，kWh
	ChargedSeconds   uint32    `json:"charged_seconds"`   // 原累计时长，秒
	RemainingSeconds uint32    `json:"remaining_seconds"` // 原剩余时长，秒
	SignalStrength   uint8     `json:"signal_strength"`   // 原信号强度，零为有效值
	PortStatus       *uint8    `json:"port_status"`       // 原状态，未上报为空
	VoltageV         *uint16   `json:"voltage_v"`         // 原电压，V，未上报为空
	TemperatureC     *int16    `json:"temperature_c"`     // 原温度，摄氏度，未上报为空
	DeviceStatus     *uint8    `json:"device_status"`     // 原设备状态，未上报为空
}

// ProcessPage 按原行主键分页，不截断订单历史，末页游标显式为空。
type ProcessPage struct {
	Items       []ProcessPoint `json:"items"`         // 原序过程读数，无点为空数组
	NextAfterID *uint64        `json:"next_after_id"` // 原下一页游标，末页为空
}

// ProcessService 将授权订单事实与网关读数组合，不持有数据库、URL或服务令牌。
type ProcessService struct {
	Orders  ProcessOrders // cmd注入的订单授权身份读取
	Gateway ProcessReader // cmd注入的具名过程读取
}

// Read 在 ctx 下按 actor/id 核对原订单，再以 after/limit 原分页取过程并转换为本用例读模型。
// 返回原序页或输入／禁止／缺行／存储／依赖错误；不启动未管理任务或更换请求身份。
func (s ProcessService) Read(ctx context.Context, actor operations.Profile, id, after uint64, limit uint16) (ProcessPage, error) {
	if !actor.HasPermission("order.read") {
		return ProcessPage{}, operations.ErrForbidden
	}
	if id == 0 || limit == 0 || limit > contract.MaxQueryPoints {
		return ProcessPage{}, &order.QueryInputError{Message: "过程游标或分页参数无效"}
	}
	if s.Orders == nil || s.Gateway == nil {
		return ProcessPage{}, ErrProcessUnavailable
	}
	identity, err := s.Orders.ProcessIdentity(ctx, actor, id)
	if err != nil {
		return ProcessPage{}, err
	}
	data, err := s.Gateway.OrderProcess(ctx, contract.OrderProcessRequest{OrderID: identity.ID, OrderNo: identity.OrderNo, DeviceID: identity.DeviceID, PortNo: identity.PortNo, AfterID: after, Limit: limit})
	if err != nil {
		return ProcessPage{}, fmt.Errorf("%w: %w", ErrProcessUnavailable, err)
	}
	out := ProcessPage{Items: make([]ProcessPoint, len(data.Items)), NextAfterID: data.NextAfterID}
	for i, p := range data.Items {
		out.Items[i] = ProcessPoint(p)
	}
	return out, nil
}
