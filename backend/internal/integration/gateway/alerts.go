package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	contract "chargepilot/internal/contracts/gateway"
)

// DeviceFaults 查询最多 100 条待处理故障，ID 必须递增。
// 请求最多五秒，不跟随重定向或自动重试；协议解析由调用方负责。
func (c *Client) DeviceFaults(ctx context.Context) ([]contract.DeviceFaultReport, error) {
	data, err := queryData[contract.DeviceFaultsReply](ctx, c, contract.DeviceFaultsPath+"?limit=100", "reports")
	if err != nil {
		return nil, err
	}
	if len(data.Reports) > contract.MaxDeviceFaultBatch {
		return nil, fmt.Errorf("%w: oversized fault batch", ErrInvalidResponse)
	}
	var previous uint64
	for _, r := range data.Reports {
		if r.ID <= previous {
			return nil, fmt.Errorf("%w: fault cursor", ErrInvalidResponse)
		}
		previous = r.ID
	}
	return data.Reports, nil
}

// MarkDeviceFaults 标记已处理的故障事件，返回实际更新条数；参数或响应无效时返回错误。
func (c *Client) MarkDeviceFaults(ctx context.Context, ids []uint64) (int64, error) {
	if len(ids) == 0 || len(ids) > contract.MaxDeviceFaultBatch {
		return 0, fmt.Errorf("invalid fault marks")
	}
	seen := make(map[uint64]bool, len(ids))
	for _, id := range ids {
		if id == 0 || seen[id] {
			return 0, fmt.Errorf("invalid fault identity")
		}
		seen[id] = true
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	copyClient := *c
	transport := c.transport
	httpClient := http.Client{Timeout: 5 * time.Second}
	if transport.HTTP != nil {
		httpClient = *transport.HTTP
	}
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	transport.HTTP = &httpClient
	copyClient.transport = transport
	// markDeviceFaultsWireReply 只校验进度字段是否存在，不把缺失当正常零值。
	type markDeviceFaultsWireReply struct {
		Marked *int64 `json:"marked"` /* 必须存在的实际更新条数 */
	}
	data, err := post[markDeviceFaultsWireReply](ctx, &copyClient, contract.MarkDeviceFaultsPath, contract.MarkDeviceFaultsRequest{IDs: ids}, http.StatusOK)
	if err != nil {
		return 0, err
	}
	if data.Marked == nil || *data.Marked < 0 || *data.Marked > int64(len(ids)) {
		return 0, fmt.Errorf("%w: marked count", ErrInvalidResponse)
	}
	return *data.Marked, nil
}

// HeartbeatAfterKey 查询指定事件之后的最新心跳；返回心跳时间和内容。
// 找到心跳时，接收时间必须晚于该事件；事件不存在或没有后续心跳时保留空值。
// 协议内容和设备编号由调用方核对。
func (c *Client) HeartbeatAfterKey(ctx context.Context, deviceID, key string) (contract.HeartbeatAfterKeyReply, error) {
	if deviceID == "" || len(deviceID) > 64 || strings.TrimSpace(deviceID) != deviceID || key == "" || len(key) > 64 {
		return contract.HeartbeatAfterKeyReply{}, fmt.Errorf("invalid heartbeat watermark")
	}
	path := contract.LatestHeartbeatPath + url.PathEscape(deviceID) + "/latest-heartbeat?after_key=" + url.QueryEscape(key)
	data, err := queryData[contract.HeartbeatAfterKeyReply](ctx, c, path, "found")
	if err != nil {
		return data, err
	}
	if data.AfterReceivedAt != nil && data.AfterReceivedAt.IsZero() {
		return contract.HeartbeatAfterKeyReply{}, fmt.Errorf("%w: zero watermark", ErrInvalidResponse)
	}
	if data.Found && (data.AfterReceivedAt == nil || data.ReceivedAt == nil || !data.ReceivedAt.After(*data.AfterReceivedAt) || data.Payload == "") {
		return contract.HeartbeatAfterKeyReply{}, fmt.Errorf("%w: incomplete heartbeat", ErrInvalidResponse)
	}
	if !data.Found && (data.ReceivedAt != nil || data.Payload != "") {
		return contract.HeartbeatAfterKeyReply{}, fmt.Errorf("%w: contradictory heartbeat", ErrInvalidResponse)
	}
	return data, nil
}
