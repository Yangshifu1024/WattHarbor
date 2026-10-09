package charge

import (
	"context"
	"errors"

	centralcontract "chargepilot/internal/contracts/central"
	gatewaycontract "chargepilot/internal/contracts/gateway"
)

// PaidStarter 顺序投递已支付待启动订单，保存本实例扫描游标；失败订单经后续空页归零重扫复用原订单幂等身份。
type PaidStarter struct {
	// Orders 消费方待启动订单页读取能力，不持中央数据库或 HTTP 配置。
	Orders interface {
		// PaidStarts 接收 ctx 与 afterID，返回原待启动订单页或读取错误；空页须是非 nil列表，不修改订单。
		PaidStarts(context.Context, uint64) ([]centralcontract.PaidStartOrder, error)
	}
	// Gateway 消费方 START 受理能力，原订单幂等由网关拥有。
	Gateway interface {
		// RequestStart 接收 ctx 与原订单请求，返回网关受理 Reply或错误；可产生原持久命令及设备下发，不代表中央已充电。
		RequestStart(context.Context, gatewaycontract.StartRequest) (gatewaycontract.StartReply, error)
	}
	// cursor 本实例上一已尝试订单 ID；本页下发失败仍推进，只有读到空页才归零重扫，不跨实例持久化。
	cursor uint64
}

// DispatchBatch 接收 ctx，顺序读取当前 cursor 后一页订单，先校验整页严格递增身份，再逐单按原订单号请求 START。
// 返回已受理数及原首个下发错误；单单失败继续本页，列表失败不推进 cursor，空页将 cursor归零供下轮全量重扫。
// 本实例保存可变 cursor，调用方须顺序复用实例；所有能力接收 ctx，不发明新命令或直接修改订单／资金。
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
