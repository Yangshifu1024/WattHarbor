package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	contract "wattharbor/internal/contracts/gateway"
)

// queryData 读取查询响应并检查 required 指定的必需字段；next_after_id 可以明确为 null。
// 请求最多五秒，共享连接池，不跟随重定向或自动重试。
func queryData[T any](ctx context.Context, c *Client, path string, required ...string) (T, error) {
	var empty T
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	transport := c.transport
	client := http.Client{Timeout: 5 * time.Second}
	if transport.HTTP != nil {
		client = *transport.HTTP
	}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	transport.HTTP = &client
	status, body, err := transport.GetStatus(ctx, c.baseURL, c.token, path)
	if err != nil {
		return empty, err
	}
	if status != http.StatusOK {
		return empty, fmt.Errorf("%w: HTTP %d, want 200", ErrInvalidResponse, status)
	}
	// queryWireReply 保留响应中的字段是否存在，便于校验缺失字段。
	type queryWireReply struct {
		Code *int            `json:"code"` // 必须存在的业务码
		Data json.RawMessage `json:"data"` // 数据 JSON，不先转换浮点数字
	}
	var wire queryWireReply
	if err := json.Unmarshal(body, &wire); err != nil {
		return empty, fmt.Errorf("%w: %w", ErrInvalidResponse, err)
	}
	if wire.Code == nil {
		return empty, fmt.Errorf("%w: missing business code", ErrInvalidResponse)
	}
	if *wire.Code != 0 {
		return empty, BusinessError{Code: *wire.Code}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(wire.Data, &fields); err != nil || fields == nil {
		return empty, fmt.Errorf("%w: missing query data", ErrInvalidResponse)
	}
	for _, key := range required {
		value, ok := fields[key]
		if !ok || (key != "next_after_id" && bytes.Equal(bytes.TrimSpace(value), []byte("null"))) {
			return empty, fmt.Errorf("%w: missing %s", ErrInvalidResponse, key)
		}
	}
	var out T
	if err := json.Unmarshal(wire.Data, &out); err != nil {
		return empty, fmt.Errorf("%w: %w", ErrInvalidResponse, err)
	}
	return out, nil
}

// validQueryDevice 检查设备编号和端口号是否有效。
func validQueryDevice(device string, port uint8) bool {
	return device != "" && len(device) <= 64 && strings.TrimSpace(device) == device && port != 0
}

// validSeries 检查点数上限和时间格式，缺失的指标不补零。
func validSeries(points []contract.CurvePoint) bool {
	if points == nil || len(points) > contract.MaxQueryPoints {
		return false
	}
	for _, point := range points {
		if _, err := time.Parse(time.RFC3339Nano, point.TS); err != nil {
			return false
		}
	}
	return true
}

// OrderProcess 按订单和游标查询过程记录；ID 必须递增，下一页游标必须等于末条 ID。
// 保留大整数和已上报的零值，不自动重试。
func (c *Client) OrderProcess(ctx context.Context, q contract.OrderProcessRequest) (contract.OrderProcessReply, error) {
	if !validQueryDevice(q.DeviceID, q.PortNo) || q.OrderID == 0 || q.OrderNo == "" || len(q.OrderNo) > 64 || strings.TrimSpace(q.OrderNo) != q.OrderNo || q.Limit == 0 || q.Limit > contract.MaxQueryPoints {
		return contract.OrderProcessReply{}, fmt.Errorf("invalid order process query")
	}
	values := url.Values{"charge_order_id": {strconv.FormatUint(q.OrderID, 10)}, "device_id": {q.DeviceID}, "port_no": {strconv.FormatUint(uint64(q.PortNo), 10)}, "after_id": {strconv.FormatUint(q.AfterID, 10)}, "limit": {strconv.FormatUint(uint64(q.Limit), 10)}}
	data, err := queryData[contract.OrderProcessReply](ctx, c, contract.OrderProcessPath+url.PathEscape(q.OrderNo)+"/process?"+values.Encode(), "items", "next_after_id")
	if err != nil {
		return data, err
	}
	if data.Items == nil || len(data.Items) > int(q.Limit) {
		return contract.OrderProcessReply{}, fmt.Errorf("%w: process page size", ErrInvalidResponse)
	}
	last := q.AfterID
	for _, point := range data.Items {
		if point.ID <= last || point.TS.IsZero() {
			return contract.OrderProcessReply{}, fmt.Errorf("%w: process ordering or timestamp", ErrInvalidResponse)
		}
		last = point.ID
	}
	if data.NextAfterID != nil && (len(data.Items) == 0 || *data.NextAfterID != last) {
		return contract.OrderProcessReply{}, fmt.Errorf("%w: process cursor", ErrInvalidResponse)
	}
	return data, nil
}

// OrderCurve 读取已授权订单的过程采样，设备和端口只用于约束订单范围。
func (c *Client) OrderCurve(ctx context.Context, q contract.OrderCurveRequest) (contract.OrderCurveReply, error) {
	if !validQueryDevice(q.DeviceID, q.PortNo) || q.OrderID == 0 || q.OrderNo == "" ||
		len(q.OrderNo) > 64 || !q.From.Before(q.To) || q.To.Sub(q.From) > contract.MaxOrderCurveWindow {
		return contract.OrderCurveReply{}, fmt.Errorf("invalid order curve query")
	}
	values := url.Values{
		"charge_order_id": {strconv.FormatUint(q.OrderID, 10)}, "device_id": {q.DeviceID},
		"port_no": {strconv.FormatUint(uint64(q.PortNo), 10)}, "from": {q.From.UTC().Format(time.RFC3339Nano)},
		"to": {q.To.UTC().Format(time.RFC3339Nano)}, "granularity": {q.Granularity},
	}
	if q.Latest {
		values.Set("view", "latest")
	} else if q.Summary {
		values.Set("view", "history")
	}
	out, err := queryData[contract.OrderCurveReply](
		ctx, c, contract.OrderProcessPath+url.PathEscape(q.OrderNo)+"/curve?"+values.Encode(),
		"series", "summary", "sampling_interval_seconds",
	)
	if err != nil {
		return out, err
	}
	if !validSeries(out.Series) || out.SamplingIntervalSeconds < 0 || (q.Latest && len(out.Series) > 1) {
		return out, fmt.Errorf("%w: order curve result", ErrInvalidResponse)
	}
	for _, point := range out.Series {
		ts, _ := time.Parse(time.RFC3339Nano, point.TS)
		if ts.Before(q.From) || ts.After(q.To) {
			return out, fmt.Errorf("%w: order curve query window", ErrInvalidResponse)
		}
	}
	return out, nil
}
