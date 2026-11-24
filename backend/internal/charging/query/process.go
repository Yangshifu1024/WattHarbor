package query

import (
	"context"
	"errors"
	"fmt"
	"time"

	contract "wattharbor/internal/contracts/gateway"
	"wattharbor/internal/domain/operations"
	"wattharbor/internal/order"
)

// ErrProcessUnavailable 表示网关无法提供充电过程数据，不泄露服务配置或数据库内容。
var ErrProcessUnavailable = errors.New("充电过程暂时无法读取，请稍后重试")

// ProcessOrders 提供已授权订单的订单、设备与端口编号，查询不允许覆盖业务号、设备或端口。
type ProcessOrders interface {
	// ProcessIdentity 校验操作权限，返回订单 ID、业务单号、设备编号与端口号。
	ProcessIdentity(ctx context.Context, actor operations.Profile, id uint64) (order.ProcessIdentity, error)
}

// ProcessReader 查询网关采样；调用前须已校验订单归属，并确定设备与端口。
type ProcessReader interface {
	// OrderProcess 按 q 标识及游标读取，返回分页结果或输入、调用、契约错误，不修改设备。
	OrderProcess(ctx context.Context, q contract.OrderProcessRequest) (contract.OrderProcessReply, error)
}

// ProcessPoint 定义过程接口的一条采样，保留计量单位与未上报字段的空值。
type ProcessPoint struct {
	ID               uint64    `json:"id"`                // 过程行主键
	TS               time.Time `json:"ts"`                // 接收时刻
	PowerW           float64   `json:"power_w"`           // 功率，W
	ChargedKWh       float64   `json:"charged_kwh"`       // 累计电量，kWh
	RemainingKWh     float64   `json:"remaining_kwh"`     // 剩余电量，kWh
	ChargedSeconds   uint32    `json:"charged_seconds"`   // 累计时长，秒
	RemainingSeconds uint32    `json:"remaining_seconds"` // 剩余时长，秒
	SignalStrength   uint8     `json:"signal_strength"`   // 信号强度，零为有效值
	PortStatus       *uint8    `json:"port_status"`       // 状态，未上报为空
	VoltageV         *uint16   `json:"voltage_v"`         // 电压，V，未上报为空
	TemperatureC     *int16    `json:"temperature_c"`     // 温度，摄氏度，未上报为空
	DeviceStatus     *uint8    `json:"device_status"`     // 设备状态，未上报为空
}

// ProcessPage 按记录主键分页，不截断订单历史，末页游标显式为空。
type ProcessPage struct {
	Items       []ProcessPoint `json:"items"`         // 按输入顺序返回的过程读数，无点为空数组
	NextAfterID *uint64        `json:"next_after_id"` // 下一页游标，末页为空
}

// ProcessService 将授权订单信息与网关读数组合，不持有数据库、URL 或服务令牌。
type ProcessService struct {
	Orders  ProcessOrders // cmd注入的订单授权身份读取
	Gateway ProcessReader // cmd 注入的网关充电采样查询
}

// Read 先校验后台权限与订单编号，再按游标查询并返回充电过程采样。
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
