package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
)

// DeviceFaults 在ctx下读取原100条待处理批，返回原主键／载荷或传输／契约错误，无重试。
// 使用受管理Transport、最多5秒且禁止重定向；单条协议解码与处理由所属用例决定。
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

// MarkDeviceFaults 在ctx下推进ids原主键，返回实际推进条数或准入／传输／契约错误。
// 0是并发或回复丢失重放的合法结果；不把未确认业务的主键加入请求，不重试或更换身份。
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
	// markDeviceFaultsWireReply 只校验原进度字段是否存在，不把缺失当正常零值。
	type markDeviceFaultsWireReply struct {
		Marked *int64 `json:"marked"` /* 必须存在的实际推进条数 */
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

// HeartbeatAfterKey 在ctx下按deviceID和key原水印取原证据；返回原时刻／载荷或输入／传输／契约错误。
// 原键缺失明确无水印，无心跳不补正常值；found时强制接收时刻晚于水印，协议与设备身份由用例核对。
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
