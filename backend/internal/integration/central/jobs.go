package central

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	centralcontract "chargepilot/internal/contracts/central"
	"chargepilot/internal/platform/serviceclient"
)

const (
	jobTimeout    = 30 * time.Second // 请求 context 和复制 HTTP 客户端使用同一上限，调用方更短的期限优先。
	jobReplyLimit = 4096             // 资金任务响应的最大字节数；读取额外一字节识别超限。
)

// DispatchBilling 触发一次计费批次，返回校验后的进度。
// 只有 HTTP 503 且 code=5003 可返回部分进度；其他失败返回零值，不自动重试。
func (c *Client) DispatchBilling(ctx context.Context, _ centralcontract.BillingDispatchRequest) (centralcontract.BillingDispatchReply, error) {
	return postJob(ctx, c, centralcontract.BillingDispatchPath, false, decodeBillingJob)
}

// DispatchRefunds 触发一次退款批次，返回处理进度和渠道是否启用。
// HTTP 503 且 code=5003 时允许缺少 enabled 并保留部分进度；其他失败返回零值，不自动重试或更换退款编号。
func (c *Client) DispatchRefunds(ctx context.Context, _ centralcontract.RefundDispatchRequest) (centralcontract.RefundDispatchReply, error) {
	return postJob(ctx, c, centralcontract.RefundDispatchPath, false, decodeRefundJob)
}

// AutoStop 触发一次停机扫描，请求正文保持 JSON null。
// HTTP 503 且 code=5003 时可保留部分进度；处理数量不表示设备已停电，不自动重试。
func (c *Client) AutoStop(ctx context.Context, _ centralcontract.AutoStopRequest) (centralcontract.AutoStopReply, error) {
	return postJob(ctx, c, centralcontract.AutoStopPath, true, decodeAutoStopJob)
}

// postJob 发送一次任务请求，nullJSON 决定发送 JSON null 还是空正文。
// 只接受 HTTP 200、code=0 的成功响应，或 HTTP 503、code=5003 的部分进度。
// 共享连接池且不修改注入的客户端；错误保留原分类。
func postJob[T any](ctx context.Context, c *Client, path string, nullJSON bool, decode func(json.RawMessage, bool) (T, error)) (T, error) {
	var empty T
	ctx, cancel := context.WithTimeout(ctx, jobTimeout)
	defer cancel()
	var body io.Reader
	if nullJSON {
		body = strings.NewReader("null")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, body)
	if err != nil {
		return empty, err
	}
	request.Header.Set("X-Service-Token", c.token)
	if nullJSON {
		request.Header.Set("Content-Type", "application/json")
	}
	client := http.Client{}
	if c.transport.HTTP != nil {
		client = *c.transport.HTTP
	}
	client.Timeout = jobTimeout
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return empty, err
	}
	defer response.Body.Close()
	var statusErr error
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		statusErr = &serviceclient.HTTPError{StatusCode: response.StatusCode, Path: path}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, jobReplyLimit+1))
	if err != nil {
		return empty, errors.Join(statusErr, fmt.Errorf("%w: read job response: %w", ErrInvalidResponse, err))
	}
	if len(data) > jobReplyLimit {
		return empty, errors.Join(statusErr, fmt.Errorf("%w: job response exceeds byte limit", ErrInvalidResponse))
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 && response.StatusCode != http.StatusOK {
		return empty, fmt.Errorf("%w: job returned HTTP %d, want 200", ErrInvalidResponse, response.StatusCode)
	}
	var reply jobEnvelopeReply
	if err := json.Unmarshal(data, &reply); err != nil {
		return empty, errors.Join(statusErr, fmt.Errorf("%w: decode job response: %w", ErrInvalidResponse, err))
	}
	if reply.Code == nil {
		return empty, errors.Join(statusErr, fmt.Errorf("%w: job response has no business code", ErrInvalidResponse))
	}
	if *reply.Code != 0 {
		statusErr = errors.Join(statusErr, BusinessError{Code: *reply.Code})
	}
	switch response.StatusCode {
	case http.StatusOK:
		if *reply.Code != 0 {
			return empty, statusErr
		}
	case http.StatusServiceUnavailable:
		if *reply.Code != 5003 {
			return empty, errors.Join(statusErr, fmt.Errorf("%w: incomplete job has mismatched business code", ErrInvalidResponse))
		}
	default:
		if response.StatusCode == http.StatusUnauthorized && *reply.Code == 1001 && string(reply.Data) == "null" {
			return empty, statusErr
		}
		return empty, errors.Join(statusErr, fmt.Errorf("%w: unexpected job HTTP status or error data", ErrInvalidResponse))
	}
	if len(reply.Data) == 0 || string(reply.Data) == "null" {
		return empty, errors.Join(statusErr, fmt.Errorf("%w: job response has no data", ErrInvalidResponse))
	}
	out, err := decode(reply.Data, response.StatusCode == http.StatusOK && *reply.Code == 0)
	if err != nil {
		return empty, errors.Join(statusErr, err)
	}
	return out, statusErr
}

