// Package refund 调用中央服务触发退款批次。
package refund

import (
	"context"
	"errors"

	centralcontract "wattharbor/internal/contracts/central"
)

// DispatchClient 调用中央服务触发退款任务。
type DispatchClient interface {
	// DispatchRefunds 触发一批退款任务；未完成时返回部分进度和错误。
	DispatchRefunds(ctx context.Context, request centralcontract.RefundDispatchRequest) (reply centralcontract.RefundDispatchReply, err error)
}

// Dispatcher 将 worker 的定时调用转为中央服务的退款请求。
type Dispatcher struct {
	Central DispatchClient // 注入的退款触发能力；不持有 URL、令牌或通用 HTTP 客户端。
}

// Run 触发一批退款任务，返回调用错误；未配置客户端时报错。
func (d Dispatcher) Run(ctx context.Context) error {
	if d.Central == nil {
		return errors.New("refund dispatcher is not configured")
	}
	_, err := d.Central.DispatchRefunds(ctx, centralcontract.RefundDispatchRequest{})
	return err
}
