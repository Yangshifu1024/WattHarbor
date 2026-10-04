package central

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	centralcontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
)

func deliverResult(ctx context.Context, c *Client, route, orderNo string, payload any) (centralcontract.ResultAcceptance, error) {
	path, err := orderPath(route, orderNo)
	if err != nil {
		return centralcontract.ResultAcceptance{}, err
	}
	data, err := post[centralcontract.ResultAcceptance](ctx, c, path, payload)
	var rejection *serviceclient.HTTPError
	if errors.As(err, &rejection) && rejection.StatusCode == http.StatusConflict {
		return data, errors.Join(centralcontract.ErrResultConflict, err)
	}
	if err != nil {
		return data, err
	}
	if !data.Accepted {
		return centralcontract.ResultAcceptance{}, fmt.Errorf("%w: result not accepted", ErrInvalidResponse)
	}
	return data, nil
}

// SubmitStartResult 的确认允许 worker 标记上报；客户端不处理补偿策略。
func (c *Client) SubmitStartResult(ctx context.Context, result centralcontract.StartResult) (centralcontract.ResultAcceptance, error) {
	return deliverResult(ctx, c, centralcontract.StartResultRoute, result.OrderNo, result)
}

// SubmitEndResult 的确认允许 worker 继续释放端口；不确认计费或退款完成。
func (c *Client) SubmitEndResult(ctx context.Context, result centralcontract.EndResult) (centralcontract.ResultAcceptance, error) {
	return deliverResult(ctx, c, centralcontract.EndResultRoute, result.OrderNo, result)
}
