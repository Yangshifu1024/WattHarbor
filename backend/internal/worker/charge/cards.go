package charge

import (
	central "chargepilot/internal/contracts/central"
	gateway "chargepilot/internal/contracts/gateway"
	"chargepilot/internal/protocol"
	"chargepilot/internal/protocol/dc589"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"strconv"
)

// CardGateway 只提供事件清单、冻结、回复、更新及端口码，不暴露网络配置或数据库。
type CardGateway interface {
	// CardEvents 在 context 下返回待投递集合及冻结值，失败返回错误；不更新。
	CardEvents(context.Context) ([]gateway.CardEvent, error)
	// FreezeCardResponse 在 context 下按请求首次写入并返回已保存的 JSON，失败返回错误。
	FreezeCardResponse(context.Context, gateway.CardFreezeRequest) (gateway.CardFreezeReply, error)
	// AdvanceCardEvent 在 context 下按事件重排或完成，失败返回错误。
	AdvanceCardEvent(context.Context, gateway.CardAdvanceRequest) error
	// ReplyCard 在 context 下向设备会话发送回复，失败返回错误。
	ReplyCard(context.Context, gateway.CardReplyRequest) error
	// ResolveCardPort 在 context 下读取设备及端口序号的业务码或错误。
	ResolveCardPort(context.Context, string, uint8) (gateway.CardPortReply, error)
}

// CardCentral 只提供事件决策和卡余额，不执行投递或拥有设备连接。
type CardCentral interface {
	// SwipeCard 在 context 下执行事件并返回操作、明确拒绝或未知结果错误。
	SwipeCard(context.Context, central.CardSwipeRequest) (central.CardOperationReply, error)
	// CardBalance 在 context 下返回卡有效性及可花费分值或错误。
	CardBalance(context.Context, string) (central.CardBalanceReply, error)
}

// CardDispatcher 编排卡事件决策和回复；冻结完成后只重发回复及 event_key。
type CardDispatcher struct {
	Gateway CardGateway // cmd 注入的专用 gateway 客户端
	Central CardCentral // cmd 注入的专用 central 客户端
}

// Run 处理一页卡事件，返回已更新数与首个失败；每次失败按事件重排。
// 成功决策先首次写入冻结，再发送拒绝／余额回复，最后更新；任一步未知结果保留号供下一轮恢复。
func (d CardDispatcher) Run(ctx context.Context) (int, error) {
	if d.Gateway == nil || d.Central == nil {
		return 0, errors.New("card dispatcher is not configured")
	}
	list, err := d.Gateway.CardEvents(ctx)
	if err != nil {
		return 0, err
	}
	done := 0
	var first error
	for _, item := range list {
		reply, err := d.decisionFor(ctx, item.EventKey, item.Payload, item.Response)
		if err == nil && !reply.Accepted {
			err = d.Gateway.ReplyCard(ctx, reply)
		}
		if err != nil {
			if first == nil {
				first = err
			}
			message := err.Error()
			if len(message) > 255 {
				message = message[:255]
			}
			if aerr := d.Gateway.AdvanceCardEvent(ctx, gateway.CardAdvanceRequest{EventKey: item.EventKey, Outcome: "retry", Error: message}); aerr != nil {
				return done, aerr
			}
			continue
		}
		if err := d.Gateway.AdvanceCardEvent(ctx, gateway.CardAdvanceRequest{EventKey: item.EventKey, Outcome: "done"}); err != nil {
			return done, err
		}
		done++
	}
	return done, first
}

// decisionFor 恢复 key 对应 frozen 或解析 payload 决策并首次写入冻结，返回最终回复或错误。
// 竞争失败使用胜出回复，不根据当前余额重算；本方法不更新投递，发送后未知由冻结值恢复。
func (d CardDispatcher) decisionFor(ctx context.Context, key, payload, frozen string) (gateway.CardReplyRequest, error) {
	var reply gateway.CardReplyRequest
	if frozen != "" {
		return reply, json.Unmarshal([]byte(frozen), &reply)
	}
	var event protocol.Event
	if json.Unmarshal([]byte(payload), &event) != nil || event.EventID != key || uuid.Validate(event.EventID) != nil {
		return reply, errors.New("card event identity mismatch")
	}
	reply, err := d.decide(ctx, event)
	if err != nil {
		return reply, err
	}
	raw, _ := json.Marshal(reply)
	finish, err := d.Gateway.FreezeCardResponse(ctx, gateway.CardFreezeRequest{EventKey: key, ResponseJSON: string(raw)})
	if err != nil {
		return reply, err
	}
	if !finish.Stored {
		if err := json.Unmarshal([]byte(finish.ResponseJSON), &reply); err != nil {
			return reply, err
		}
	}
	return reply, nil
}

// decide 按 e 卡事件获取 central 决定或余额，返回协议回复或未知结果错误。
// 明确不足余额映射非无效卡拒绝；协议金额单位和饱和继续复用 dc589 规则，不复制资金逻辑。
func (d CardDispatcher) decide(ctx context.Context, e protocol.Event) (gateway.CardReplyRequest, error) {
	reply := gateway.CardReplyRequest{DeviceID: e.DeviceID, Session: hex.EncodeToString(e.SessionID[:]), CardNumber: e.CardNumber}
	cardNo := strconv.FormatUint(uint64(e.CardNumber), 10)
	if e.Type == protocol.CardSwipe {
		if e.Port == 0xFF {
			reply.Kind, reply.Invalid = protocol.CommandCardDenied, true
			return reply, nil
		}
		resolved, err := d.Gateway.ResolveCardPort(ctx, e.DeviceID, e.Port)
		if err != nil {
			return reply, err
		}
		if !resolved.Found {
			return reply, errors.New("card port not provisioned")
		}
		op, err := d.Central.SwipeCard(ctx, central.CardSwipeRequest{EventID: e.EventID, CardNo: cardNo, PortID: resolved.PortCode, OccurredAt: e.ReceivedAt})
		if err == nil {
			if uuid.Validate(op.OperationID) != nil {
				return reply, errors.New("card operation response missing identity")
			}
			reply.Accepted = true
			return reply, nil
		}
		if !errors.Is(err, central.ErrCardDeclined) {
			return reply, err
		}
		reply.Kind = protocol.CommandCardDenied
		var declined *central.CardDeclineReply
		reply.Invalid = !errors.As(err, &declined) || !declined.InsufficientBalance
	} else if e.Type == protocol.CardBalanceQuery {
		reply.Kind = protocol.CommandCardBalance
		if e.CardNumber == 0 {
			reply.Invalid = true
			return reply, nil
		}
	} else {
		return reply, errors.New("unexpected card event type")
	}
	balance, err := d.Central.CardBalance(ctx, cardNo)
	if err != nil {
		return reply, err
	}
	reply.Invalid = reply.Invalid || !balance.Valid
	reply.BalanceUnits = dc589.CardBalanceUnits(balance.BalanceCents)
	return reply, nil
}
