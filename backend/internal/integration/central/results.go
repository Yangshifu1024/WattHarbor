package central

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	centralcontract "chargepilot/internal/contracts/central"
	"chargepilot/internal/platform/serviceclient"
)

// deliverResult 接收 ctx、c、route、orderNo 与 payload，按原路径提交回执，返回中央接受／重放 Reply 或输入、HTTP／业务／响应错误。
// HTTP409合并 ErrResultConflict；Accepted=false 拒绝为无效响应，不替消费者执行补偿、释放、标记或未知结果重试。
func deliverResult(ctx context.Context, c *Client, route, orderNo string, payload any) (centralcontract.ResultReply, error) {
	path, err := orderPath(route, orderNo)
	if err != nil {
		return centralcontract.ResultReply{}, err
	}
	data, err := post[centralcontract.ResultReply](ctx, c, path, payload)
	var rejection *serviceclient.HTTPError
	if errors.As(err, &rejection) && rejection.StatusCode == http.StatusConflict {
		return data, errors.Join(centralcontract.ErrResultConflict, err)
	}
	if err != nil {
		return data, err
	}
	if !data.Accepted {
		return centralcontract.ResultReply{}, fmt.Errorf("%w: result not accepted", ErrInvalidResponse)
	}
	return data, nil
}

// SubmitStartResult 在 ctx 下提交 result 原启动命令、端口身份和启动结果，返回接受／重放 Reply 或原冲突及传输／响应错误。
// 确认允许消费者标记上报；客户端不改数据库、不创建新命令或自行处理补偿。
func (c *Client) SubmitStartResult(ctx context.Context, result centralcontract.StartResultRequest) (centralcontract.ResultReply, error) {
	return deliverResult(ctx, c, centralcontract.StartResultRoute, result.OrderNo, result)
}

// SubmitEndResult 在 ctx 下提交 result 原结束身份、Wh／秒计量及分段，返回接受／重放 Reply 或原冲突及传输／响应错误。
// 确认允许消费者继续释放网关端口，不代表计费、扣款或退款完成；未知确认不自动重发。
func (c *Client) SubmitEndResult(ctx context.Context, result centralcontract.EndResultRequest) (centralcontract.ResultReply, error) {
	return deliverResult(ctx, c, centralcontract.EndResultRoute, result.OrderNo, result)
}
