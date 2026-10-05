package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// vendorUnavailable 返回新的统一厂商依赖错误，隐藏认证、响应正文及存储细节；无 IO。
func vendorUnavailable() *contract.VendorFailure {
	return &contract.VendorFailure{Status: 503, Code: 5003, Message: "厂商服务暂时不可用，请稍后重试"}
}

// Vendors 在 ctx 下用 q 查询公开字典并传递 requestID；返回分页或原服务错误，无重试或业务写入。
func (c *Client) Vendors(ctx context.Context, q contract.VendorQuery, requestID string) (contract.VendorPage, error) {
	query := url.Values{"page": {strconv.Itoa(q.Page)}, "page_size": {strconv.Itoa(q.PageSize)}, "keyword": {q.Keyword}, "status": {q.Status}}
	if len(q.IDs) > 0 {
		ids := make([]string, len(q.IDs))
		for i, id := range q.IDs {
			ids[i] = strconv.FormatUint(id, 10)
		}
		query.Set("ids", strings.Join(ids, ","))
	}
	var out contract.VendorPage
	err := c.requestVendor(ctx, http.MethodGet, "", query, nil, &out, requestID)
	return out, err
}

// Vendor 在 ctx 下读取 id 的公开字典并传递 requestID；返回原事实或公开错误，错误身份不接受，不重试。
func (c *Client) Vendor(ctx context.Context, id uint64, requestID string) (contract.Vendor, error) {
	var out contract.Vendor
	err := c.requestVendor(ctx, http.MethodGet, "/"+strconv.FormatUint(id, 10), nil, nil, &out, requestID)
	if err == nil && out.ID != id {
		err = vendorUnavailable()
	}
	return out, err
}

// CreateVendor 在 ctx 下原样提交 in 并传递 requestID，返回服务确认的公开字典或原错误；不添加幂等或重试。
func (c *Client) CreateVendor(ctx context.Context, in contract.VendorInput, requestID string) (contract.Vendor, error) {
	var out contract.Vendor
	err := c.requestVendor(ctx, http.MethodPost, "", nil, in, &out, requestID)
	return out, err
}

// UpdateVendor 在 ctx 下为 id 原样提交 in 并传递 requestID，返回原公开字典或错误；错误身份不接受，不重试。
func (c *Client) UpdateVendor(ctx context.Context, id uint64, in contract.VendorInput, requestID string) (contract.Vendor, error) {
	var out contract.Vendor
	err := c.requestVendor(ctx, http.MethodPut, "/"+strconv.FormatUint(id, 10), nil, in, &out, requestID)
	if err == nil && out.ID != id {
		err = vendorUnavailable()
	}
	return out, err
}

// requestVendor 在 ctx 下以 method/path/query 访问已知厂商契约，将 body 序列化并解析 out，requestID 仅追踪。
// 返回原公开业务错误或统一依赖错误；十秒及一 MiB 限制，禁止重定向，复用进程传输池，不重试。
func (c *Client) requestVendor(ctx context.Context, method, path string, query url.Values, body, out any, requestID string) error {
	endpoint := c.baseURL + contract.VendorPath + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return vendorUnavailable()
		}
		payload = bytes.NewReader(encoded)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, endpoint, payload)
	if err != nil {
		return vendorUnavailable()
	}
	req.Header.Set("X-Service-Token", c.token)
	req.Header.Set("X-Request-ID", requestID)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := http.Client{}
	if c.transport.HTTP != nil {
		client = *c.transport.HTTP
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return vendorUnavailable()
	}
	defer resp.Body.Close()
	const limit = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(raw) > limit {
		return vendorUnavailable()
	}
	var envelope struct {
		Code    *int            `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return vendorUnavailable()
	}
	if envelope.Code == nil {
		return vendorUnavailable()
	}
	if resp.StatusCode != http.StatusOK || *envelope.Code != 0 {
		switch resp.StatusCode {
		case http.StatusBadRequest, http.StatusNotFound, http.StatusConflict:
			if envelope.Message != "" && *envelope.Code != 0 {
				return &contract.VendorFailure{Status: resp.StatusCode, Code: *envelope.Code, Message: envelope.Message}
			}
		}
		return vendorUnavailable()
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" || json.Unmarshal(envelope.Data, out) != nil || !validVendorResponse(out) {
		return vendorUnavailable()
	}
	return nil
}

// validVendorResponse 核对 out 的厂商身份字段及分页边界；返回合法性，无 IO 或修改。
func validVendorResponse(out any) bool {
	validRow := func(row contract.Vendor) bool {
		return row.ID > 0 && row.VendorCode != "" && row.VendorName != "" && row.AdapterClass != "" && row.Protocol != "" && (row.Status == "enabled" || row.Status == "disabled")
	}
	switch result := out.(type) {
	case *contract.Vendor:
		return validRow(*result)
	case *contract.VendorPage:
		if result.Page < 1 || result.PageSize < 1 || result.PageSize > 100 || result.Total < int64(len(result.Items)) {
			return false
		}
		for _, row := range result.Items {
			if !validRow(row) {
				return false
			}
		}
		return true
	default:
		return false
	}
}
