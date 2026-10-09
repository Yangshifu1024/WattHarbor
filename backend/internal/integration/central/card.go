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

// SwipeCard 在 ctx 下重发 in 原事件，返回原操作或明确业务拒绝／未知受理错误，不自动重试。
// 只有409及2009被识别为拒绝，不足余额保持原标志；响应丢失必须继续沿用原事件号。
func (c *Client) SwipeCard(ctx context.Context, in contract.CardSwipeRequest) (contract.CardOperationReply, error) {
	var out contract.CardOperationReply
	err := c.cardRequest(ctx, http.MethodPost, contract.CardSwipePath, in, &out, true)
	if err == nil && uuid.Validate(out.OperationID) != nil {
		err = ErrInvalidResponse
	}
	return out, err
}

// CardBalance 在 ctx 下读取 cardNo 原卡有效性及共享钱包余额，返回契约结果或传输错误；不扣款。
func (c *Client) CardBalance(ctx context.Context, cardNo string) (contract.CardBalanceReply, error) {
	// cardBalanceWireReply 保留原信封或数据的字段存在性，仅用于传输解码。
	type cardBalanceWireReply struct {
		Valid        *bool  `json:"valid"`         // 原卡有效性，nil 为契约缺失
		BalanceCents *int64 `json:"balance_cents"` // 可花费分值，nil 或负数不得当作零余额
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

// cardRequest 在 ctx 下用 method/path 发送 payload 并解码 out；decline 仅允许原刷卡端点409决策。
// 请求有5秒期限、64KiB响应边界、禁止重定向并复用注入连接池；读取错误不变成业务拒绝。
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
	// cardEnvelopeReply 保留原信封或数据的字段存在性，仅用于传输解码。
	type cardEnvelopeReply struct {
		Code *int            `json:"code"` // 原业务码，nil 不能视为成功
		Data json.RawMessage `json:"data"` // 原操作、余额或明确拒绝内容
	}
	var envelope cardEnvelopeReply
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidResponse, err)
	}
	if decline && resp.StatusCode == http.StatusConflict {
		if envelope.Code == nil || *envelope.Code != 2009 {
			return ErrInvalidResponse
		}
		// cardDeclineWireReply 保留原信封或数据的字段存在性，仅用于传输解码。
		type cardDeclineWireReply struct {
			InsufficientBalance bool `json:"insufficient_balance"` // 原明确不足余额标志
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
