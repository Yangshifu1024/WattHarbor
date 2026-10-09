package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	contract "chargepilot/internal/contracts/gateway"
)

// DeviceSummaries 批量查询 1 至 100 台设备，每个编号非空且最多 64 字节。
// 保留未上报字段的空值；请求最多五秒，不自动重试。
func (c *Client) DeviceSummaries(ctx context.Context, ids []string) ([]contract.DeviceSummary, error) {
	if len(ids) == 0 || len(ids) > 100 {
		return nil, errors.New("device summaries require 1–100 devices")
	}
	query := url.Values{}
	for _, id := range ids {
		if id == "" || len(id) > 64 {
			return nil, errors.New("invalid summary device ID")
		}
		query.Add("device_id", id)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	status, body, err := c.transport.GetStatus(ctx, c.baseURL, c.token, contract.DeviceSummariesPath+"?"+query.Encode())
	if err != nil {
		return nil, err
	}
	var reply envelopeReply[contract.DeviceSummariesReply]
	if status != http.StatusOK || json.Unmarshal(body, &reply) != nil || reply.Code == nil || reply.Data == nil {
		return nil, ErrInvalidResponse
	}
	if *reply.Code != 0 {
		return nil, BusinessError{Code: *reply.Code}
	}
	return reply.Data.Items, nil
}
