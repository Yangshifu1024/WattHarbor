package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
)

// DeviceSummaries 在 ctx 下查询 ids 的原运行事实；一至一百个非空、最长 64 字节的编号。
// 返回原 nullable 字段及端口，或输入、传输、HTTP、业务码、响应缺失错误；五秒上限，无重试或业务写入。
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
