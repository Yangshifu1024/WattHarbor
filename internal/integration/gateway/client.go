// Package gateway 实现 gateway 内部 HTTP 接口的有类型客户端。
package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	gatewaycontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
)

var ErrInvalidResponse = gatewaycontract.ErrInvalidResponse

// BusinessError 保留网关共享业务错误类型及原业务码，供 errors.As 分类；不携内部正文或令牌。
type BusinessError = gatewaycontract.BusinessError

// Client 是网关内部接口的具名 HTTP 客户端，持有注入传输、地址和服务令牌；不访问数据库、设备连接表或自行创建进程资源。
type Client struct {
	// transport 已注入的服务 HTTP 能力，复制配置值并共享其 HTTP 客户端；负责原服务令牌、超时与传输错误，不由本类型关闭连接池。
	transport serviceclient.Client
	// baseURL 已通过构造校验且去除尾部斜杠的目标服务地址，只用于拼接既有内部端点。
	baseURL string
	// token 非空服务鉴权令牌，由原传输能力写请求头，不进入业务 DTO 或日志。
	token string
}

// NewClient 接收 transport、baseURL 与 token，校验 HTTP(S) 地址、主机、无用户信息／查询／fragment及非空令牌，返回持有原传输能力的客户端或配置错误。
// 仅去除地址尾部斜杠，不发请求、不新建业务身份或数据库资源；共享 HTTP 连接池的生命周期仍由装配方管理。
func NewClient(transport serviceclient.Client, baseURL, token string) (*Client, error) {
	base, err := url.Parse(baseURL)
	if err != nil || base == nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || token == "" {
		return nil, errors.New("gateway client requires an HTTP base URL and service token")
	}
	return &Client{transport: transport, baseURL: strings.TrimRight(baseURL, "/"), token: token}, nil
}

// ChargingEvidence 在 ctx 下提交 queries 的1至20项原设备／端口证据查询，返回同数量且逐项身份匹配的原证据页或输入、传输／业务／响应错误。
// 不合并分页、不推断证据完整，也不执行停机或计费，分页策略由调用用例负责。
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

// RequestStop 在 ctx 下提交 request 原订单、用户及来源，返回HTTP202且 accepted=true、stopped=false并含命令／状态的受理 Reply，或传输／业务／响应错误。
// 仅确认网关受理，不确认设备已停止或资金处理，不自行重试未知结果。
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

// envelopeReply 保留原网关信封 code/data 的缺失状态，供原端点成功校验，不把缺失业务码作为成功。
type envelopeReply[T any] struct {
	Code *int `json:"code"` // 必需业务码，nil表示缺失
	Data *T   `json:"data"` // 必需原端点数据，nil表示缺失
}

// RequestStart 在 ctx 下提交 request 原订单号，先拒绝空、超长或两侧空白，再核对HTTP202、原订单号、非空命令／状态及 acked 标志一致。
// 返回受理 Reply 或输入、传输／业务／响应错误；不推进中央订单，不把受理当新设备回执，不自行重发。
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

// post 接收 ctx、c、path、payload 与 expectedStatus，发送原 JSON POST，返回明确 code0和非 nil data 的 T 或状态、业务、传输／解码／取消错误。
// 已收到状态后的非 HTTP 拒绝读取／解码失败保留 ErrInvalidResponse；不生成重试身份或关闭注入资源。
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
