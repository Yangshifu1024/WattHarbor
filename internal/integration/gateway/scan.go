package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
)

// ResolveScan 在 ctx 期限内查询request.Code原设备／端口事实，不读取中央业务状态。
// 返回原 JSON 读模型；404 返回 ErrScanNotFound，传输、业务码及身份缺失返回错误。
// 响应仍限制为 64 KiB，不暴露 URL 或令牌给业务消费者。
func (c *Client) ResolveScan(ctx context.Context, request gatewaycontract.ScanRequest) (gatewaycontract.ScanReply, error) {
	status, body, err := c.transport.GetStatus(ctx, c.baseURL, c.token, gatewaycontract.ScanResolvePath+"?"+url.Values{"code": {request.Code}}.Encode())
	if errors.Is(err, serviceclient.ErrNotFound) {
		return gatewaycontract.ScanReply{}, gatewaycontract.ErrScanNotFound
	}
	if err != nil {
		return gatewaycontract.ScanReply{}, err
	}
	if status != http.StatusOK || len(body) > 64<<10 {
		return gatewaycontract.ScanReply{}, ErrInvalidResponse
	}
	var reply envelopeReply[gatewaycontract.ScanReply]
	if json.Unmarshal(body, &reply) != nil || reply.Code == nil || *reply.Code != 0 || reply.Data == nil || reply.Data.DeviceID == "" {
		return gatewaycontract.ScanReply{}, ErrInvalidResponse
	}
	return *reply.Data, nil
}
