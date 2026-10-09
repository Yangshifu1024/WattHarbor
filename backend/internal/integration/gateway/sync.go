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

// get 接收 ctx、c 与 path，执行原 GET并验证HTTP200、明确 code0及非 nil data，返回 T 或传输／业务／解码／取消错误；不重试或写业务状态。
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

// StartResults 在 ctx 下按可选 commandID 读取原待上报启动回执，筛选值仅 URL 编码，返回保留 nullable 证据的非 nil 列表或原请求／响应错误；不标记上报。
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

// EndEvents 在 ctx 下读取原未处理结束事件页，返回非 nil 的事件身份与原 JSON 载荷或原请求／响应错误；不冻结、释放或标记事件。
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

// ChargeCommand 在 ctx 下按 orderID 读取原启动命令，未找到返回 nil,nil；找到时核对命令非 nil及订单 ID一致，返回原命令或请求／身份失配错误；无写入。
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

// mark 接收 ctx、c、path、payload 与 requested，提交原标记请求并返回0至 requested 的实际推进数或请求／缺失进度／范围错误。
// 零为合法重放，计数仅来自明确响应，不猜测传输失败后的提交结果，不重发。
func mark(ctx context.Context, c *Client, path string, payload any, requested int) (int, error) {
	// markWireReply 只校验原进度字段是否存在，不把缺失当正常零值。
	type markWireReply struct {
		Marked *int `json:"marked"` // 原实际推进数；nil为缺失，0为合法重放
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

// MarkStartResultsReported 在 ctx 下按 ids 原启动命令身份集合请求标记上报，返回明确实际推进数或原请求／响应错误；零为重放，不更换命令身份。
func (c *Client) MarkStartResultsReported(ctx context.Context, ids []string) (int, error) {
	return mark(ctx, c, gatewaycontract.MarkStartResultsPath, gatewaycontract.MarkStartResultsRequest{CommandIDs: ids}, len(ids))
}

// MarkDeviceEventsProcessed 在 ctx 下按 ids 原设备事件身份集合请求处理标记，返回明确推进数或原请求／响应错误；零为重放，不猜测失败后的持久状态。
func (c *Client) MarkDeviceEventsProcessed(ctx context.Context, ids []uint64) (int, error) {
	return mark(ctx, c, gatewaycontract.MarkDeviceEventsPath, gatewaycontract.MarkDeviceEventsRequest{IDs: ids}, len(ids))
}

// ReleasePort 在 ctx 下按 request 原端口和订单号请求释放，返回 released=true 的原 Reply或请求／未释放响应错误。
// 保留 already 重放事实，本方法不核对中央结束状态，不自行重复请求或释放其它占用。
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

// MeterSamples 在 ctx 下按 query 原设备、ack/end UTC RFC3339Nano时刻及 uint64 endID 查询计量事件，返回非 nil 原载荷列表或请求／响应错误。
// 窗口及 ID上界由服务端原查询执行，本客户端不补造、缩放或推断计量。
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

// FreezeEndDelivery 在 ctx 下提交 request 原事件／订单身份和完整候选载荷，返回非空原冻结 Reply或请求／缺载荷错误。
// 首写及身份比较由网关拥有；本客户端不覆盖存量、重新生成命令或重试未知写入。
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

// CompensateStart 在 ctx 下以 orderNo 和 commandID 请求原启动补偿，校验订单号后提交原身份，只有明确 accepted=true 返回 nil。
// 返回输入、传输／业务／响应错误；不推断设备从未启动，不在客户端执行补偿算法或资金退款。
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
