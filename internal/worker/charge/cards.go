package charge

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	central "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
	gateway "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol/dc589"
	"github.com/google/uuid"
	"strconv"
)

// CardGateway 只提供原事件清单、冻结、原回复、推进及端口码，不暴露网络配置或数据库。
type CardGateway interface {
	// CardEvents 在 context 下返回原待投递集合及冻结值，失败返回错误；不推进。
	CardEvents(context.Context) ([]gateway.CardEvent, error)
	// FreezeCardResponse 在 context 下按原请求首写并返回权威 JSON，失败返回错误。
	FreezeCardResponse(context.Context, gateway.CardFreezeRequest) (gateway.CardFreezeReply, error)
	// AdvanceCardEvent 在 context 下按原事件重排或完成，失败返回错误。
	AdvanceCardEvent(context.Context, gateway.CardAdvanceRequest) error
	// ReplyCard 在 context 下向原设备会话发送原回复，失败返回错误。
	ReplyCard(context.Context, gateway.CardReplyRequest) error
	// ResolveCardPort 在 context 下读取原设备及端口序号的业务码或错误。
	ResolveCardPort(context.Context, string, uint8) (gateway.CardPortReply, error)
}

// CardCentral 只提供原事件决策和原卡余额，不执行投递或拥有设备连接。
type CardCentral interface {
	// SwipeCard 在 context 下执行原事件并返回原操作、明确拒绝或未知结果错误。
	SwipeCard(context.Context, central.CardSwipeRequest) (central.CardOperationReply, error)
	// CardBalance 在 context 下返回原卡有效性及可花费分值或错误。
	CardBalance(context.Context, string) (central.CardBalanceReply, error)
}

// CardDispatcher 编排原卡事件决策和回复；冻结完成后只重发原回复及原 event_key。
type CardDispatcher struct {
	Gateway CardGateway // cmd 注入的具名 gateway 客户端
	Central CardCentral // cmd 注入的具名 central 客户端
}

// Run 在 ctx 下处理一页原卡事件，返回已推进数与首个失败；每次失败按原事件重排。
// 成功决策先首写冻结，再发送拒绝／余额回复，最后推进；任一步未知结果保留原号供下一轮恢复。
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

// decisionFor 在 ctx 下恢复 key 对应 frozen 或解析 payload 决策并首写冻结，返回权威回复或错误。
// 竞争失败使用胜出回复，不根据当前余额重算；本方法不推进投递，发送后未知由原冻结值恢复。
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

// decide 在 ctx 下按原 e 卡事件获取 central 决定或余额，返回原协议回复或未知结果错误。
// 明确不足余额映射原非无效卡拒绝；协议金额单位和饱和继续复用 dc589 规则，不复制资金逻辑。
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
