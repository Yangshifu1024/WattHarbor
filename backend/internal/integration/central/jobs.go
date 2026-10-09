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
	jobReplyLimit = 4096             // 资金任务信封的最大字节数；读取额外一字节识别超限。
)

// DispatchBilling 在 ctx 下触发一次无业务参数的计费批次，返回已验证回执及调用错误。
// 仅原 503／code=5003 回执可带部分进度；传输失败或形状失配返回零值，不推断资金状态或自动重试。
func (c *Client) DispatchBilling(ctx context.Context, _ centralcontract.BillingDispatchRequest) (centralcontract.BillingDispatchReply, error) {
	return postJob(ctx, c, centralcontract.BillingDispatchPath, false, decodeBillingJob)
}

// DispatchRefunds 在 ctx 下触发一次无业务参数的退款批次，返回已验证进度及渠道启用标志。
// 原 503／code=5003 回执可缺失 enabled 并保留已验证进度；其他失败返回零值，不能据未知结果更换退款身份或自动重试。
func (c *Client) DispatchRefunds(ctx context.Context, _ centralcontract.RefundDispatchRequest) (centralcontract.RefundDispatchReply, error) {
	return postJob(ctx, c, centralcontract.RefundDispatchPath, false, decodeRefundJob)
}

// AutoStop 在 ctx 下触发一次无业务参数的停机扫描，保持原 null JSON 请求并返回已验证数量。
// 原 503／code=5003 回执保留合法部分进度；其他失败返回零值，错误不代表设备已停电，不自动重试或生成命令身份。
func (c *Client) AutoStop(ctx context.Context, _ centralcontract.AutoStopRequest) (centralcontract.AutoStopReply, error) {
	return postJob(ctx, c, centralcontract.AutoStopPath, true, decodeAutoStopJob)
}

// postJob 按 path 发出单次任务 POST；nullJSON 决定保留原 null 正文或无正文形状。
// decode 校验业务载荷，success 表达严格 200／code=0；只接受此成功形状或原 503／code=5003 未完成进度。
// 不修改共享 HTTP 客户端或 Transport；非成功 HTTP 与失配错误可同时保留，调用方可 errors.Is/As 分类。
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

// jobEnvelopeReply 仅保留任务信封的明确业务码和原载荷；缺失或 null 不作为零进度成功。
type jobEnvelopeReply struct {
	Code *int            `json:"code"` // 必需业务码；指针区分明确零值与缺失或 null。
	Data json.RawMessage `json:"data"` // 成功及未完成需对象载荷；原身份拒绝为 null，由所属解码器验证进度字段。
}

// decodeBillingJob 校验 data 中明确、非负且未溢出的 completed，返回计费回执或失配错误；无 IO。
// success 不改变 completed 的必需性，失败回执同样必须明确声明进度。
func decodeBillingJob(data json.RawMessage, _ bool) (centralcontract.BillingDispatchReply, error) {
	var wire struct {
		Completed *int `json:"completed"` // 原计费数量；nil 表示缺失或 null，不能推定为零。
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return centralcontract.BillingDispatchReply{}, fmt.Errorf("%w: billing data: %w", ErrInvalidResponse, err)
	}
	if wire.Completed == nil || *wire.Completed < 0 {
		return centralcontract.BillingDispatchReply{}, fmt.Errorf("%w: billing completed is missing or negative", ErrInvalidResponse)
	}
	return centralcontract.BillingDispatchReply{Completed: *wire.Completed}, nil
}

// decodeRefundJob 校验 data 中明确非负的 processed，success 时还要求明确 enabled。
// 未启用且有正进度是失配；失败回执允许不提供 enabled。返回合法回执或错误，无 IO。
func decodeRefundJob(data json.RawMessage, success bool) (centralcontract.RefundDispatchReply, error) {
	var wire struct {
		Enabled   json.RawMessage `json:"enabled"`   // 成功必需、失败可缺失；显式 null 或非 bool 一律失配。
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

// decodeAutoStopJob 校验 data 中明确、非负且未溢出的 stopped，返回扫描回执或失配错误；无 IO。
// success 不改变 stopped 的必需性，失败回执同样必须明确声明进度。
func decodeAutoStopJob(data json.RawMessage, _ bool) (centralcontract.AutoStopReply, error) {
	var wire struct {
		Stopped *int `json:"stopped"` // 原扫描数量；nil 表示缺失或 null，不能推定为零。
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return centralcontract.AutoStopReply{}, fmt.Errorf("%w: auto-stop data: %w", ErrInvalidResponse, err)
	}
	if wire.Stopped == nil || *wire.Stopped < 0 {
		return centralcontract.AutoStopReply{}, fmt.Errorf("%w: auto-stop stopped is missing or negative", ErrInvalidResponse)
	}
	return centralcontract.AutoStopReply{Stopped: *wire.Stopped}, nil
}
