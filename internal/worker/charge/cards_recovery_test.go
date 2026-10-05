package charge

import (
	"context"
	"encoding/json"
	"errors"
	central "github.com/ChargePilot2026/charge-pilot/internal/contracts/central"
	gateway "github.com/ChargePilot2026/charge-pilot/internal/contracts/gateway"
	"github.com/ChargePilot2026/charge-pilot/internal/protocol"
	"github.com/google/uuid"
	"reflect"
	"testing"
)

// recoveryCards 仅模拟持久投递接口，不宣称证明真实库的冻结或设备消费。
type recoveryCards struct {
	event          gateway.CardEvent     // 原事件及模拟持久冻结值
	replies        []gateway.CardReply   // 发送的原回复
	advances       []gateway.CardAdvance // 原事件状态推进
	freezeCalls    int                   // 冻结请求次数
	loseFirstReply bool                  // 模拟首个回复发送后响应丢失
}

// CardEvents 返回 ctx 下同一个待投递原事件 fixture，无 IO。
func (f *recoveryCards) CardEvents(ctx context.Context) ([]gateway.CardEvent, error) {
	return []gateway.CardEvent{f.event}, nil
}

// FreezeCardResponse 在 ctx 下按 in 原号模拟首写，返回权威原 JSON 或身份错误，无真实库 IO。
func (f *recoveryCards) FreezeCardResponse(ctx context.Context, in gateway.CardFreeze) (gateway.CardFrozen, error) {
	f.freezeCalls++
	if in.EventKey != f.event.EventKey {
		return gateway.CardFrozen{}, errors.New("wrong freeze event")
	}
	stored := f.event.Response == ""
	if stored {
		f.event.Response = in.ResponseJSON
	}
	return gateway.CardFrozen{Stored: stored, ResponseJSON: f.event.Response}, nil
}

// AdvanceCardEvent 记录 ctx 下 in 的原推进；不清除冻结回复，无 IO。
func (f *recoveryCards) AdvanceCardEvent(ctx context.Context, in gateway.CardAdvance) error {
	f.advances = append(f.advances, in)
	return nil
}

// ReplyCard 记录 ctx 下 in，模拟第一次发送后的未知结果，不改变原回复，无真实设备 IO。
func (f *recoveryCards) ReplyCard(ctx context.Context, in gateway.CardReply) error {
	f.replies = append(f.replies, in)
	if f.loseFirstReply && len(f.replies) == 1 {
		return errors.New("reply response lost")
	}
	return nil
}

// ResolveCardPort 返回 ctx/device/port 的原端口 fixture，无 IO。
func (f *recoveryCards) ResolveCardPort(ctx context.Context, device string, port uint8) (gateway.CardPort, error) {
	return gateway.CardPort{Found: true, PortCode: "original-port"}, nil
}

// recoveryCentral 记录资金查询次数，模拟明确余额不足和之后改变的余额；无真实资金副作用。
type recoveryCentral struct {
	swipes, balances int   /* 原操作调用计数 */
	cents            int64 /* 当前可花费分值 */
}

// SwipeCard 记录 ctx/in 原事件并返回明确不足余额；无真实扣款。
func (f *recoveryCentral) SwipeCard(ctx context.Context, in central.CardSwipe) (central.CardOperation, error) {
	f.swipes++
	return central.CardOperation{}, &central.CardDecline{InsufficientBalance: true}
}

// CardBalance 返回 ctx/card 原卡当前余额 fixture，记录次数，无 IO。
func (f *recoveryCentral) CardBalance(ctx context.Context, card string) (central.CardBalance, error) {
	f.balances++
	return central.CardBalance{Valid: true, BalanceCents: f.cents}, nil
}

// TestFrozenCardReplyIsUsedWithoutNewDecision 钉住已经冻结的原设备、会话及余额回复，禁止再次决策。
func TestFrozenCardReplyIsUsedWithoutNewDecision(t *testing.T) {
	key := uuid.NewString()
	original := gateway.CardReply{DeviceID: "original-device", Session: "010203040506", Kind: protocol.CommandCardBalance, CardNumber: 17, BalanceUnits: 88}
	raw, _ := json.Marshal(original)
	edge := &recoveryCards{event: gateway.CardEvent{EventKey: key, Payload: "already frozen input must not be reinterpreted", Response: string(raw)}}
	core := &recoveryCentral{cents: 9000}
	done, err := (CardDispatcher{Gateway: edge, Central: core}).Run(context.Background())
	if err != nil || done != 1 || len(edge.replies) != 1 || !reflect.DeepEqual(edge.replies[0], original) {
		t.Fatalf("frozen reply changed: done=%d replies=%+v err=%v", done, edge.replies, err)
	}
	if core.swipes != 0 || core.balances != 0 || edge.freezeCalls != 0 || len(edge.advances) != 1 || edge.advances[0].EventKey != key || edge.advances[0].Outcome != "done" {
		t.Fatalf("frozen event was decided again: core=%+v edge=%+v", core, edge)
	}
}

// TestLostCardReplyReusesFrozenOriginalBalance 模拟发送后响应丢失及原事件重排，余额变化不得改写回复。
func TestLostCardReplyReusesFrozenOriginalBalance(t *testing.T) {
	key := uuid.NewString()
	event := protocol.Event{EventID: key, Type: protocol.CardSwipe, DeviceID: "device", CardNumber: 17, Port: 1}
	raw, _ := json.Marshal(event)
	edge := &recoveryCards{event: gateway.CardEvent{EventKey: key, Payload: string(raw)}, loseFirstReply: true}
	core := &recoveryCentral{cents: 889}
	dispatcher := CardDispatcher{Gateway: edge, Central: core}
	done, err := dispatcher.Run(context.Background())
	if err == nil || done != 0 || len(edge.replies) != 1 || edge.replies[0].BalanceUnits != 88 || edge.replies[0].Invalid || len(edge.advances) != 1 || edge.advances[0].Outcome != "retry" {
		t.Fatalf("first unknown result: done=%d err=%v edge=%+v", done, err, edge)
	}
	core.cents = 9000
	done, err = dispatcher.Run(context.Background())
	if err != nil || done != 1 || len(edge.replies) != 2 || !reflect.DeepEqual(edge.replies[0], edge.replies[1]) || core.swipes != 1 || core.balances != 1 || edge.freezeCalls != 1 || len(edge.advances) != 2 || edge.advances[1].EventKey != key || edge.advances[1].Outcome != "done" {
		t.Fatalf("recovery changed original decision: done=%d err=%v core=%+v edge=%+v", done, err, core, edge)
	}
}
