package central

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	centralcontract "wattharbor/internal/contracts/central"
	"wattharbor/internal/platform/serviceclient"
)

// deliverResult 按订单路径提交设备回执，要求 central 明确返回已接受，保留重复提交标志。
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

// SubmitStartResult 提交启动命令、端口和启动结果，返回 central 的接受结果及是否重复提交。
func (c *Client) SubmitStartResult(ctx context.Context, result centralcontract.StartResultRequest) (centralcontract.ResultReply, error) {
	return deliverResult(ctx, c, centralcontract.StartResultRoute, result.OrderNo, result)
}

// SubmitEndResult 提交结束事件、分段计量和订单编号；电量为 Wh，时长为秒。
// 返回 central 的接受结果及是否重复提交。
func (c *Client) SubmitEndResult(ctx context.Context, result centralcontract.EndResultRequest) (centralcontract.ResultReply, error) {
	return deliverResult(ctx, c, centralcontract.EndResultRoute, result.OrderNo, result)
}
