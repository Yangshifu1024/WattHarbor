package charge

import (
	"context"
	"errors"

	centralcontract "chargepilot/internal/contracts/central"
	gatewaycontract "chargepilot/internal/contracts/gateway"
)

// PaidStarter 分页请求启动已支付订单，并保存当前扫描游标。
// 失败订单在读到空页、游标归零后重新尝试，始终使用同一订单号。
type PaidStarter struct {
	// Orders 调用方待启动订单页读取能力，不持中央数据库或 HTTP 配置。
	Orders interface {
		// PaidStarts 返回待启动订单页或读取错误；空页须是非 nil 列表，不修改订单。
		PaidStarts(context.Context, uint64) ([]centralcontract.PaidStartOrder, error)
	}
	// Gateway 调用方 START 受理能力，订单幂等由网关拥有。
	Gateway interface {
		// RequestStart 接收 ctx 与订单请求，返回网关受理 Reply 或错误；可产生持久命令及设备下发，不代表中央已充电。
		RequestStart(context.Context, gatewaycontract.StartRequest) (gatewaycontract.StartReply, error)
	}
	// cursor 本实例上一已尝试订单 ID；本页下发失败仍更新，只有读到空页才归零重扫，不跨实例持久化。
	cursor uint64
}

// DispatchBatch 先检查整页订单 ID 严格递增，再逐单请求 START，返回受理数和首个错误。
// 单个订单失败仍继续并更新游标；列表失败保留游标，空页归零重扫。
// 同一实例必须顺序调用，不能并发修改游标。
func (s *PaidStarter) DispatchBatch(ctx context.Context) (int, error) {
	if s == nil || s.Orders == nil || s.Gateway == nil {
		return 0, errors.New("paid start dispatcher is not configured")
	}
	rows, err := s.Orders.PaidStarts(ctx, s.cursor)
	if err != nil {
		return 0, err
	}
	if rows == nil {
		return 0, errors.New("central paid start list rejected")
	}
	if len(rows) == 0 {
		s.cursor = 0
		return 0, nil
	}
	previous := s.cursor
	for _, row := range rows {
		if row.ID <= previous || row.OrderNo == "" {
			return 0, errors.New("central paid start cursor mismatch")
		}
		previous = row.ID
	}
	accepted := 0
	var firstError error
	for _, row := range rows {
		s.cursor = row.ID
		if _, err := s.Gateway.RequestStart(ctx, gatewaycontract.StartRequest{OrderNo: row.OrderNo}); err != nil {
			if firstError == nil {
				firstError = err
			}
			continue
		}
		accepted++
	}
	return accepted, firstError
}
