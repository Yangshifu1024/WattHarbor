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

type BusinessError = gatewaycontract.BusinessError

type Client struct {
	transport serviceclient.Client
	baseURL   string
	token     string
}

func NewClient(transport serviceclient.Client, baseURL, token string) (*Client, error) {
	base, err := url.Parse(baseURL)
	if err != nil || base == nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || token == "" {
		return nil, errors.New("gateway client requires an HTTP base URL and service token")
	}
	return &Client{transport: transport, baseURL: strings.TrimRight(baseURL, "/"), token: token}, nil
}

// ChargingEvidence 返回一次批量查询的证据页，分页和证据完整性策略由调用用例决定。
func (c *Client) ChargingEvidence(ctx context.Context, queries []gatewaycontract.EvidenceQuery) ([]gatewaycontract.EvidencePage, error) {
	if len(queries) == 0 || len(queries) > gatewaycontract.MaxEvidenceBatch {
		return nil, errors.New("charging evidence requires 1–20 queries")
	}
	data, err := post[gatewaycontract.EvidenceData](ctx, c, gatewaycontract.ChargingEvidencePath, gatewaycontract.EvidenceRequest{Requests: queries}, http.StatusOK)
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

// RequestStop 只确认 gateway 以 202 受理命令，不确认设备已停止。
func (c *Client) RequestStop(ctx context.Context, request gatewaycontract.StopRequest) (gatewaycontract.StopAcceptance, error) {
	data, err := post[gatewaycontract.StopAcceptance](ctx, c, gatewaycontract.StopPath, request, http.StatusAccepted)
	if err != nil {
		return data, err
	}
	if !data.Accepted || data.Stopped || data.CommandID == "" || data.Status == "" {
		return gatewaycontract.StopAcceptance{}, fmt.Errorf("%w: stop acceptance missing or inconsistent", ErrInvalidResponse)
	}
	return data, nil
}

type response[T any] struct {
	Code *int `json:"code"`
	Data *T   `json:"data"`
}

// RequestStart 确认 gateway 以 202 受理原订单号，不推进 central 的订单状态。
func (c *Client) RequestStart(ctx context.Context, request gatewaycontract.StartRequest) (gatewaycontract.StartAcceptance, error) {
	if request.OrderNo == "" || len(request.OrderNo) > 64 || strings.TrimSpace(request.OrderNo) != request.OrderNo {
		return gatewaycontract.StartAcceptance{}, errors.New("invalid paid charge order number")
	}
	data, err := post[gatewaycontract.StartAcceptance](ctx, c, gatewaycontract.StartPath, request, http.StatusAccepted)
	if err != nil {
		return data, err
	}
	if data.OrderNo != request.OrderNo || data.CommandID == "" || data.Status == "" || data.DeviceAcknowledged != (data.Status == "acked") {
		return gatewaycontract.StartAcceptance{}, fmt.Errorf("%w: start acceptance missing or inconsistent", ErrInvalidResponse)
	}
	return data, nil
}

func post[T any](ctx context.Context, c *Client, path string, payload any, expectedStatus int) (T, error) {
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
