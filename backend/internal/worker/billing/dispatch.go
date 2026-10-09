package billing

import (
	"context"
	"errors"

	centralcontract "chargepilot/internal/contracts/central"
)

// DispatchClient 是计费任务触发所需的窄能力，回执验证及网络配置由注入实现拥有。
type DispatchClient interface {
	// DispatchBilling 在 ctx 下以无业务参数的 request 触发原计费批次，返回验证后的 reply 与错误。
	// 未完成可返回部分计数，未知结果不推断资金状态或自动重试。
	DispatchBilling(ctx context.Context, request centralcontract.BillingDispatchRequest) (reply centralcontract.BillingDispatchReply, err error)
}

// Dispatcher 仅适配 runtime 的计费触发入口，不执行计算、入账或恢复算法。
type Dispatcher struct {
	Central DispatchClient // 注入的计费触发能力；不持有 URL、令牌或通用 HTTP 客户端。
}

// Run 在 ctx 下触发一次原批次并返回原调用错误；未配置返回明确错误。
// 保留 runtime 的 error 入口，部分计数不变成成功，不重试或更换投递身份。
func (d Dispatcher) Run(ctx context.Context) error {
	if d.Central == nil {
		return errors.New("billing dispatcher is not configured")
	}
	_, err := d.Central.DispatchBilling(ctx, centralcontract.BillingDispatchRequest{})
	return err
}
