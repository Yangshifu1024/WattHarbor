package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	gatewaycontract "chargepilot/internal/contracts/gateway"
)

// get 读取内部接口，只接受 HTTP 200、code=0 且 data 非空的响应，不自动重试。
func get[T any](ctx context.Context, c *Client, path string) (T, error) {
	var empty T
	status, body, err := c.transport.GetStatus(ctx, c.baseURL, c.token, path)
	if err != nil {
		return empty, err
	}
	if status != http.StatusOK {
		return empty, fmt.Errorf("%w: HTTP %d, want 200", ErrInvalidResponse, status)
	}
	var reply envelopeReply[T]
	if err := json.Unmarshal(body, &reply); err != nil {
		return empty, fmt.Errorf("%w: %w", ErrInvalidResponse, err)
	}
	if reply.Code == nil {
		return empty, fmt.Errorf("%w: response has no business code", ErrInvalidResponse)
	}
	if *reply.Code != 0 {
		return empty, BusinessError{Code: *reply.Code}
	}
	if reply.Data == nil {
		return empty, fmt.Errorf("%w: response has no data", ErrInvalidResponse)
	}
	return *reply.Data, nil
}

// StartResults 查询待上报的启动回执，可按命令 ID 筛选；保留空值，无记录返回空列表。
func (c *Client) StartResults(ctx context.Context, commandID string) ([]gatewaycontract.SyncCommand, error) {
	path := gatewaycontract.StartResultsPath
	if commandID != "" {
		path += "?command_id=" + url.QueryEscape(commandID)
	}
	data, err := get[gatewaycontract.StartResultsReply](ctx, c, path)
	if err != nil {
		return nil, err
	}
	if data.Items == nil {
		return nil, fmt.Errorf("%w: start results has no items", ErrInvalidResponse)
	}
	return data.Items, nil
}

// EndEvents 查询未处理的结束事件；保留事件编号和 JSON，无记录返回空列表。
func (c *Client) EndEvents(ctx context.Context) ([]gatewaycontract.SyncEvent, error) {
	data, err := get[gatewaycontract.EventsReply](ctx, c, gatewaycontract.EndEventsPath)
	if err != nil {
		return nil, err
	}
	if data.Items == nil {
		return nil, fmt.Errorf("%w: end events has no items", ErrInvalidResponse)
	}
	return data.Items, nil
}

// ChargeCommand 查询订单的启动命令并核对订单 ID；未找到时返回 nil, nil。
func (c *Client) ChargeCommand(ctx context.Context, orderID uint64) (*gatewaycontract.SyncCommand, error) {
	path := strings.Replace(gatewaycontract.ChargeCommandRoute, ":order_id", strconv.FormatUint(orderID, 10), 1)
	data, err := get[gatewaycontract.CommandReply](ctx, c, path)
	if err != nil {
		return nil, err
	}
	if !data.Found {
		return nil, nil
	}
	if data.Command == nil || data.Command.ChargeOrderID != orderID {
		return nil, fmt.Errorf("%w: command identity mismatch", ErrInvalidResponse)
	}
	return data.Command, nil
}

// mark 提交标记请求，实际更新数须在 0 到请求条数之间；0 可以表示重复请求。
// 只采信明确响应，调用失败时不猜测更新结果或自动重发。
func mark(ctx context.Context, c *Client, path string, payload any, requested int) (int, error) {
	// markWireReply 只校验进度字段是否存在，不把缺失当正常零值。
	type markWireReply struct {
		Marked *int `json:"marked"` // 实际更新数；nil为缺失，0为合法重放
	}
	data, err := post[markWireReply](ctx, c, path, payload, http.StatusOK)
	if err != nil {
		return 0, err
	}
	if data.Marked == nil || *data.Marked < 0 || *data.Marked > requested {
		return 0, fmt.Errorf("%w: missing or invalid mark count", ErrInvalidResponse)
	}
	return *data.Marked, nil
}

