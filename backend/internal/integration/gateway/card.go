package gateway

import (
	"context"
	"errors"
	contract "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/google/uuid"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// CardEvents 在 ctx 下读取原待投递清单，返回含冻结回复的集合或传输／契约错误；不领取或推进。
func (c *Client) CardEvents(ctx context.Context) ([]contract.CardEvent, error) {
	ctx, cancel, client := c.cardBound(ctx)
	defer cancel()
	out, err := get[contract.CardEventsReply](ctx, client, contract.CardEventsPath)
	if err != nil {
		return nil, err
	}
	if out.Items == nil {
		return nil, ErrInvalidResponse
	}
	for _, row := range out.Items {
		if uuid.Validate(row.EventKey) != nil || row.Payload == "" {
			return nil, ErrInvalidResponse
		}
	}
	return out.Items, nil
}

// FreezeCardResponse 在 ctx 下为 in 原事件首写决定，返回权威冻结 JSON 或错误，不生成新事件。
func (c *Client) FreezeCardResponse(ctx context.Context, in contract.CardFreezeRequest) (contract.CardFreezeReply, error) {
	ctx, cancel, client := c.cardBound(ctx)
	defer cancel()
	if uuid.Validate(in.EventKey) != nil || in.ResponseJSON == "" {
		return contract.CardFreezeReply{}, errors.New("invalid card freeze identity")
	}
	var empty contract.CardFreezeReply
	// cardFreezeWireReply 是冻结卡回复端点的解码投影，以可空 Stored 区分缺字段与正常竞争重放，再转换为共享回执。
	type cardFreezeWireReply struct {
		Stored       *bool  `json:"stored"`        // nil 表示缺少首写结果，false 仍为正常竞争重放
		ResponseJSON string `json:"response_json"` // 原权威冻结回复
	}
	out, err := post[cardFreezeWireReply](ctx, client, contract.CardFreezePath, in, http.StatusOK)
	if err != nil {
		return empty, err
	}
	if out.Stored == nil || out.ResponseJSON == "" {
		return empty, ErrInvalidResponse
	}
	return contract.CardFreezeReply{Stored: *out.Stored, ResponseJSON: out.ResponseJSON}, nil
}

// AdvanceCardEvent 在 ctx 下用 in 原事件重排或完成，返回传输／契约错误；不改变冻结决定。
// 原端点返回 advanced=false 仍为正常幂等响应，不把已经推进的重复请求当成新的事件。
func (c *Client) AdvanceCardEvent(ctx context.Context, in contract.CardAdvanceRequest) error {
	ctx, cancel, client := c.cardBound(ctx)
	defer cancel()
	// false 是已推进后的正常幂等值，字段缺失则不能确认端点已处理请求。
	type cardAdvanceWireReply struct {
		Advanced *bool `json:"advanced"` /* 原实际推进标志，nil 表示缺字段 */
	}
	out, err := post[cardAdvanceWireReply](ctx, client, contract.CardAdvancePath, in, http.StatusOK)
	if err == nil && out.Advanced == nil {
		return ErrInvalidResponse
	}
	return err
}

// ReplyCard 在 ctx 下发送 in 原冻结回复，返回发送错误或未受理错误，不重算卡余额。
func (c *Client) ReplyCard(ctx context.Context, in contract.CardReplyRequest) error {
	ctx, cancel, client := c.cardBound(ctx)
	defer cancel()
	out, err := post[contract.CardDeliveryReply](ctx, client, contract.CardReplyPath, in, http.StatusOK)
	if err == nil && !out.Accepted {
		err = errors.New("card reply not accepted")
	}
	return err
}

// ResolveCardPort 在 ctx 下读取 deviceID／portNo 原端口码，返回开通事实或传输／契约错误；只读。
func (c *Client) ResolveCardPort(ctx context.Context, deviceID string, portNo uint8) (contract.CardPortReply, error) {
	ctx, cancel, client := c.cardBound(ctx)
	defer cancel()
	path := contract.CardPortPath + "?device_id=" + url.QueryEscape(deviceID) + "&port_no=" + strconv.Itoa(int(portNo))
	// cardPortWireReply 是卡端口查询的解码投影，以可空 Found 区分缺字段与确实未开通，不把缺失开通事实默认为 false。
	type cardPortWireReply struct {
		Found    *bool  `json:"found"`     // nil 表示缺少开通事实，false 为确实未开通
		PortCode string `json:"port_code"` // 已开通时要求存在原业务码
	}
	out, err := get[cardPortWireReply](ctx, client, path)
	if err != nil {
		return contract.CardPortReply{}, err
	}
	if out.Found == nil || *out.Found && out.PortCode == "" {
		return contract.CardPortReply{}, ErrInvalidResponse
	}
	return contract.CardPortReply{Found: *out.Found, PortCode: out.PortCode}, nil
}

// cardBound 为 ctx 派生5秒期限并返回复用原连接池、禁止重定向的具名客户端副本及取消函数。
// 不打开连接，不修改进程拥有的 HTTP 客户端；调用方必须释放期限，取消终止当前请求。
func (c *Client) cardBound(ctx context.Context) (context.Context, context.CancelFunc, *Client) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	out := *c
	httpClient := &http.Client{Timeout: 5 * time.Second}
	if c.transport.HTTP != nil {
		copy := *c.transport.HTTP
		httpClient = &copy
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	out.transport.HTTP = httpClient
	return ctx, cancel, &out
}
