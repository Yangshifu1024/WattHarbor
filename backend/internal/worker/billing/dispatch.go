// Package billing 调用中央服务触发计费批次。
package billing

import (
	"context"
	"errors"

	centralcontract "chargepilot/internal/contracts/central"
)

// DispatchClient 调用中央服务触发计费任务。
type DispatchClient interface {
	// DispatchBilling 触发一批计费任务；未完成时返回部分进度和错误。
	DispatchBilling(ctx context.Context, request centralcontract.BillingDispatchRequest) (reply centralcontract.BillingDispatchReply, err error)
}

// Dispatcher 将 worker 的定时调用转为中央服务的计费请求。
type Dispatcher struct {
	Central DispatchClient // 注入的计费触发能力；不持有 URL、令牌或通用 HTTP 客户端。
}

// Run 触发一批计费任务，返回调用错误；未配置客户端时报错。
func (d Dispatcher) Run(ctx context.Context) error {
	if d.Central == nil {
		return errors.New("billing dispatcher is not configured")
	}
	_, err := d.Central.DispatchBilling(ctx, centralcontract.BillingDispatchRequest{})
	return err
}
