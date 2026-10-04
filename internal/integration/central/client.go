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

type BusinessError = centralcontract.BusinessError

type Client struct {
	transport serviceclient.Client
	baseURL   string
	token     string
}

func NewClient(transport serviceclient.Client, baseURL, token string) (*Client, error) {
	base, err := url.Parse(baseURL)
	if err != nil || base == nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || token == "" {
		return nil, errors.New("central client requires an HTTP base URL and service token")
	}
	return &Client{transport: transport, baseURL: strings.TrimRight(baseURL, "/"), token: token}, nil
}

// PaidStarts 读取单页待启动订单；游标推进和整轮重扫由消费者负责。
func (c *Client) PaidStarts(ctx context.Context, afterID uint64) ([]centralcontract.PaidStartOrder, error) {
	path := centralcontract.PaidStartsPath + "?after_id=" + strconv.FormatUint(afterID, 10)
	data, err := get[centralcontract.PaidStartsData](ctx, c, path)
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

type response[T any] struct {
	Code *int `json:"code"`
	Data *T   `json:"data"`
}

func get[T any](ctx context.Context, c *Client, path string) (T, error) {
	var empty T
	status, body, err := c.transport.GetStatus(ctx, c.baseURL, c.token, path)
	if err != nil {
		return empty, err
	}
	var reply response[T]
	if err := json.Unmarshal(body, &reply); err != nil {
		return empty, fmt.Errorf("%w: %w", ErrInvalidResponse, err)
	}
	return decode(status, reply)
}

func post[T any](ctx context.Context, c *Client, path string, payload any) (T, error) {
	var empty T
	var reply response[T]
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

func decode[T any](status int, reply response[T]) (T, error) {
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
