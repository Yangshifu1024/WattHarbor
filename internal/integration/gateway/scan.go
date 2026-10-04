package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/domain/money"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
)

// ResolveScan 在 ctx 期限内查询原 code 的设备／端口事实，不读取中央业务状态。
// 返回原 JSON 读模型；404 返回 ErrScanNotFound，传输、业务码及身份缺失返回错误。
// 响应仍限制为 64 KiB，不暴露 URL 或令牌给业务消费者。
func (c *Client) ResolveScan(ctx context.Context, code string) (money.ScanResult, error) {
	status, body, err := c.transport.GetStatus(ctx, c.baseURL, c.token, gatewaycontract.ScanResolvePath+"?"+url.Values{"code": {code}}.Encode())
	if errors.Is(err, serviceclient.ErrNotFound) {
		return money.ScanResult{}, gatewaycontract.ErrScanNotFound
	}
	if err != nil {
		return money.ScanResult{}, err
	}
	if status != http.StatusOK || len(body) > 64<<10 {
		return money.ScanResult{}, ErrInvalidResponse
	}
	var reply response[money.ScanResult]
	if json.Unmarshal(body, &reply) != nil || reply.Code == nil || *reply.Code != 0 || reply.Data == nil || reply.Data.DeviceID == "" {
		return money.ScanResult{}, ErrInvalidResponse
	}
	return *reply.Data, nil
}
