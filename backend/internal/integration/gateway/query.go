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

	contract "chargepilot/internal/contracts/gateway"
)

// queryData 在 ctx 下为 c 读取 path 的原查询信封，返回 T 或HTTP／业务／缺字段／解码错误。
// required 指定必须存在且非null的数据字段；next_after_id允许显式null。最多5秒且禁止重定向，不重试。
// 复用已装配Transport，仅复制请求级Client配置；不创建后台任务或额外连接池。
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
	// queryWireReply 保留原信封或数据的字段存在性，仅用于传输解码。
	type queryWireReply struct {
		Code *int            `json:"code"` // 必须存在的原业务码
		Data json.RawMessage `json:"data"` // 原数据JSON，不先转换浮点数字
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

// validQueryDevice 核对 device/port 原规范身份，返回准入结果，不执行网络或数据库操作。
func validQueryDevice(device string, port uint8) bool {
	return device != "" && len(device) <= 64 && strings.TrimSpace(device) == device && port != 0
}

// validSeries 核对 points 的有界长度和可解析时刻，返回契约结果；可空指标不补零，无IO。
func validSeries(points []contract.CurvePoint, count int) bool {
	if points == nil || len(points) > contract.MaxQueryPoints || count != len(points) {
		return false
	}
	for _, point := range points {
		if _, err := time.Parse(time.RFC3339Nano, point.TS); err != nil {
			return false
		}
	}
	return true
}

// Telemetry 在 ctx 下读取 q 原端口的有界遥测，返回实际窗口、空值与原单位或输入／传输／契约错误。
// 参数逐段转义，核对原设备、实际窗口包含关系、点数与分辨率；不将网关故障转换为空曲线。
func (c *Client) Telemetry(ctx context.Context, q contract.TelemetryRequest) (contract.TelemetryReply, error) {
	if !validQueryDevice(q.DeviceID, q.PortNo) || q.From.IsZero() || !q.From.Before(q.To) || q.To.Sub(q.From) > 24*time.Hour {
		return contract.TelemetryReply{}, fmt.Errorf("invalid telemetry query")
	}
	values := url.Values{"port_no": {strconv.FormatUint(uint64(q.PortNo), 10)}, "from": {q.From.UTC().Format(time.RFC3339Nano)}, "to": {q.To.UTC().Format(time.RFC3339Nano)}}
	data, err := queryData[contract.TelemetryReply](ctx, c, contract.TelemetryPath+url.PathEscape(q.DeviceID)+"/telemetry?"+values.Encode(), "device_id", "from", "to", "series", "count", "granularity", "bucket")
	if err != nil {
		return data, err
	}
	if data.DeviceID != q.DeviceID || !data.From.Before(data.To) || data.From.Before(q.From) || data.To.After(q.To) || !validSeries(data.Series, data.Count) || (data.Granularity != "raw" && data.Granularity != "15min" && data.Granularity != "hourly") {
		return contract.TelemetryReply{}, fmt.Errorf("%w: telemetry identity, window or series", ErrInvalidResponse)
	}
	return data, nil
}

// HistoricalCurve 在 ctx 下按 q 的原订单窗口读取聚合曲线，返回原边界标志、摘要及点或输入／调用／契约错误。
// 核对业务号、设备、端口、时刻及粒度；摘要保持原JSON精度，聚合边界允许落在原窗口相邻桶内，无写入。
func (c *Client) HistoricalCurve(ctx context.Context, q contract.HistoricalCurveRequest) (contract.HistoricalCurveReply, error) {
	if !validQueryDevice(q.DeviceID, q.PortNo) || q.OrderNo == "" || len(q.OrderNo) > 64 || strings.TrimSpace(q.OrderNo) != q.OrderNo || q.StartedAt.IsZero() || !q.StartedAt.Before(q.EndedAt) || (q.Granularity != "15min" && q.Granularity != "hourly") {
		return contract.HistoricalCurveReply{}, fmt.Errorf("invalid historical curve query")
	}
	values := url.Values{"order_id": {q.OrderNo}, "port_no": {strconv.FormatUint(uint64(q.PortNo), 10)}, "started_at": {q.StartedAt.UTC().Format(time.RFC3339Nano)}, "ended_at": {q.EndedAt.UTC().Format(time.RFC3339Nano)}, "granularity": {q.Granularity}}
	data, err := queryData[contract.HistoricalCurveReply](ctx, c, contract.HistoricalCurvePath+url.PathEscape(q.DeviceID)+"/historical-curve?"+values.Encode(), "order_id", "device_id", "port_no", "started_at", "ended_at", "granularity", "series", "count", "summary", "boundary_approximate")
	if err != nil {
		return data, err
	}
	if data.OrderNo != q.OrderNo || data.DeviceID != q.DeviceID || data.PortNo != q.PortNo || !data.StartedAt.Equal(q.StartedAt) || !data.EndedAt.Equal(q.EndedAt) || data.Granularity != q.Granularity || data.Summary == nil || !validSeries(data.Series, data.Count) {
		return contract.HistoricalCurveReply{}, fmt.Errorf("%w: historical curve identity, window or series", ErrInvalidResponse)
	}
	return data, nil
}

// OrderProcess 在 ctx 下按 q 原身份与游标读取过程页，返回原序点及末页空游标或输入／调用／契约错误。
// 行主键须严格递增且下一游标等于页尾；精确uint64与已上报零值保持原值，不自动重试或换订单。
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