// jobEnvelopeReply 保存响应的业务码和数据；字段缺失或 null 不能当成零进度成功。
type jobEnvelopeReply struct {
	Code *int            `json:"code"` // 必需业务码；指针区分明确零值与缺失或 null。
	Data json.RawMessage `json:"data"` // 成功或未完成时须包含对象；鉴权拒绝时为 null，进度字段由对应解码器检查。
}

// decodeBillingJob 要求 completed 为非负整数，成功和失败响应都必须提供。
func decodeBillingJob(data json.RawMessage, _ bool) (centralcontract.BillingDispatchReply, error) {
	var wire struct {
		Completed *int `json:"completed"` // 计费数量；nil 表示缺失或 null，不能推定为零。
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return centralcontract.BillingDispatchReply{}, fmt.Errorf("%w: billing data: %w", ErrInvalidResponse, err)
	}
	if wire.Completed == nil || *wire.Completed < 0 {
		return centralcontract.BillingDispatchReply{}, fmt.Errorf("%w: billing completed is missing or negative", ErrInvalidResponse)
	}
	return centralcontract.BillingDispatchReply{Completed: *wire.Completed}, nil
}

// decodeRefundJob 要求 processed 为非负整数，成功时还须提供 enabled。
// enabled=false 时数量必须为零；失败时可省略 enabled，但不能将其设为 null。
func decodeRefundJob(data json.RawMessage, success bool) (centralcontract.RefundDispatchReply, error) {
	var wire struct {
		Enabled   json.RawMessage `json:"enabled"`   // 成功时必需，失败时可省略；null 或非布尔值无效。
		Processed *int            `json:"processed"` // 必需已处理数量；nil 不能当作零。
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return centralcontract.RefundDispatchReply{}, fmt.Errorf("%w: refund data: %w", ErrInvalidResponse, err)
	}
	var enabled *bool
	if len(wire.Enabled) != 0 {
		var value *bool
		if err := json.Unmarshal(wire.Enabled, &value); err != nil || value == nil {
			return centralcontract.RefundDispatchReply{}, fmt.Errorf("%w: refund enabled is not a bool", ErrInvalidResponse)
		}
		enabled = value
	}
	if wire.Processed == nil || *wire.Processed < 0 || (success && enabled == nil) ||
		(enabled != nil && !*enabled && *wire.Processed > 0) {
		return centralcontract.RefundDispatchReply{}, fmt.Errorf("%w: refund progress or enabled mismatch", ErrInvalidResponse)
	}
	return centralcontract.RefundDispatchReply{Enabled: enabled, Processed: *wire.Processed}, nil
}

// decodeAutoStopJob 要求 stopped 为非负整数，成功和失败响应都必须提供。
func decodeAutoStopJob(data json.RawMessage, _ bool) (centralcontract.AutoStopReply, error) {
	var wire struct {
		Stopped *int `json:"stopped"` // 扫描数量；nil 表示缺失或 null，不能推定为零。
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return centralcontract.AutoStopReply{}, fmt.Errorf("%w: auto-stop data: %w", ErrInvalidResponse, err)
	}
	if wire.Stopped == nil || *wire.Stopped < 0 {
		return centralcontract.AutoStopReply{}, fmt.Errorf("%w: auto-stop stopped is missing or negative", ErrInvalidResponse)
	}
	return centralcontract.AutoStopReply{Stopped: *wire.Stopped}, nil
}
