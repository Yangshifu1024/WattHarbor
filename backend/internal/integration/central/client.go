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

	centralcontract "chargepilot/internal/contracts/central"
	"chargepilot/internal/platform/serviceclient"
)

var ErrInvalidResponse = centralcontract.ErrInvalidResponse

// BusinessError 保留中央共享业务错误类型及业务码，供 errors.As 使用，不携响应正文或令牌。
type BusinessError = centralcontract.BusinessError

// Client 通过注入的 HTTP 客户端和服务令牌调用 central；连接池由进程管理。
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
		return nil, errors.New("central client requires an HTTP base URL and service token")
	}
	return &Client{transport: transport, baseURL: strings.TrimRight(baseURL, "/"), token: token}, nil
}

// PaidStarts 查询游标之后的一页待启动订单；ID 必须递增，订单号不能为空。
// 无数据时返回空列表，下一页和整轮重查由调用方处理。
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

// envelopeReply 用指针区分字段缺失与明确的零值，缺失 code 或 data 不能算成功。
type envelopeReply[T any] struct {
	Code *int `json:"code"` // 必需业务码，nil 表示缺失
	Data *T   `json:"data"` // 必需对应端点数据，nil 表示缺失
}

// get 发送带服务令牌的 GET 并校验响应；解码失败保留 ErrInvalidResponse 和底层错误。
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

// post 发送一次 JSON POST 并校验响应；收到响应后的读取或解码失败保留 ErrInvalidResponse。
// 结果不确定时不自动重试或更换请求数据。
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

// decode 只接受 HTTP 200、code=0 且 data 非空的响应；非零业务码返回 BusinessError。
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
