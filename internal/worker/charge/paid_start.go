package charge

import (
	"context"
	"errors"

	centralcontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
)

// PaidStarter 重试已支付订单，直到 gateway 接受持久化 START 命令；重复调用由 gateway 保证幂等。
type PaidStarter struct {
	Orders interface {
		PaidStarts(context.Context, uint64) ([]centralcontract.PaidStartOrder, error)
	}
	Gateway interface {
		RequestStart(context.Context, gatewaycontract.StartRequest) (gatewaycontract.StartAcceptance, error)
	}
	cursor uint64
}

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
