package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	gatewaycontract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/platform/serviceclient"
)

// ResolveScan 按二维码查询设备和端口，不读取中央运营状态。
// HTTP 404 返回 ErrScanNotFound；其他请求或响应错误直接返回。响应最多 64 KiB。
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
