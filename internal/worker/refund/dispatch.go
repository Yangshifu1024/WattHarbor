package refund

import (
	"context"
	"errors"

	centralcontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
)

// DispatchClient 是退款任务触发所需的窄能力，回执验证及网络配置由注入实现拥有。
type DispatchClient interface {
	// DispatchRefunds 在 ctx 下以无业务参数的 request 触发原退款批次，返回验证后的 reply 与错误。
	// 未完成可保留进度，未知结果不推断资金状态、不自动重试或更换渠道身份。
	DispatchRefunds(ctx context.Context, request centralcontract.RefundDispatchRequest) (reply centralcontract.RefundDispatchReply, err error)
}

// Dispatcher 仅适配 runtime 的退款触发入口，资金及渠道执行仍由 central 所属用例拥有。
type Dispatcher struct {
	Central DispatchClient // 注入的退款触发能力；不持有 URL、令牌或通用 HTTP 客户端。
}

// Run 在 ctx 下触发一次原批次并返回原调用错误；未配置返回明确错误。
// 保留 runtime 的 error 入口，未完成不能视为成功，不重试或生成新的退款身份。
func (d Dispatcher) Run(ctx context.Context) error {
	if d.Central == nil {
		return errors.New("refund dispatcher is not configured")
	}
	_, err := d.Central.DispatchRefunds(ctx, centralcontract.RefundDispatchRequest{})
	return err
}
