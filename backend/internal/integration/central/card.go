package central

import (
	"bytes"
	contract "chargepilot/internal/contracts/central"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"io"
	"net/http"
	"net/url"
	"time"
)

// SwipeCard 提交刷卡事件，不自动重试；响应丢失后必须继续使用同一事件号。
// 只有 HTTP 409 且业务码为 2009 才算明确拒绝，余额不足标志保持原值。
func (c *Client) SwipeCard(ctx context.Context, in contract.CardSwipeRequest) (contract.CardOperationReply, error) {
	var out contract.CardOperationReply
	err := c.cardRequest(ctx, http.MethodPost, contract.CardSwipePath, in, &out, true)
	if err == nil && uuid.Validate(out.OperationID) != nil {
		err = ErrInvalidResponse
	}
	return out, err
}

// CardBalance 查询卡是否有效及钱包可用余额，不扣款。
func (c *Client) CardBalance(ctx context.Context, cardNo string) (contract.CardBalanceReply, error) {
	// cardBalanceWireReply 保留响应中的字段是否存在，便于校验缺失字段。
	type cardBalanceWireReply struct {
		Valid        *bool  `json:"valid"`         // 卡是否有效，nil 表示响应缺少该字段
		BalanceCents *int64 `json:"balance_cents"` // 可用余额，分；缺失或负数时拒绝响应
	}
	var data cardBalanceWireReply
	err := c.cardRequest(ctx, http.MethodGet, contract.CardBalancePath+"?card_no="+url.QueryEscape(cardNo), nil, &data, false)
	if err != nil {
		return contract.CardBalanceReply{}, err
	}
	if data.Valid == nil || data.BalanceCents == nil || *data.BalanceCents < 0 {
		return contract.CardBalanceReply{}, ErrInvalidResponse
	}
	return contract.CardBalanceReply{Valid: *data.Valid, BalanceCents: *data.BalanceCents}, nil
}

// cardRequest 发送一次刷卡请求；只有刷卡端点的明确拒绝才返回拒绝结果。
// 请求最多五秒、响应最多 64 KiB；共享连接池且不跟随重定向，读取失败不算业务拒绝。
func (c *Client) cardRequest(ctx context.Context, method, path string, payload, out any, decline bool) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Token", c.token)
	client := &http.Client{Timeout: 5 * time.Second}
	if c.transport.HTTP != nil {
		copy := *c.transport.HTTP
		client = &copy
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	if err != nil {
		return err
	}
	if len(body) > 64<<10 {
		return ErrInvalidResponse
	}
	// cardEnvelopeReply 保留响应中的字段是否存在，便于校验缺失字段。
	type cardEnvelopeReply struct {
		Code *int            `json:"code"` // 业务码，nil 不能视为成功
		Data json.RawMessage `json:"data"` // 操作、余额或拒绝详情
	}
	var envelope cardEnvelopeReply
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidResponse, err)
	}
	if decline && resp.StatusCode == http.StatusConflict {
		if envelope.Code == nil || *envelope.Code != 2009 {
			return ErrInvalidResponse
		}
		// 余额不足标志缺失时为 false，不将其他拒绝原因当作余额不足。
		type cardDeclineWireReply struct {
			InsufficientBalance bool `json:"insufficient_balance"` // 明确的余额不足标志
		}
		var data cardDeclineWireReply
		if len(envelope.Data) > 0 {
			if err := json.Unmarshal(envelope.Data, &data); err != nil {
				return err
			}
		}
		return &contract.CardDeclineReply{InsufficientBalance: data.InsufficientBalance}
	}
	if resp.StatusCode != http.StatusOK {
		return errors.New("card service returned HTTP " + fmt.Sprint(resp.StatusCode))
	}
	if envelope.Code == nil || *envelope.Code != 0 || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return ErrInvalidResponse
	}
	return json.Unmarshal(envelope.Data, out)
}
