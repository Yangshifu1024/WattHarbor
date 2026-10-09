package gateway

import (
	contract "chargepilot/internal/contracts/gateway"
	"context"
	"errors"
	"github.com/google/uuid"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// CardEvents 查询待处理卡事件和已保存回复，不修改投递状态。
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

// FreezeCardResponse 保存事件的首次处理结果；重复调用返回已保存的回复，不生成新事件。
func (c *Client) FreezeCardResponse(ctx context.Context, in contract.CardFreezeRequest) (contract.CardFreezeReply, error) {
	ctx, cancel, client := c.cardBound(ctx)
	defer cancel()
	if uuid.Validate(in.EventKey) != nil || in.ResponseJSON == "" {
		return contract.CardFreezeReply{}, errors.New("invalid card freeze identity")
	}
	var empty contract.CardFreezeReply
	// cardFreezeWireReply 用指针区分缺少 stored 字段和 stored=false 的重复调用结果。
	type cardFreezeWireReply struct {
		Stored       *bool  `json:"stored"`        // nil 表示字段缺失，false 表示已有保存结果
		ResponseJSON string `json:"response_json"` // 首次保存的回复
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

// AdvanceCardEvent 更新事件的重试或完成状态，不改变已保存的回复。
// advanced=false 可以表示重复请求，不能当作新事件。
func (c *Client) AdvanceCardEvent(ctx context.Context, in contract.CardAdvanceRequest) error {
	ctx, cancel, client := c.cardBound(ctx)
	defer cancel()
	// false 可以表示此前已完成更新，字段缺失则不能确认请求已处理。
	type cardAdvanceWireReply struct {
		Advanced *bool `json:"advanced"` /* 是否实际更新状态，nil 表示缺字段 */
	}
	out, err := post[cardAdvanceWireReply](ctx, client, contract.CardAdvancePath, in, http.StatusOK)
	if err == nil && out.Advanced == nil {
		return ErrInvalidResponse
	}
	return err
}

// ReplyCard 将已保存的回复发送给设备，不重新计算余额。
func (c *Client) ReplyCard(ctx context.Context, in contract.CardReplyRequest) error {
	ctx, cancel, client := c.cardBound(ctx)
	defer cancel()
	out, err := post[contract.CardDeliveryReply](ctx, client, contract.CardReplyPath, in, http.StatusOK)
	if err == nil && !out.Accepted {
		err = errors.New("card reply not accepted")
	}
	return err
}

// ResolveCardPort 查询设备端口是否开通及其业务编码。
func (c *Client) ResolveCardPort(ctx context.Context, deviceID string, portNo uint8) (contract.CardPortReply, error) {
	ctx, cancel, client := c.cardBound(ctx)
	defer cancel()
	path := contract.CardPortPath + "?device_id=" + url.QueryEscape(deviceID) + "&port_no=" + strconv.Itoa(int(portNo))
	// cardPortWireReply 用指针区分缺少 found 字段和 found=false 的未开通结果。
	type cardPortWireReply struct {
		Found    *bool  `json:"found"`     // nil 表示缺少开通事实，false 为确实未开通
		PortCode string `json:"port_code"` // 已开通时要求存在业务码
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

// cardBound 创建五秒请求超时并复制客户端配置；共享连接池，不跟随重定向。
// 调用方必须调用取消函数。
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
