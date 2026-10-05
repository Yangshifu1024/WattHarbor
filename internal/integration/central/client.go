// Package central 实现 central 内部 HTTP 接口的有类型客户端。
package central

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	centralcontract "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
)

var ErrInvalidResponse = centralcontract.ErrInvalidResponse

// BusinessError 保留中央共享业务错误类型及原业务码，供 errors.As 使用，不携响应正文或令牌。
type BusinessError = centralcontract.BusinessError

// Client 是中央内部接口的具名 HTTP 客户端，持有已注入传输、地址与服务令牌；不持有业务数据库、执行资金算法或自行管理进程资源。
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
		return nil, errors.New("central client requires an HTTP base URL and service token")
	}
	return &Client{transport: transport, baseURL: strings.TrimRight(baseURL, "/"), token: token}, nil
}

// PaidStarts 在 ctx 下读取 afterID 后单页待启动订单，返回原有序记录或传输／业务／缺失及游标失配错误。
// 空页保持非 nil 列表；每个 ID 须严格递增且订单号非空，游标推进与整轮重扫由消费者负责，无业务写入。
func (c *Client) PaidStarts(ctx context.Context, afterID uint64) ([]centralcontract.PaidStartOrder, error) {
	path := centralcontract.PaidStartsPath + "?after_id=" + strconv.FormatUint(afterID, 10)
	data, err := get[centralcontract.PaidStartsReply](ctx, c, path)
	if err != nil {
		return nil, err
	}
	if data.Items == nil {
		return nil, fmt.Errorf("%w: response has no items", ErrInvalidResponse)
	}
	previous := afterID
	for _, row := range data.Items {
		if row.ID <= previous || row.OrderNo == "" {
			return nil, fmt.Errorf("%w: paid start cursor or order mismatch", ErrInvalidResponse)
		}
		previous = row.ID
	}
	return data.Items, nil
}

// envelopeReply 保留原中央信封 code/data 的缺失状态，供客户端验证明确成功，不把缺失字段解读为默认成功。
type envelopeReply[T any] struct {
	Code *int `json:"code"` // 必需业务码，nil表示缺失
	Data *T   `json:"data"` // 必需原端点数据，nil表示缺失
}

// get 接收 ctx、c 与 path，发送带服务令牌的原 GET并解码类型 T，返回核验后的数据或原 HTTP／业务／取消及响应错误。
// JSON 解码失败保留 ErrInvalidResponse 与底层原因；不自动重试或写业务数据。
func get[T any](ctx context.Context, c *Client, path string) (T, error) {
	var empty T
	status, body, err := c.transport.GetStatus(ctx, c.baseURL, c.token, path)
	if err != nil {
		return empty, err
	}
	var reply envelopeReply[T]
	if err := json.Unmarshal(body, &reply); err != nil {
		return empty, fmt.Errorf("%w: %w", ErrInvalidResponse, err)
	}
	return decode(status, reply)
}

// post 接收 ctx、c、path 与 payload，以原 JSON POST 调用并解码 T，返回数据或 HTTP／业务／取消及响应错误。
// 已收到状态且非 HTTP 拒绝的读取／解码失败包裹 ErrInvalidResponse；未知结果不重发、不更换 payload 身份。
func post[T any](ctx context.Context, c *Client, path string, payload any) (T, error) {
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
	return decode(status, reply)
}

// decode 接收 status 与 reply，仅接受 HTTP200、明确 code0及非 nil data，返回原 T；状态／缺字段错误为 ErrInvalidResponse，非零码为 BusinessError；无 IO。
func decode[T any](status int, reply envelopeReply[T]) (T, error) {
	var empty T
	if status != http.StatusOK {
		return empty, fmt.Errorf("%w: returned HTTP %d, want 200", ErrInvalidResponse, status)
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
