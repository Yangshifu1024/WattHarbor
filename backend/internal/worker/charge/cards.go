package charge

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"strconv"
	central "wattharbor/internal/contracts/central"
	gateway "wattharbor/internal/contracts/gateway"
	"wattharbor/internal/protocol"
	"wattharbor/internal/protocol/dc589"
)

// CardGateway 提供网关中的刷卡事件、已保存回复和设备发送接口。
type CardGateway interface {
	// CardEvents 读取待处理刷卡事件及其已保存回复。
	CardEvents(context.Context) ([]gateway.CardEvent, error)
	// FreezeCardResponse 保存首次回复；已有回复时返回原内容。
	FreezeCardResponse(context.Context, gateway.CardFreezeRequest) (gateway.CardFreezeReply, error)
	// AdvanceCardEvent 标记事件完成，或安排稍后重试。
	AdvanceCardEvent(context.Context, gateway.CardAdvanceRequest) error
	// ReplyCard 向事件对应的设备连接发送拒绝或余额回复。
	ReplyCard(context.Context, gateway.CardReplyRequest) error
	// ResolveCardPort 根据设备编号和端口序号查询业务端口编码。
	ResolveCardPort(context.Context, string, uint8) (gateway.CardPortReply, error)
}

// CardCentral 提供刷卡扣款、启动或加时，以及卡余额查询。
type CardCentral interface {
	// SwipeCard 按刷卡事件编号执行操作；明确拒绝与请求结果未知使用不同错误。
	SwipeCard(context.Context, central.CardSwipeRequest) (central.CardOperationReply, error)
	// CardBalance 返回卡是否有效及可用余额，金额单位为分。
	CardBalance(context.Context, string) (central.CardBalanceReply, error)
}

// CardDispatcher 处理刷卡事件；回复保存后，重试始终使用原内容。
type CardDispatcher struct {
	Gateway CardGateway // cmd 注入的专用 gateway 客户端
	Central CardCentral // cmd 注入的专用 central 客户端
}

// Run 先保存处理结果，再发送拒绝或余额回复，最后标记事件完成。
// 启动或加时已受理时不另发卡回复；失败事件延后重试，返回完成数和首个错误。
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

// decisionFor 优先读取已保存回复；首次处理才查询卡服务并保存结果。
// 并发处理已抢先保存时，使用对方保存的回复，避免余额变化后回复不一致。
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

// decide 调用卡服务执行刷卡或查询余额，再生成设备回复。
// 刷卡受理后无需卡回复；余额不足和无效卡使用不同拒绝标记。
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
