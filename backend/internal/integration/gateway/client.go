// Package gateway 实现 gateway 内部 HTTP 接口的有类型客户端。
package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	gatewaycontract "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/platform/serviceclient"
)

var ErrInvalidResponse = gatewaycontract.ErrInvalidResponse

// BusinessError 保留网关共享业务错误类型及业务码，供 errors.As 分类；不携内部正文或令牌。
type BusinessError = gatewaycontract.BusinessError

// Client 通过注入的 HTTP 客户端和服务令牌调用 gateway；连接池由进程管理。
type Client struct {
	// transport 注入的 HTTP 调用配置，共享连接池；由进程负责关闭。
	transport serviceclient.Client
	// baseURL 已通过构造校验且去除尾部斜杠的目标服务地址，只用于拼接既有内部端点。
	baseURL string
	// token 服务令牌，写入请求头，不进入业务参数或日志。
	token string
}

// NewClient 校验 HTTP(S) 地址和非空服务令牌；地址不得含用户信息、查询参数或片段。
// 共享注入的连接池，不发送请求。
func NewClient(transport serviceclient.Client, baseURL, token string) (*Client, error) {
	base, err := url.Parse(baseURL)
	if err != nil || base == nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || token == "" {
		return nil, errors.New("gateway client requires an HTTP base URL and service token")
	}
	return &Client{transport: transport, baseURL: strings.TrimRight(baseURL, "/"), token: token}, nil
}

// ChargingEvidence 批量查询 1 至 20 个端口的充电记录，校验每项响应与请求对应。
// 分页和完整性判断由调用方负责。
func (c *Client) ChargingEvidence(ctx context.Context, queries []gatewaycontract.EvidenceQueryRequest) ([]gatewaycontract.EvidencePageReply, error) {
	if len(queries) == 0 || len(queries) > gatewaycontract.MaxEvidenceBatch {
		return nil, errors.New("charging evidence requires 1–20 queries")
	}
	data, err := post[gatewaycontract.EvidenceReply](ctx, c, gatewaycontract.ChargingEvidencePath, gatewaycontract.EvidenceRequest{Requests: queries}, http.StatusOK)
	if err != nil {
		return nil, err
	}
	if len(data.Evidence) != len(queries) {
		return nil, fmt.Errorf("%w: evidence count differs from request", ErrInvalidResponse)
	}
	for i, page := range data.Evidence {
		if page.DeviceID != queries[i].DeviceID || page.PortNo != queries[i].PortNo {
			return nil, fmt.Errorf("%w: evidence identity differs from request", ErrInvalidResponse)
		}
	}
	return data.Evidence, nil
}

// RequestStop 提交停机请求；响应必须为 HTTP 202、accepted=true、stopped=false，且含命令和状态。
// 这只表示网关已接受请求，不表示设备已停止；结果不确定时不自动重试。
func (c *Client) RequestStop(ctx context.Context, request gatewaycontract.StopRequest) (gatewaycontract.StopReply, error) {
	data, err := post[gatewaycontract.StopReply](ctx, c, gatewaycontract.StopPath, request, http.StatusAccepted)
	if err != nil {
		return data, err
	}
	if !data.Accepted || data.Stopped || data.CommandID == "" || data.Status == "" {
		return gatewaycontract.StopReply{}, fmt.Errorf("%w: stop acceptance missing or inconsistent", ErrInvalidResponse)
	}
	return data, nil
}

// envelopeReply 用指针区分字段缺失与明确的零值，缺失 code 或 data 不能算成功。
type envelopeReply[T any] struct {
	Code *int `json:"code"` // 必需业务码，nil 表示缺失
	Data *T   `json:"data"` // 必需对应端点数据，nil 表示缺失
}

// RequestStart 提交订单启动请求，拒绝空、超长或两侧带空白的订单号。
// 响应须为 HTTP 202，订单号、命令、状态和 acked 标志必须一致；不自动重发。
func (c *Client) RequestStart(ctx context.Context, request gatewaycontract.StartRequest) (gatewaycontract.StartReply, error) {
	if request.OrderNo == "" || len(request.OrderNo) > 64 || strings.TrimSpace(request.OrderNo) != request.OrderNo {
		return gatewaycontract.StartReply{}, errors.New("invalid paid charge order number")
	}
	data, err := post[gatewaycontract.StartReply](ctx, c, gatewaycontract.StartPath, request, http.StatusAccepted)
	if err != nil {
		return data, err
	}
	if data.OrderNo != request.OrderNo || data.CommandID == "" || data.Status == "" || data.DeviceAcknowledged != (data.Status == "acked") {
		return gatewaycontract.StartReply{}, fmt.Errorf("%w: start acceptance missing or inconsistent", ErrInvalidResponse)
	}
	return data, nil
}

// post 发送一次 JSON POST，核对预期 HTTP 状态、code=0 和非空 data。
// 收到响应后的读取或解码失败保留 ErrInvalidResponse；不自动重试。
func post[T any](ctx context.Context, c *Client, path string, payload any, expectedStatus int) (T, error) {
	var empty T
	var reply envelopeReply[T]
	status, err := c.transport.PostStatus(ctx, c.baseURL, c.token, path, payload, &reply)
	if err != nil {
		var rejection *serviceclient.HTTPError
		if status != 0 && !errors.As(err, &rejection) {
			return empty, fmt.Errorf("%w: %w", ErrInvalidResponse, err)
		}
		return empty, err
	}
	if status != expectedStatus {
		return empty, fmt.Errorf("%w: %s returned HTTP %d, want %d", ErrInvalidResponse, path, status, expectedStatus)
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
