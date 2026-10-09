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
	// Orders 分页读取已支付的待启动订单。
	Orders interface {
		// PaidStarts 按订单 ID 升序读取下一页；空页必须返回非 nil 列表。
		PaidStarts(context.Context, uint64) ([]centralcontract.PaidStartOrder, error)
	}
	// Gateway 按订单号保存并下发启动命令。
	Gateway interface {
		// RequestStart 按同一订单号请求启动；受理成功还须等待设备回执。
		RequestStart(context.Context, gatewaycontract.StartRequest) (gatewaycontract.StartReply, error)
	}
	// cursor 保存本实例最后尝试的订单 ID；空页归零，下发失败也前进。
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
