package charge

import (
	"context"
	"errors"

	centralcontract "wattharbor/internal/contracts/central"
)

// AutoStopClient 是 worker 停机触发所需的接口，网络配置及回执验证由注入实现负责。
type AutoStopClient interface {
	// AutoStop 以无业务参数的 request 触发一次扫描，返回验证后的进度 reply 与错误。
	// 未完成回执可有部分进度；未知结果不推断设备状态，不自动重试或创建命令标识。
	AutoStop(ctx context.Context, request centralcontract.AutoStopRequest) (reply centralcontract.AutoStopReply, err error)
}

// AutoStopper 只触发 central 扫描；停机判定、计费截止点冻结和下发由 central 用例完成。
// worker 不读订单计费快照、不写 charge_billing_cutoff、不直读 gateway 库。
type AutoStopper struct {
	Central AutoStopClient // 注入的停机触发能力；不持有 URL、令牌或通用 HTTP 客户端。
}

// Run 触发一轮扫描，返回已验证的处理数量和调用错误；数量不表示设备已停电。
// 未配置返回零值和明确错误，未完成保留合法部分进度；不重试、不更换业务编号或写入资金。
func (s AutoStopper) Run(ctx context.Context) (int, error) {
	if s.Central == nil {
		return 0, errors.New("auto stop is not configured")
	}
	reply, err := s.Central.AutoStop(ctx, centralcontract.AutoStopRequest{})
	return reply.Stopped, err
}
