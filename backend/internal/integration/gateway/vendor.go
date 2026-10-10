package gateway

import (
	"bytes"
	contract "chargepilot/internal/contracts/gateway"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// vendorUnavailable 返回统一的厂商服务错误，隐藏凭据、响应正文和存储细节。
func vendorUnavailable() *contract.VendorFailure {
	return &contract.VendorFailure{Status: 503, Code: 5003, Message: "厂商服务暂时不可用，请稍后重试"}
}

// Vendors 查询厂商列表并传递请求追踪号，不自动重试。
func (c *Client) Vendors(ctx context.Context, q contract.VendorListRequest, requestID string) (contract.VendorListReply, error) {
	// Status 保留空串：零值 VendorStatus 按跨进程约定编码为 status=，表示不过滤；
	// 不能改成省略该参数，那会改变接口语义。
	query := url.Values{"page": {strconv.Itoa(q.Page)}, "page_size": {strconv.Itoa(q.PageSize)}, "keyword": {q.Keyword}, "status": {string(q.Status)}}
	if len(q.IDs) > 0 {
		ids := make([]string, len(q.IDs))
		for i, id := range q.IDs {
			ids[i] = strconv.FormatUint(id, 10)
		}
		query.Set("ids", strings.Join(ids, ","))
	}
	var out contract.VendorListReply
	err := c.requestVendor(ctx, http.MethodGet, "", query, nil, &out, requestID)
	return out, err
}

// Vendor 查询指定厂商并核对响应编号，传递请求追踪号，不自动重试。
func (c *Client) Vendor(ctx context.Context, id uint64, requestID string) (contract.VendorReply, error) {
	var out contract.VendorReply
	err := c.requestVendor(ctx, http.MethodGet, "/"+strconv.FormatUint(id, 10), nil, nil, &out, requestID)
	if err == nil && out.ID != id {
		err = vendorUnavailable()
	}
	return out, err
}

// CreateVendor 提交厂商配置并传递请求追踪号，不自动重试。
func (c *Client) CreateVendor(ctx context.Context, in contract.VendorRequest, requestID string) (contract.VendorReply, error) {
	var out contract.VendorReply
	err := c.requestVendor(ctx, http.MethodPost, "", nil, in, &out, requestID)
	return out, err
}

// UpdateVendor 更新指定厂商并核对响应编号，传递请求追踪号，不自动重试。
func (c *Client) UpdateVendor(ctx context.Context, id uint64, in contract.VendorRequest, requestID string) (contract.VendorReply, error) {
	var out contract.VendorReply
	err := c.requestVendor(ctx, http.MethodPut, "/"+strconv.FormatUint(id, 10), nil, in, &out, requestID)
	if err == nil && out.ID != id {
		err = vendorUnavailable()
	}
	return out, err
}

// requestVendor 调用厂商接口并解码响应；公开业务错误保留，其他错误统一隐藏细节。
// 请求最多十秒、响应最多 1 MiB，共享连接池，不跟随重定向或自动重试。
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
	// vendorEnvelopeReply 保留响应中的字段是否存在，便于校验缺失字段。
	type vendorEnvelopeReply struct {
		Code    *int            `json:"code"`    // 业务码，nil 表示缺失
		Message string          `json:"message"` // 公开错误提示，依赖故障不透传
		Data    json.RawMessage `json:"data"`    // 对应端点数据，保留JSON精度
	}
	var envelope vendorEnvelopeReply
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

// validVendorResponse 检查厂商编号、必需字段和分页范围。
func validVendorResponse(out any) bool {
	validRow := func(row contract.VendorReply) bool {
		return row.ID > 0 && row.VendorCode != "" && row.VendorName != "" && row.AdapterClass != "" && row.Protocol != "" && (row.Status == "enabled" || row.Status == "disabled")
	}
	switch result := out.(type) {
	case *contract.VendorReply:
		return validRow(*result)
	case *contract.VendorListReply:
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