// MarkStartResultsReported 标记启动回执已上报；返回实际更新数，重复请求可为零。
func (c *Client) MarkStartResultsReported(ctx context.Context, ids []string) (int, error) {
	return mark(ctx, c, gatewaycontract.MarkStartResultsPath, gatewaycontract.MarkStartResultsRequest{CommandIDs: ids}, len(ids))
}

// MarkDeviceEventsProcessed 标记设备事件已处理；返回实际更新数，重复请求可为零。
func (c *Client) MarkDeviceEventsProcessed(ctx context.Context, ids []uint64) (int, error) {
	return mark(ctx, c, gatewaycontract.MarkDeviceEventsPath, gatewaycontract.MarkDeviceEventsRequest{IDs: ids}, len(ids))
}

// ReleasePort 按端口和订单号释放占用，响应须为 released=true；保留 already 标志。
// 这里只校验网关响应，不检查中央订单的结束状态或自动重试。
func (c *Client) ReleasePort(ctx context.Context, request gatewaycontract.ReleasePortRequest) (gatewaycontract.ReleasePortReply, error) {
	data, err := post[gatewaycontract.ReleasePortReply](ctx, c, gatewaycontract.ReleasePortPath, request, http.StatusOK)
	if err != nil {
		return data, err
	}
	if !data.Released {
		return gatewaycontract.ReleasePortReply{}, fmt.Errorf("%w: port not released", ErrInvalidResponse)
	}
	return data, nil
}

// MeterSamples 按设备、启动与结束时间、结束事件 ID 查询计量记录。
// 时间使用 UTC RFC3339Nano，ID 保持整数精度；记录筛选由服务端执行。
func (c *Client) MeterSamples(ctx context.Context, query gatewaycontract.MeterSamplesRequest) ([]gatewaycontract.SyncEvent, error) {
	path := strings.Replace(gatewaycontract.MeterSamplesRoute, ":device_id", url.PathEscape(query.DeviceID), 1)
	params := url.Values{"ack_at": {query.AckAt.UTC().Format(time.RFC3339Nano)}, "end_at": {query.EndAt.UTC().Format(time.RFC3339Nano)}, "end_id": {strconv.FormatUint(query.EndID, 10)}}
	data, err := get[gatewaycontract.EventsReply](ctx, c, path+"?"+params.Encode())
	if err != nil {
		return nil, err
	}
	if data.Items == nil {
		return nil, fmt.Errorf("%w: meter samples has no items", ErrInvalidResponse)
	}
	return data.Items, nil
}

// FreezeEndDelivery 提交结束事件与订单的候选数据，返回网关已保存的非空数据。
// 首次保存和编号核对由网关负责，不覆盖旧数据或重试未知结果。
func (c *Client) FreezeEndDelivery(ctx context.Context, request gatewaycontract.FreezeEndDeliveryRequest) (gatewaycontract.FreezeEndDeliveryReply, error) {
	data, err := post[gatewaycontract.FreezeEndDeliveryReply](ctx, c, gatewaycontract.FreezeEndDeliveryPath, request, http.StatusOK)
	if err != nil {
		return data, err
	}
	if data.Payload == "" {
		return gatewaycontract.FreezeEndDeliveryReply{}, fmt.Errorf("%w: frozen payload missing", ErrInvalidResponse)
	}
	return data, nil
}

// CompensateStart 按订单号和启动命令 ID 请求补偿停止，只有 accepted=true 才算已接受。
// 这不证明设备从未启动，也不执行退款。
func (c *Client) CompensateStart(ctx context.Context, orderNo, commandID string) error {
	if orderNo == "" || len(orderNo) > 64 || strings.ContainsAny(orderNo, "/?#") {
		return errors.New("invalid charge order number")
	}
	path := strings.Replace(gatewaycontract.CompensateStartRoute, ":order_no", url.PathEscape(orderNo), 1)
	data, err := post[gatewaycontract.CompensateStartReply](ctx, c, path, gatewaycontract.CompensateStartRequest{CommandID: commandID}, http.StatusOK)
	if err != nil {
		return err
	}
	if !data.Accepted {
		return fmt.Errorf("%w: compensation not accepted", ErrInvalidResponse)
	}
	return nil
}
